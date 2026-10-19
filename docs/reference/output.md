---
title: Output and exit codes
description: arcave's output contract — JSON results on stdout, logs on stderr, the JSON shape of every command, and the exit codes.
keywords: [arcave output, JSON, stdout, stderr, exit codes, jq, docker compose]
---

# Output and exit codes

**All results are JSON on stdout. All logs are on stderr.** That split is the contract: a script or an agent parses stdout and never has to pick data out of prose.

```shell title="stdout carries the result, and nothing else"
ID=$(arcave savepoint create nightly | jq -r .id)
```

Three exceptions: `arcave --version` prints a plain version line, [`arcave mcp`](cli/mcp.md) writes nothing at all to stdout — that stream is its JSON-RPC channel — and [`arcave init`](cli/init.md) prints nothing either; it reports the file it wrote through the log stream.

## Exit codes

```text
0    success
1    failure — the error is reported on stderr
130  interrupted by Ctrl-C or SIGTERM
```

The first Ctrl-C lets the command unwind through its own cleanup and then exits `130`; a second one exits `130` at once. A savepoint that committed but whose services would not start exits `1` with the id in the message; the savepoint is complete either way.

## The log stream

Compose's own per-container events arrive on stderr as ordinary arcave log records, in whatever `--log-format` says:

```text title="--log-format text"
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF compose kind=Container name=myapp-db-1 status=Started
INF compose kind=Container name=myapp-db-1 status=Healthy
```

```json title="--log-format json"
{"level":"info","kind":"Container","name":"myapp-db-1","status":"Healthy","message":"compose"}
```

Steps still in flight — `Waiting`, `Starting`, `Creating`, image-pull progress — are logged at debug level, so `--log-level debug` is how you watch a slow start unfold. `--progress tty` hands the terminal to compose's own renderer instead; `--progress quiet` drops those events altogether.

## What each command prints

