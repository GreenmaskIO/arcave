---
title: arcave savepoint create
description: Capture an application-consistent savepoint of a Docker Compose project — every stateful volume, every container's writable layer, and the compose spec of the project itself.
keywords: [arcave savepoint create, volume capture, writable layer, compose spec, docker compose]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# arcave savepoint create

Captures a savepoint: every stateful volume, every container's writable layer, the images the services run on, and the compose spec of the project.

```text title="Synopsis"
arcave savepoint create [name] [flags]
```

The services are stopped for the capture and started again when it completes — that is what makes the result application-consistent. The sequence is [the savepoint flow](../../how_it_works.md#the-savepoint-flow); what ends up inside is [what a savepoint covers](../../how_it_works.md#what-a-savepoint-covers).

Everything is captured by default. The `capture` section of the [config](../configuration.md) turns a kind off for the whole project, and `arcave.*` labels in the compose file leave out or include a service, a volume, a mount, a layer or an image — see [Capture labels](../capture_labels.md). What a savepoint does not hold is listed in its `not_covered`. When a unit is captured differently than in the latest savepoint, `savepoint create` warns of it before it stops anything.

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

A name is unique within the compose project and session: `savepoint create` refuses one another savepoint already carries (safety snapshots aside), one containing `/`, and one `rollback` would read as something else — `latest`, or a savepoint id. All are refused before anything is stopped.

## Output

On a terminal, the final tree of the steps on stderr and a summary of what was captured on stdout; in a script, or with `--format json`, the savepoint record as JSON on stdout. The summary's `size` is what the savepoint holds and `stored` what it added to the store; its `Warnings` spell out `not_covered` and any image not stored.

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ savepoint.create before-migration       7.1s
├── ✔ project.introspect                  0.1s
├── ✔ compose.stop                        0.2s
├── ✔ savepoint.capture before-migration  3.6s
└── ✔ compose.start                       2.2s

Savepoint before-migration · 019e843e · 19:41:12 · stored 210 MB
            count     size   stored
  volumes       1  40.9 MB  40.9 MB
  layers        2   163 MB  20.6 KB
  images        2   671 MB   169 MB

Warnings (1)
  ! app has no volumes: only its writable layer is held

  roll back:  arcave -p myapp rollback before-migration
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "id": "019e843e-1650-7000-8000-000000000000",
  "name": "before-migration",
  "description": "before the v2 schema",
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [
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
      "size_bytes": 40863232,
      "stored_bytes": 40863232
    }
  ],
  "writable_layers": [
    {
      "service": "app",
      "replica": 1,
      "container_id": "1886b4bf7a8a88a70845594686adddfd78aad8ce96a2ecab4e4468a4c98db8ca",
      "container_name": "myapp-app-1",
      "committed_image_id": "sha256:f68cd146c87c7e1e7606d02065a872eed3eafafa97ca55b8836c2785445b72da",
      "imagebackend": "dedupcopy",
      "upstream_image_id": "sha256:39c1995c80982badee967125886cce5097338509b7f56d05105e8ccbd3375ec5",
      "upstream_image_ref": "myapp:latest",
      "size_bytes": 4096126,
      "stored_bytes": 3807
    },
    {
      "service": "db",
      "replica": 1,
      "container_id": "22c7d216e10d814dfaba822285cd82e0d59d118ba6c1a941e170a5c4a7f3fc1f",
      "container_name": "myapp-db-1",
      "committed_image_id": "sha256:6b3f960b737dff8f5ac90b9538610413cafe14be340f3635dc4cbb279ddc3035",
      "imagebackend": "dedupcopy",
      "upstream_image_id": "sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
      "upstream_image_ref": "postgres:16",
      "size_bytes": 158913382,
      "stored_bytes": 16783
    }
  ],
  "images": [
    {
      "id": "sha256:39c1995c80982badee967125886cce5097338509b7f56d05105e8ccbd3375ec5",
      "refs": ["myapp:latest"],
      "repo_digests": [
        "myapp@sha256:39c1995c80982badee967125886cce5097338509b7f56d05105e8ccbd3375ec5"
      ],
      "size_bytes": 13580222,
      "stored_bytes": 4097189,
      "stored": true
    },
    {
      "id": "sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
      "refs": ["postgres:16"],
      "repo_digests": [
        "postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54"
      ],
      "size_bytes": 657586103,
      "stored_bytes": 164698711,
      "stored": true
    }
  ],
  "spec": { "format": "compose-yaml" },
  "not_covered": {
    "services_without_volumes": ["app"]
  }
}
```

</TabItem>
</Tabs>

| Field | Meaning |
|---|---|
| `id` | A UUIDv7 in canonical form, assigned at capture. What `rollback` and `savepoint remove` take. |
| `name`, `description` | What you passed, or empty strings. |
| `created_at` | RFC3339Nano. |
| `stats` | One entry per captured volume, keyed by `volume.object_id` — see [Storage layout](../storage_layout.md#object-ids). `volume.type` is `named`, `bind` or `anon`; `volume.backend` and `type` are `fullcopy` or `cdc`. `size_bytes` is the volume's size, `stored_bytes` what this capture added to the store; `payload` is the backend's own bookkeeping. |
| `writable_layers` | One entry per container whose writable layer was committed. `imagebackend` is `dedupcopy` or `fullcopy`; `size_bytes` is the committed image's size, `stored_bytes` what it added to the store after deduplication. |
| `images` | One entry per distinct image the services run on, stored once however many savepoints and replicas name it. `stored_bytes` is what this savepoint added, omitted when the image was already stored; `stored: false` means only its identity could be recorded. |
| `spec` | The compose spec committed beside the record. `format` is always `compose-yaml`. |
| `not_covered` | What the savepoint does not hold: skipped mounts with a reason code, services with no captured volume, containers killed at the stop deadline, replica gaps, unresolvable images, layers and images a label left out. Omitted when it holds everything. **Read it before relying on the savepoint.** |
| `labels` | Present only on a [safety snapshot](savepoint_list.md#safety-snapshots-are-hidden). |

Each field is omitted when empty, except `id`, `name`, `description` and `created_at`.

Full shapes for every command are in [Output](../output.md).

## Errors

| Condition | What happens |
|---|---|
| The project is not in the Docker daemon | `compose project "myapp": no containers found: bring the compose project up once, or name another project with -p` |
| A daemon older than Docker 25 | `unsupported docker daemon: the daemon reports version 24.0.7; arcave needs Docker 25 or newer` — before anything is stopped. See [Requirements](../requirements.md). |
| An `arcave.*` label arcave cannot read | `invalid capture policy label: arcave.capture="maybe" on service app: want true or false` — before anything is stopped. See [Capture labels](../capture_labels.md#what-a-bad-label-does). |
| A declared named volume the daemon does not hold | `docker volume "myapp_pgdata" does not exist (bring the project up once to create it)`. An error, never a skip: an empty capture would silently replace live data at the next rollback. |
| An anonymous volume whose container is gone | `no container for service "app" replica 1 in project "myapp" mounts "/data" (is the project created?)` |
| The name is taken in this compose project | `savepoint name is already taken: "before-migration" names savepoint 019e843e-1650-7000-8000-000000000000` |
| The name carries `/`, is `latest` or is a savepoint id | `invalid savepoint name "feat/v1": must not contain "/"`, `…: is reserved`, or `…: must not be a savepoint id` |
| The project has no volume to capture, or labels left every one out | `no stateful volumes found; nothing to capture` |
| Two volumes render one object id | `two volumes address the same object id: service "app" at "/var/lib" and service "app-var" at "/lib" both address "volume-service-app-var-lib"; rename one of them` |
| The savepoint directory already holds objects | `capture directory is not empty: 019e843e-1650-7000-8000-000000000000 holds 3 object(s), an interrupted run's remains; check what is there before removing it` |
| A daemon whose version could not be read exports an old archive shape | `capturing volumes: … unsupported image archive format: member "…" is neither a blob nor metadata; a daemon older than Docker 25 exports this shape`. See [Requirements](../requirements.md). |
| An interrupted rollback is marked | `rollback to savepoint 019e843e-1650-7000-8000-000000000000 was interrupted (restoring)` — see [Troubleshooting](../troubleshooting.md#an-interrupted-rollback). |
| Another arcave run holds the lock | `another arcave command is already running in this project` |
| The capture can't write its `heartbeat.json` for 25 minutes | `heartbeat lost` — the capture cancels itself and commits nothing; its directory is left for [`arcave gc`](gc.md). Check that the store is reachable and writable. |
| The services do not become healthy in `--wait-timeout` | `savepoint 019e843e-1650-7000-8000-000000000000 committed; services failed to start: …` — **the savepoint exists and is complete.** Only the restart failed: note the id, and look at the project. |

An error raised during the capture itself — after the services are stopped — is prefixed `capturing volumes: `, and the services are started again.

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
