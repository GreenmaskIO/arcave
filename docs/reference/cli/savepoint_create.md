---
title: arcave savepoint create
description: Capture an application-consistent savepoint of a Docker Compose project — every stateful volume, every container's writable layer, and the compose spec of the project itself.
keywords: [arcave savepoint create, volume capture, writable layer, compose spec, docker compose]
---

# arcave savepoint create

Captures a savepoint: every stateful volume, every container's writable layer, and the compose spec of the project.

```text title="Synopsis"
arcave savepoint create [name] [flags]
```

The services are stopped for the capture and started again when it completes — that is what makes the result application-consistent. The sequence is [`concepts/savepoint_lifecycle.md`](../../concepts/savepoint_lifecycle.md); what ends up inside is [`concepts/what_a_savepoint_contains.md`](../../concepts/what_a_savepoint_contains.md).

## Flags

| Flag | Default | Description |
|---|---|---|
| `--name` | — | The savepoint's name. Equivalent to the positional argument. |
| `--description` | — | What this savepoint is being taken before. |

Neither is a config key: both are per-run values. Global flags apply, and [`--wait-timeout`](index.md#global-flags) matters most here — it bounds the health-check wait after the restart.

The name may be a positional argument, `--name`, or both. Both is an error only when they disagree:

```shell
arcave savepoint create before-migration
arcave savepoint create --name before-migration
arcave savepoint create before-migration --name before-migration   # fine
arcave savepoint create before-migration --name something-else     # savepoint name given both as argument and --name
```

A savepoint does not need a name. The id is assigned either way.

## Output

The savepoint record, as JSON on stdout.

```json title="stdout"
{
  "id": "1780335122",
  "name": "before-migration",
  "description": "before the v2 schema",
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [
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
  ],
  "images": [
    {
      "service": "app",
      "replica": 1,
      "container_id": "3f1a9c0b2e77…",
      "image_id": "sha256:aaaa…",
      "base_image": "myapp:latest",
      "size_bytes": 83887104,
      "stored_bytes": 214016
    }
  ],
  "spec": { "format": "compose-yaml" }
}
```

| Field | Meaning |
|---|---|
| `id` | The Unix second the capture started, in decimal digits. What `rollback` and `savepoint remove` take. |
| `name`, `description` | What you passed, or empty strings. |
| `created_at` | RFC3339Nano. |
| `stats` | One entry per captured volume, keyed by `volume.object_id` — the grammar is in [Storage layout](../storage_layout.md#object-ids). `volume.type` is `named`, `bind` or `anon`; `volume.backend` and `type` are `copy` or `cdc`. `payload` is the backend's own bookkeeping. |
| `images` | One entry per container whose writable layer was committed. `size_bytes` is the layer's own size; `stored_bytes` is what it added to the store after deduplication. |
| `spec` | The compose spec committed beside the record. `format` is always `compose-yaml`. |
| `labels` | Present only on a [safety snapshot](savepoint_list.md#safety-snapshots-are-hidden). |

Full shapes for every command are in [Output](../output.md).

## Errors

| Condition | What happens |
|---|---|
| The project is not in the daemon | `no containers found for compose project "myapp": bring the stack up once, or name another project with -p` |
| A declared named volume the daemon does not hold | `docker volume "myapp_pgdata" does not exist (bring the project up once to create it)`. An error, never a skip: an empty capture would silently replace live data at the next rollback. |
| An anonymous volume whose container is gone | `no container for service "app" in project "myapp" mounts "/data" (is the project created?)` |
| The project has no volume to capture | `no stateful volumes found; nothing to capture` |
| Two volumes render one object id | `two volumes address the same object id: service "app" at "/var/lib" and service "app-var" at "/lib" both address "volume-service-app-var-lib"; rename one of them` |
| The savepoint directory already holds objects | `capture directory is not empty: 1780335122 holds 3 object(s) — a savepoint of this same second, or an interrupted run's remains; retry, and check what is there before removing it` |
| A daemon older than Docker 25 | `unsupported image archive format: member "…" is neither a blob nor metadata; a daemon older than Docker 25 exports this shape`. See [Requirements](../requirements.md). |
| An interrupted rollback is marked | `rollback to savepoint 1780335122 was interrupted (restoring)` — see [Troubleshooting](../troubleshooting.md#an-interrupted-rollback). |
| Another arcave run holds the lock | `another arcave command is already running in this project` |
| The services do not become healthy in `--wait-timeout` | `savepoint 1780335122 committed; services failed to start: …` — **the savepoint exists and is complete.** Only the restart failed: note the id, and look at the project. |

A capture that fails before the record is committed leaves no savepoint. What the failed run wrote is named in the log and left in place; the next capture refuses that directory rather than sweeping it.

## Examples

```shell title="Before a risky change"
arcave savepoint create before-migration --description "pre v2 schema"
```

```shell title="Unnamed, in a loop where the id is what matters"
ID=$(arcave savepoint create | jq -r .id)
```

```shell title="Give a slow project more time to come back"
arcave savepoint create before-migration --wait-timeout 5m
```

```shell title="Watch a slow capture unfold"
arcave --log-level debug savepoint create before-migration
```

Compose's in-flight steps — `Waiting`, `Starting`, `Creating`, image-pull progress — are logged at debug level.

## Next step

[`arcave savepoint list`](savepoint_list.md) — find what you captured.