| Command | stdout |
|---|---|
| [`init`](cli/init.md) | nothing |
| [`savepoint create`](cli/savepoint_create.md) | one savepoint record |
| [`savepoint list`](cli/savepoint_list.md) | an array of savepoint records, newest first unless `--sort asc`; `[]` when there are none |
| [`savepoint remove`](cli/savepoint_remove.md) | `{"dry_run": …, "removed": [ … ]}` |
| [`rollback`](cli/rollback.md) | `{"rolled_back_to": …, "restored": [ … ], "writable_layers": [ … ]}` |
| [`session create`](cli/session.md#session-create), [`session set`](cli/session.md#session-set), [`session unset`](cli/session.md#session-unset), [`session current`](cli/session.md#session-current) | one session record: `{"id": …, "name": …, "created_at": …, "from": {"session": …}}` |
| [`session list`](cli/session.md#session-list) | an array of session entries — the record plus `status`, `current`, `savepoint_count`, `compose_projects`, `size_bytes` — newest first unless `--sort asc` |
| [`session delete`](cli/session.md#session-delete) | `{"session": …, "deleted": true, "freed_bytes": …}` |
| [`mcp`](cli/mcp.md) | nothing — the stream is the protocol |
| [`status`](cli/status.md) | nothing; the command returns `not implemented` |

JSON is indented two spaces and ends with a newline.

## The savepoint record

The one shape four commands are built from: `savepoint create` prints it, `savepoint list` prints an array of it, `savepoint remove` puts it in `removed`, and `rollback` puts it in `rolled_back_to`.

```json
{
  "id": "019e843e-1650-7000-8000-000000000000",
  "name": "before-migration",
  "description": "before the v2 schema",
  "labels": { "arcave-safety-snapshot": "true" },
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [ … ],
  "writable_layers": [ … ],
  "images": [ … ],
  "spec": { "format": "compose-yaml" },
  "not_covered": { … }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | A UUIDv7 in canonical form, assigned at capture. |
| `name` | string | What you passed, or `""`. |
| `description` | string | What you passed, or `""`. |
| `labels` | object | Omitted unless the record carries marks. `arcave-safety-snapshot: "true"` is the one arcave sets. |
| `created_at` | string | RFC3339Nano. |
| `stats` | array | One entry per captured volume. Omitted when there are none. |
| `writable_layers` | array | One entry per container whose writable layer was captured. Omitted when there are none. |
| `images` | array | One entry per distinct image a service's `image:` names, kept so a container whose layer was left out can be rebuilt. Omitted when there are none. |
| `spec` | object | `{"format": "compose-yaml"}` — the compose spec a rollback rebuilds the project from. |
| `not_covered` | object | What the savepoint does **not** hold. Omitted when it holds everything. |

What a savepoint captures follows the `capture` defaults in the config and the `arcave.*` labels in the compose file — see [Capture labels](capture_labels.md).

### `stats[]`

```json
{
  "volume": {
    "object_id": "volume-named-myapp_pgdata",
    "compose_project": "myapp",
    "type": "named",
    "backend": "fullcopy",
    "named": {
      "volume_name": "myapp_pgdata",
      "used_by": [
        { "service": "db", "target": "/var/lib/postgresql/data" }
      ]
    }
  },
  "type": "fullcopy",
  "payload": { "size_bytes": 41172480 }
}
```

`volume.object_id` is the volume's name in the store — see [Storage layout](storage_layout.md#object-ids). `volume.type` is `named`, `bind` or `anon`, and the block of that name carries its addressing:

| `type` | Block | Fields |
|---|---|---|
| `named` | `named` | `volume_name`, and `used_by[]` of `{service, target, read_only, subpath}` |
| `bind` | `bind` | `service`, `target`, `source` (the host path), `read_only` |
| `anon` | `anon` | `service`, `replica`, `target`, `read_only`, `subpath` |

`read_only` and `subpath` are omitted when unset. `payload` is the capturing backend's own bookkeeping, and only that backend reads it:

| Backend | `payload` |
|---|---|
| `fullcopy` | `{"size_bytes": …}` — the bytes the archive holds |
| `cdc` | `{"restic_snapshot_id": …, "data_added": …, "total_bytes_processed": …}` — what this capture cost the store, and the volume's logical size |

### `writable_layers[]`

```json
{
  "service": "app",
  "replica": 1,
  "container_id": "3f1a9c0b2e77…",
  "container_name": "myapp-app-1",
  "committed_image_id": "sha256:aaaa…",
  "imagebackend": "dedupcopy",
  "upstream_image_id": "sha256:bbbb…",
  "upstream_image_ref": "myapp:latest",
  "size_bytes": 83887104,
  "stored_bytes": 214016
}
```

`replica` is compose's own container number, counted from 1, and `container_name` the name a rollback gives the container back. `committed_image_id` is the image the commit made — the image plus the container's changes. `imagebackend` is `dedupcopy` (the default: split into blobs shared across savepoints) or `fullcopy` (one whole archive), as `writable_layers.backend` or a label chose. `upstream_image_id` and `upstream_image_ref` name the image the layer sits on; they are omitted when arcave could not name it. `size_bytes` is the committed image's archive size; `stored_bytes` is what this capture added to the store once the bytes already there were recognised, and omitted when it added nothing. The gap between the two is the deduplication, in your own output.

### `images[]`

```json
{
  "id": "sha256:bbbb…",
  "refs": ["postgres:16"],
  "repo_digests": ["postgres@sha256:cccc…"],
  "size_bytes": 453246976,
  "stored_bytes": 453246976,
  "stored": true
}
```

One entry per distinct image the services run, stored once for every savepoint and replica naming it. `id` is the image's id; `refs` and `repo_digests` are its names, omitted when it has none. `stored_bytes` is what this savepoint added — omitted when the image was already in the store. `stored` is `false` when neither the store nor the daemon held the image: only its identity is recorded, with a warning, and a rollback needs it pullable to rebuild a container on it.

### `not_covered`

Every field is omitted when empty, and `not_covered` itself when all are.

| Field | Entries | Meaning |
|---|---|---|
| `mounts` | `{volume, service, replica, target, raw_type, reason, detail}` | Mounts the savepoint does not hold. `reason` is a stable code: `excluded` (a label or the config left it out), `tmpfs`, `npipe`, `image`, `cluster`, `unknown_type`, `inherited_from_outside` (a `volumes_from` of a container outside the project). `volume` names a named volume left out whole; `service` and `target` a mount. |
| `services_without_volumes` | service names | Services no captured volume belongs to; only their writable layer is held. |
| `killed_containers` | `{service, replica, container}` | Containers killed at the stop deadline: their volumes may hold a crash state. |
| `replica_gaps` | `{service, replicas, missing}` | Services whose replicas are not numbered 1..N; a rollback brings back exactly `replicas`. |
| `unresolved_images` | `{service, replica, container, image}` | Containers whose image cannot be named: only a held writable layer brings them back. |
| `writable_layers` | `{service, replica, container, reason, detail}` | Replicas whose writable layer was left out; a rollback creates them fresh from their image. |
| `images` | `{service, image, reason, detail}` | Services whose image bytes were left out. |

```json
{
  "mounts": [
    { "service": "app", "target": "/tmp/cache", "raw_type": "tmpfs", "reason": "tmpfs" },
    { "volume": "myapp_scratch", "raw_type": "volume", "reason": "excluded", "detail": "arcave.capture=false on volume myapp_scratch" }
  ],
  "services_without_volumes": ["worker"]
}
```

## `rollback`

```json
{
  "rolled_back_to": { … the savepoint record … },
  "restored": [ … the volumes, in the shape stats[].volume uses … ],
  "writable_layers": [
    {
      "service": "app",
      "replica": 1,
      "container_name": "myapp-app-1",
      "image_id": "sha256:aaaa…",
      "tag": "arcave/myapp-app-1-myapp-latest:sp-019e843e-1650-7000-8000-000000000000"
    }
  ]
}
```

`writable_layers` names each container layer put back, and is omitted when the savepoint held none. `tag` is the reference the restored layer answers to in your local Docker daemon: after a rollback the container runs on it, not on the image its compose file names.

## Reading it

```shell title="The id of the savepoint just taken"
ID=$(arcave savepoint create nightly | jq -r .id)
```

```shell title="The newest savepoint in the store"
arcave savepoint list | jq -r 'max_by(.created_at).id'
```

```shell title="What a savepoint covers"
arcave savepoint list | jq -r '.[0].stats[].volume.object_id'
```

```shell title="Layer bytes stored against layer bytes captured"
arcave savepoint list | jq '[.[] | (.writable_layers // [])[] | {size: .size_bytes, stored: (.stored_bytes // 0)}]'
```

```shell title="What a savepoint left out"
arcave savepoint list | jq '.[0].not_covered // "nothing"'
```

```shell title="Results only, in a noisy terminal"
arcave savepoint list 2>/dev/null | jq .
```

## Next step

[MCP tools](mcp_tools.md) — the same results, as an agent receives them.
