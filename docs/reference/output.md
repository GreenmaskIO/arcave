---
title: Output and exit codes
description: arcave's output contract — JSON results on stdout, logs on stderr, the JSON shape of every command, and the exit codes.
keywords: [arcave output, JSON, stdout, stderr, exit codes, jq, docker compose]
---

# Output and exit codes

**Results are JSON on stdout. All logs are on stderr.** That split is the contract: a script or an agent parses stdout and never has to pick data out of prose. `--progress json` is the one mode that puts more on stdout, and it says so line by line — see [Progress](#progress). A person at a terminal gets the result as text instead, in two cases only — see [Result format](#result-format).

```shell title="stdout carries the result, and nothing else"
ID=$(arcave savepoint create nightly | jq -r .id)
```

Three exceptions: `arcave --version` prints a plain version line, [`arcave mcp`](cli/mcp.md) writes nothing at all to stdout — that stream is its JSON-RPC channel — and [`arcave init`](cli/init.md) prints nothing either; it reports the file it wrote through the log stream.

## Result format

`--format` (or `ARCAVE_FORMAT`) decides how the result is printed:

| Case | stdout |
|---|---|
| `--format text` | text |
| `--format json` | JSON |
| `--format auto`, the default, with the live tree drawn and stdout a terminal | text |
| `--format auto`, anything else | JSON |
| `--progress json` | the JSON stream; `--format text` beside it is refused |

The tree is drawn on stderr and the result goes to stdout, so `ID=$(arcave savepoint create)` captures JSON even on a terminal: `auto` checks both. Text is for reading, not parsing — its shape may change; a type of result the text printer does not know is printed as JSON.

```text title="savepoint create, as text"
✔ savepoint.create nightly         15.4s
├── ✔ project.introspect            0.2s
├── ✔ compose.stop                  0.4s
├── ✔ savepoint.capture            12.1s
└── ✔ compose.start                 2.7s

Savepoint nightly · 01a188c4 · 19:45:35 · stored 4.7 MB
            count     size   stored
  volumes       3  48.8 MB   4.7 MB
  layers        4   275 MB  44.1 KB
  images        4   1.1 GB        0

Warnings (1)
  ! worker has no volumes: only its writable layer is held

  roll back:  arcave -p myapp rollback nightly
```

`stored` is what this savepoint added to the store, by kind and, in the title, in all; `size` is what it holds. The kinds are not added up: a layer's archive carries its image's layers, so only the title's total is what the savepoint wrote. The volumes by name are in the JSON result. `Warnings (N)` lists what puts the data at risk first — a container killed at the stop deadline, a replica gone, an image not stored — then what only informs, each hint after a `→`. On a terminal the title is bold and the warnings yellow; written to a file, or under `NO_COLOR`, the text carries no color.

Beside a text result the final tree keeps every stage of the run as a line with its time, and below the stages only what the result does not say: a step that failed, or warned of something the result leaves out, with its path from the root. A failed run that prints no result leaves the whole tree, and its error once, as the `Error:` line below it; a partial `gc` prints its result and keeps only what that result does not say. The final tree writes five warnings per step at most and counts the rest; `--progress log` shows all of them.

## Exit codes

```text
0    success
1    failure — the error is reported on stderr, or as the last line under --progress json
130  interrupted by Ctrl-C or SIGTERM
```

The first Ctrl-C lets the command unwind through its own cleanup and then exits `130`; a second one exits `130` at once. A savepoint that committed but whose services would not start exits `1` with the id in the message; the savepoint is complete either way.

## Progress

`--progress` decides how a run shows what it is doing; the result stays the result whatever it says.

- **`tty`** draws the workflow as a live tree on stderr — every step, the bytes of every volume and layer — a bar where the total is known exactly, when a volume or layer is restored and for every `cdc` volume, and the bytes with their rate while a capture runs — a warning under the step it concerns — and leaves the final tree in the scrollback before the result goes to stdout. Only warnings print above the tree unless a level is set, by `--log-level`, `ARCAVE_LOG_LEVEL` or `log.level` in the config. `auto`, the default, picks it when stderr is a terminal and `log.format` is `text`.
- **`log`** writes one log record per step event, named by its path in the tree:

  ```text
  INF rollback[sp1]/rollback.apply/volume.restore/volume[volume-named-myapp_pgdata] bytes=1.2GB/1.2GB elapsed=8.1s status=succeeded
  ```

- **`json`** puts the whole run on stdout as JSON lines, one per event, and the result last:

  ```json
  {"type":"progress","event":{"node":3,"status":"started","open":{"parent":1,"step":{"kind":"compose.stop","type":"activity"}},"sample":{"elapsed_ns":0},"at":"…"}}
  {"type":"progress","event":{"node":3,"status":"succeeded","sample":{"elapsed_ns":2100000000},"at":"…"}}
  {"type":"result","result":{ … }}
  ```

  A failure ends the stream with `{"type":"error","error":"…"}` instead, and a partial `gc` with its result and its `error` on one line. A failure before the output exists — an unreadable config, a bad flag — is still plain text on stderr. `node` is the step's id — the Go API calls it `StepID` — and `open.parent` its parent's. Rebuild the tree by `open.parent`; each later event of a node replaces its state whole, but a `warned` or resource event's `msg` is that event's alone: the node's own `msg` is the last plain update's.

### Step kinds

Every node's `open.step.kind` is one of these, and they are part of the stream's contract: an action is `<namespace>.<action>`, an object a bare noun, its key in `step.id` or its name in `step.name`.

| Kind | What it is |
|---|---|
| `savepoint.create`, `savepoint.remove`, `rollback`, `gc`, `session.fork`, `session.remove` | the root of the command that ran |
| `session.lock` | the wait for another holder of the session's lock; reported only when there is one |
| `project.introspect` | reading the live compose project |
| `compose.stop`, `compose.down`, `compose.create`, `compose.start` | a compose operation, one `resource` per container |
| `savepoint.capture` | one savepoint's capture, a safety snapshot's included |
| `volume.capture`, `volume.restore` → `volume` | a batch over the volumes, one `volume` per volume |
| `layer.capture`, `layer.restore` → `layer` | a batch over the writable layers, one `layer` per container |
| `image.capture` → `image` | a batch over the images, one `image` per image |
| `replica.rebuild` → `replica` | the replicas a rollback recreates under their recorded number |
| `savepoint.drop` → `savepoint` | the savepoints a remove takes |
| `rollback.prepare`, `rollback.safety-snapshot`, `rollback.apply`, `rollback.revert`, `rollback.cleanup` | a rollback's phases |
| `session.copy` → `key` | a fork's copy, one `key` per top-level directory or object |
| `gc.find` → `collector`, `gc.find` → `compose-project` → `collector` | what each collector found: the daemon's, then each compose project's |
| `gc.erase` → `eraser`, `gc.erase` → `compose-project` → `eraser`, then → `gc.delete` → `garbage` | the deletions, one `garbage` per target — per repository for a restic `forget` |

[`arcave mcp`](cli/mcp.md) always logs its progress: a tool's answer is its result alone.

## Compose's events

Compose's own per-object events — a container `Stopping`, `Stopped`, `Healthy` — land on the step that caused them: `compose.stop`, `compose.down`, `compose.create`, `compose.start`. The tree shows one line per container under that step. Under `--progress log` each is a record of the step, at debug level while the object is under way and at info once it has settled:

```text
DBG rollback[sp1]/rollback.apply/compose.start msg=Starting resource=container:myapp-db-1 status=in_progress
INF rollback[sp1]/rollback.apply/compose.start msg=Healthy resource=container:myapp-db-1 status=in_progress
```

so `--log-level debug` is how you watch a slow start unfold. In the `--progress json` stream an event about an object carries `resource`, and `"settled": true` once the object has reached its outcome while the step goes on. A `warned` event carries `"in_result": true` when the result repeats the warning — what a savepoint leaves out, a safety snapshot kept, what a gc could not judge or held back.

`--compose-progress tty` hands the terminal to compose's own renderer instead — under `--progress log` only, since two renderers cannot share one terminal; `--compose-progress quiet` drops those events altogether.

## What each command prints

The JSON each command prints; [as text](#result-format), each is a summary, a table or a line.

| Command | stdout |
|---|---|
| [`init`](cli/init.md) | nothing |
| [`savepoint create`](cli/savepoint_create.md) | one savepoint record |
| [`savepoint list`](cli/savepoint_list.md) | an array of savepoint records, newest first unless `--sort asc`; `[]` when there are none |
| [`savepoint remove`](cli/savepoint_remove.md) | `{"dry_run": …, "removed": [ … ]}` |
| [`rollback`](cli/rollback.md) | `{"rolled_back_to": …, "restored": [ … ], "writable_layers": [ … ], "safety_snapshot": "<id>"}` |
| [`session create`](cli/session.md#session-create), [`session set`](cli/session.md#session-set), [`session unset`](cli/session.md#session-unset), [`session current`](cli/session.md#session-current) | one session record: `{"id": …, "name": …, "created_at": …, "from": {"session": …}}` |
| [`session list`](cli/session.md#session-list) | an array of session entries — the record plus `status`, `current`, `savepoint_count`, `safety_snapshot_count`, `compose_projects`, `size_bytes` — newest first unless `--sort asc` |
| [`session show`](cli/session.md#session-show) | `{"session": …, "current": …, "from": {"id": …, "name": …}, "savepoint_count": …, "safety_snapshot_count": …, "compose_projects": [{"compose_project": …, "savepoints": [ … ]}], "checked_at": …}` — compose projects by name, each one's savepoints newest first as `savepoint list` prints them, safety snapshots only under `--include-safety-snapshots` |
| [`session remove`](cli/session.md#session-remove) | `{"session": …, "removed": true, "freed_bytes": …}` |
| [`tools generate-compose`](cli/tools_generate_compose.md) | `{"compose_project": …, "services": [ … ], "path": …}` — the compose file itself goes to `-o` |
| [`gc`](cli/gc.md) | `{"dry_run": …, "session": …, "reports": [ … ], "savepoints": [ … ], "total": { … }}` — one report per kind and compose project of the session, each with its `deletion` past a dry run, and what each dead savepoint frees; `total` adds `deleted`, `delete_failed`, `not_run` and `freed_bytes` when gc deleted |
| [`mcp`](cli/mcp.md) | nothing — the stream is the protocol |
| [`status`](cli/status.md) | one status object — `state`, the `deployment` the containers' labels name, `services` with every replica, `rollback_in_flight`, `unreadable` and `notices` — printed before the error when the status is partial |

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
  "size_bytes": 41172480,
  "stored_bytes": 3948691,
  "payload": { "compression": "zstd" }
}
```

`volume.object_id` is the volume's name in the store — see [Storage layout](storage_layout.md#object-ids). `volume.type` is `named`, `bind` or `anon`, and the block of that name carries its addressing:

| `type` | Block | Fields |
|---|---|---|
| `named` | `named` | `volume_name`, and `used_by[]` of `{service, target, read_only, subpath}` |
| `bind` | `bind` | `service`, `target`, `source` (the host path), `read_only` |
| `anon` | `anon` | `service`, `replica`, `target`, `read_only`, `subpath` |

`read_only` and `subpath` are omitted when unset. `size_bytes` is the volume as the backend measured it — the tar archive for `fullcopy`, the bytes read for `cdc` — and `stored_bytes` what this capture added to the store, omitted when it added nothing: `fullcopy` stores the archive whole, zstd-compressed unless `backends.fullcopy.compression` is `none`, when the two are equal; `cdc` stores only the chunks that changed, compressed. `reused_bytes` is what of `size_bytes` the backend found already stored, omitted when none was: `cdc`'s unchanged chunks, counted before compression, so `stored_bytes` and `reused_bytes` need not add up to `size_bytes`. `payload` is the capturing backend's own bookkeeping, and only that backend reads it:

| Backend | `payload` |
|---|---|
| `fullcopy` | `{"compression": …}` — `zstd` or `none`, the format the archive is stored in |
| `cdc` | `{"restic_snapshot_id": …}` — the restic snapshot the capture made |

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

One entry per distinct image the services run, stored once for every savepoint and replica naming it. `id` is the image's id; `refs` and `repo_digests` are its names, omitted when it has none. `stored_bytes` is what this savepoint added — omitted when the image was already in the store. `stored` is `false` when neither the store nor the daemon held the image, or the daemon failed to hand it over: only its identity is recorded, with a warning, and a rollback needs it pullable to rebuild a container on it.

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
  ],
  "safety_snapshot": "019e8491-04c8-7000-8000-000000000000"
}
```

`writable_layers` names each container layer put back, and is omitted when the savepoint held none. `tag` is the reference the restored layer answers to in your local Docker daemon: after a rollback the container runs on it, not on the image its compose file names.

`safety_snapshot` is the id of the safety snapshot the rollback took: rolling back to it undoes the rollback. It is omitted when none was taken: the project had no containers, or `rollback.safety_snapshot` or `--safety-snapshot` turned it off. In that last case the stream has no `rollback.safety-snapshot` step, and a warning, marked `in_result`, says what running without one risks.

## `status`

```json
{
  "compose_project": "myapp",
  "checked_at": "2026-11-07T17:14:00Z",
  "state": "degraded",
  "session": {"id": "00000000-0000-0000-0000-000000000001", "name": "baseline"},
  "deployment": {
    "source": "mixed",
    "deployed_at": "2026-11-07T14:02:11Z",
    "arcave_project": "myapp",
    "session": {"id": "00000000-0000-0000-0000-000000000001", "name": "baseline"},
    "savepoint": {
      "id": "019e843e-…", "name": "before-migration", "created_at": "2026-11-06T19:41:00Z",
      "safety_snapshot": false,
      "description": "the schema before 0042",
      "restored": {"volumes": 3, "writable_layers": 2}
    },
    "replaced": 1,
    "record": "found"
  },
  "services": [
    {
      "name": "web", "state": "running", "replicas_expected": 2, "replicas_up": 2,
      "replicas": [
        {
          "number": 2, "container": "myapp-web-2", "container_id": "…", "state": "running",
          "exit_code": 0, "oom_killed": false, "restart_count": 0,
          "created_at": "…", "started_at": "…",
          "replaces": "myapp-web-2", "in_deployment": false, "in_savepoint": true,
          "ports": [{"container_port": 80, "protocol": "tcp", "host_ip": "0.0.0.0", "host_port": 8080}]
        }
      ]
    }
  ],
  "rollback_in_flight": null,
  "unreadable": [],
  "notices": [{"code": "replica-recreated", "message": "web-2 was recreated outside arcave …"}]
}
```

`state` is one of `rollback-pending`, `absent`, `stopped`, `degraded`, `starting`, `running` — see [the command](cli/status.md#the-state). `deployment` is `null` with no container, and `{"source": "compose"}` alone when no container carries a rollback's labels; `source` is `savepoint` when every container does, `mixed` otherwise. `deployed_at`, `arcave_project`, `session` and the savepoint's `id`, `name`, `created_at` and `safety_snapshot` are read off the labels, so they are there when the store is not. `record` says whether the deployed savepoint's session still holds it — `found`, `missing` (the savepoint or its session removed since), or `unreadable` — and is absent when `arcave_project` is not the one the config names, whose store is not read; the savepoint's `description` and `restored` come from that record, and are absent unless it is `found`. `replaced` counts the replicas the savepoint recorded that were recreated outside arcave since — those with `in_deployment` `false` and `in_savepoint` `true` — and is absent at `0`.

A replica's `state` is `running`, `completed`, `exited`, `restarting`, `created`, `paused`, `removing`, `dead`, or `missing` for one the deployed savepoint recorded and the daemon does not hold; `health` is `healthy`, `unhealthy` or `starting`, and absent with no healthcheck or once the container stopped. `in_deployment` marks a container the deployment built and `in_savepoint` a replica the savepoint recorded: `false`/`true` was recreated outside arcave, `false`/`false` was added since. `replaces` is the container compose recreated this one over. `replicas_expected` is absent with nothing deployed.

`session` is the session your commands work in, `null` when it could not be resolved. `rollback_in_flight` is the inflight marker: `{"phase", "session", "target", "safety", "at", "recover"}`, `recover` being the savepoint to roll back to. `unreadable` lists what could not be read — `{"source": "store" | "session" | "inflight-marker", "error"}` — and is not empty exactly when the command exits `1` with a result.

`notices` are `{"code", "message", "severe"}`, the severe first. `code` names the condition and is stable — act on it, never on `message`, whose wording may change; [the command](cli/status.md#what-the-warnings-say) lists every code and what to do about it. A status notice carries no `hint`: what to do is the documentation's. Every id in a status is whole.

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
