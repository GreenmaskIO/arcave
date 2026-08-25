---
title: Output and exit codes
description: arcave's output contract — JSON results on stdout, logs on stderr, the JSON shape of every command, and the two exit codes.
keywords: [arcave output, JSON, stdout, stderr, exit codes, jq, docker compose]
---

# Output and exit codes

**All results are JSON on stdout. All logs are on stderr.** That split is the contract: a script or an agent parses stdout and never has to pick data out of prose.

```shell title="stdout carries the result, and nothing else"
ID=$(arcave savepoint create nightly | jq -r .id)
```

Two exceptions: `arcave --version` prints a plain version line, and [`arcave mcp`](cli/mcp.md) writes nothing at all to stdout — that stream is its JSON-RPC channel. [`arcave init`](cli/init.md) prints no JSON either; it reports the file it wrote through the log stream.

## Exit codes

```text
0  success
1  failure — the error is reported on stderr
```

There is no third code. A savepoint that committed but whose services would not start exits `1` with the id in the message; the savepoint is complete either way.

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
| [`savepoint list`](cli/savepoint_list.md) | an array of savepoint records, oldest first; `[]` when there are none |
| [`savepoint remove`](cli/savepoint_remove.md) | `{"dry_run": …, "removed": [ … ]}` |
| [`rollback`](cli/rollback.md) | `{"rolled_back_to": …, "restored": [ … ], "images": [ … ]}` |
| [`mcp`](cli/mcp.md) | nothing — the stream is the protocol |
| [`status`](cli/status.md) | nothing; the command returns `not implemented` |

JSON is indented two spaces and ends with a newline.

## The savepoint record

The one shape three commands are built from: `savepoint create` prints it, `savepoint list` prints an array of it, `savepoint remove` puts it in `removed`, and `rollback` puts it in `rolled_back_to`.

```json
{
  "id": "1780335122",
  "name": "before-migration",
  "description": "before the v2 schema",
  "labels": { "arcave-safety-snapshot": "true" },
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [ … ],
  "images": [ … ],
  "spec": { "format": "compose-yaml" }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | The Unix second the capture started, in decimal digits. |
| `name` | string | What you passed, or `""`. |
| `description` | string | What you passed, or `""`. |
| `labels` | object | Omitted unless the record carries marks. `arcave-safety-snapshot: "true"` is the one arcave sets. |
| `created_at` | string | RFC3339Nano. |
| `stats` | array | One entry per captured volume. Omitted when there are none. |
| `images` | array | One entry per captured writable layer. Omitted when there are none. |
| `spec` | object | `{"format": "compose-yaml"}`. Absent on records predating the spec artifact, which cannot be rolled back to. |

### `stats[]`

```json
{
  "volume": {
    "object_id": "volume-named-myapp_pgdata",
    "compose_project": "myapp",
    "type": "named",
    "backend": "copy",
    "named": {
      "volume_name": "myapp_pgdata",
      "used_by": [
        { "service": "db", "target": "/var/lib/postgresql/data" }
      ]
    }
  },
  "type": "copy",
  "payload": { "size_bytes": 41172480 }
}
```

`volume.object_id` is the identity the store is keyed by — the grammar is in [Storage layout](storage_layout.md#object-ids). `volume.type` is `named`, `bind` or `anon`, and the block of that name carries its addressing:

| `type` | Block | Fields |
|---|---|---|
| `named` | `named` | `volume_name`, and `used_by[]` of `{service, target, read_only, subpath}` |
| `bind` | `bind` | `service`, `target`, `source` (the host path), `read_only` |
| `anon` | `anon` | `service`, `target`, `read_only`, `subpath` |

`payload` is the capturing backend's own bookkeeping, and only that backend reads it:

| Backend | `payload` |
|---|---|
| `copy` | `{"size_bytes": …}` — the bytes the archive holds |
| `cdc` | `{"restic_snapshot_id": …, "data_added": …, "total_bytes_processed": …}` — what this capture cost the store, and the volume's logical size |

### `images[]`

```json
{
  "service": "app",
  "replica": 1,
  "container_id": "3f1a9c0b2e77…",
  "image_id": "sha256:aaaa…",
  "base_image": "myapp:latest",
  "size_bytes": 83887104,
  "stored_bytes": 214016
}
```

`replica` is compose's own container number, counted from 1. `size_bytes` is the layer's own size; `stored_bytes` is what this capture added to the store once the blobs it shares with earlier savepoints were recognised. The gap between the two is the deduplication, in your own output.

## `rollback`

```json
{
  "rolled_back_to": { … the savepoint record … },
  "restored": [ … the volumes, in the shape stats[].volume uses … ],
  "images": [
    {
      "service": "app",
      "replica": 1,
      "image_id": "sha256:aaaa…",
      "image_ref": "arcave/myapp-app-1-myapp-latest:sp-1780335122"
    }
  ]
}
```

`image_ref` is the reference the restored layer answers to in your local daemon.

## Reading it

```shell title="The id of the savepoint just taken"
ID=$(arcave savepoint create nightly | jq -r .id)
```

```shell title="The newest savepoint in the store"
arcave savepoint list | jq -r 'max_by(.created_at).id'
```

```shell title="What a savepoint covers"
arcave savepoint list | jq -r '.[-1].stats[].volume.object_id'
```

```shell title="Layer bytes stored against layer bytes captured"
arcave savepoint list | jq '[.[].images[] | {size: .size_bytes, stored: .stored_bytes}]'
```

```shell title="Results only, in a noisy terminal"
arcave savepoint list 2>/dev/null | jq .
```

## Next step

[MCP tools](mcp_tools.md) — the same results, as an agent receives them.
