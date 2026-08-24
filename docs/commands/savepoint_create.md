---
title: savepoint create
description: Capture a consistent snapshot of every stateful volume and every container's writable layer in a Docker Compose project, with the stack stopped.
keywords: [arcave savepoint create, volume snapshot, docker compose, consistent snapshot, writable layer, Open-Source, Greenmask]
---

# arcave savepoint create

Captures a consistent snapshot of the environment: every stateful volume, every container's writable layer, and the compose spec of the stack itself.

## Usage

```text
Usage: arcave savepoint create [name] [flags]
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--name` | — | Savepoint name. Equivalent to the positional argument. |
| `--description` | — | What this savepoint captures, e.g. the change it is being taken before. |

Neither is a config key — both are per-run values. Global flags apply, and [`--wait-timeout`](index.md#global-flags) matters most here: it bounds the health-check wait after the restart.

The name may be given as a positional argument, as `--name`, or both — supplying both is an error only when they disagree.

```shell
arcave savepoint create before-migration
arcave savepoint create --name before-migration
arcave savepoint create before-migration --name before-migration   # fine
arcave savepoint create before-migration --name something-else     # error
```

A savepoint does not need a name. The id is assigned either way.

## Behavior

1. Claims the project lock, and refuses if an [interrupted rollback](../concepts.md#inflight-marker) is marked.
2. Reads the running stack from the Docker daemon.
3. **Stops** the project's services, waiting up to `--stop-timeout` for each container to exit.
4. Captures every stateful volume.
5. Commits each container's writable layer and stores the image.
6. Writes `project.yaml` — the stack's compose spec.
7. Writes `savepoint.json` — the record, **last**, which is what commits the savepoint.
8. **Starts** the services and waits up to `--wait-timeout` for health checks.

If the capture fails, arcave restarts the services and reports the capture error. Nothing is committed, and the partial data is logged and left in place rather than deleted — see [How arcave works](../how_it_works.md#the-record-is-the-commit-marker).

## What gets captured

The unit is the **volume**, not the mount: every named volume the daemon reports for the project, every bind mount, and every anonymous volume. A named volume that three services mount is captured once. Non-persistent mounts — `tmpfs`, `npipe`, `image`, `cluster` — are skipped, each reported with its reason.

Full detail, including the `subpath:` caveat, is on [How arcave works](../how_it_works.md#what-a-savepoint-covers).

## Output

```json title="stdout"
{
  "id": "1780335122",
  "name": "before-migration",
  "description": "Before applying migration 2026_001",
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
      "type": "copy"
    }
  ],
  "images": [
    {
      "service": "app",
      "replica": 1,
      "container_id": "3f1a…",
      "image_id": "sha256:aaaa…",
      "base_image": "myapp:latest",
      "size_bytes": 83887104
    }
  ],
  "spec": { "format": "compose-yaml" }
}
```

| Field | Meaning |
|---|---|
| `id` | Unix timestamp, decimal digits. What `rollback` and `remove` take. |
| `name`, `description` | What you passed, or empty strings. |
| `created_at` | RFC3339Nano. |
| `stats` | One entry per captured volume. `volume.type` is `named`, `bind` or `anon`; `volume.backend` is `copy` or `cdc`. |
| `images` | One entry per container whose writable layer was committed. `size_bytes` is what that layer cost. |
| `spec` | The compose spec committed beside the record. `format` is always `compose-yaml`. |

`labels` appears only on a [safety snapshot](../concepts.md#safety-snapshot).

## Errors

| Condition | What happens |
|---|---|
| No `.arcave/config.yaml` | `config is not found: run 'arcave init' if not yet initialized` |
| No compose project named | `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME` |
| Another arcave run holds the lock | Fails immediately rather than queuing. |
| An interrupted rollback is marked | Refused until the rollback is resolved — see [Troubleshooting](../troubleshooting.md#an-interrupted-rollback). |
| The stack does not exist on the daemon | Fails saying so. `docker compose down` removes it; `stop` does not. |
| A declared named volume is missing | **Error**, not a skip. Run `docker compose up` once so the volumes exist. |
| A service's anonymous volume container is gone | The capture fails — an anonymous volume can only be reached through its container. |
| The savepoint directory is not empty | Refused, not swept. Only an interrupted run can have left objects there. |
| Services do not become healthy in `--wait-timeout` | The savepoint is committed; the command reports the start failure. |

## Examples

### Snapshot before a risky change

```shell
arcave savepoint create before-migration --description "pre v2 schema"
```

### An unnamed checkpoint

```shell
arcave savepoint create
```

Useful in a loop where the id is captured programmatically and the name would be noise.

### Capture the id for a script

```shell
ID=$(arcave savepoint create nightly | jq -r .id)
```

Results are JSON on stdout and logs are on stderr, so this needs no filtering.

### Give a slow stack more time to come back

```shell
arcave savepoint create before-migration --wait-timeout 5m
```

### Watch a slow capture unfold

```shell
arcave --log-level debug savepoint create before-migration
```

Compose's in-flight steps — `Waiting`, `Starting`, `Creating`, image-pull progress — are logged at debug level.

## Related docs

* [savepoint list](savepoint_list.md) — find what you captured.
* [rollback](rollback.md) — put it back.
* [How arcave works](../how_it_works.md) — the mechanism in full.
