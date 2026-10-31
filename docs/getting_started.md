---
title: Getting started
description: Initialise arcave in a Docker Compose project, take your first savepoint, break something on purpose, and roll the environment back in one command.
keywords: [arcave quickstart, getting started, savepoint, rollback, docker compose, tutorial, Open-Source, Greenmask]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Getting started

Five minutes, one compose project, and one deliberate mistake to undo.

If you have not installed arcave yet, start at [Installation](installation.md).

<video src={require('./assets/demo/base-cycle.mp4').default} autoPlay loop muted playsInline controls width="100%" />

*What it shows:* the cycle this page walks through — a savepoint, a dropped table, `arcave rollback` — and the rows are back.

## 1. Bring a compose project up

arcave reads the **running project** from the Docker daemon — it never opens your compose file. So the project has to exist before arcave can do anything with it.

```shell title="A minimal stateful project"
docker compose up -d
docker compose ls        # note the project name in the first column
```

Any project with a persistent volume works. The examples below assume a project named `myapp` with a Postgres service.

:::warning
`docker compose stop` is fine — a stopped container still describes the project. `docker compose down` is not: it removes the containers, so `savepoint create` has nothing to capture and fails saying so. A rollback still works on a torn-down project.
:::

## 2. Initialise arcave

```shell
arcave init
```

This writes `.arcave/config.yaml` and nothing else. It opens no storage and contacts no daemon — it is the one command that runs before arcave has a project to speak of.

```yaml title=".arcave/config.yaml"
project:
    name: myapp
common:
    concurrency: 5
compose:
    wait_timeout: 2m0s
    stop_timeout: 10s
    progress: log
storage:
    type: directory
    directory:
        path: .arcave/storage
snapshot:
    backend: fullcopy
writable_layers:
    backend: dedupcopy
capture:
    writable_layers: true
    images: true
    volumes:
        named: true
        bind: true
        anonymous: true
rollback:
    leftover_volumes: overwrite
log:
    format: text
```

Every key but `log.level` is written out with its default, so the file doubles as a list of what you can change; [Configuration](reference/configuration.md) explains each one. One flag is worth knowing now:

```shell
arcave init --name myapp                        # the storage tenant name
```

Without `--name`, the storage tenant is named after the current directory.

Savepoint data goes to `.arcave/storage`. To keep it elsewhere — another directory, an S3 bucket, an Azure container, an SFTP server — edit `storage` in `.arcave/config.yaml`; see [Configuration](reference/configuration.md#storage).

You will want `.arcave/` in your `.gitignore`.

## 3. Name the project once

Every command that touches a compose project needs to know which one. Set it once for the shell instead of passing `-p` every time:

```shell
export ARCAVE_PROJECT_NAME=myapp
```

:::note
`-p/--project-name` names the **compose project**, as `docker compose ls` lists it. That is a different thing from `project.name` in the config, which names the *storage tenant*. See [Concepts](concepts.md#the-compose-project-versus-the-storage-tenant) — and note that the environment variable sets both.
:::

## 4. Take your first savepoint

```shell
arcave savepoint create before-migration
```

arcave stops the services, captures every volume, the image each service runs on and every container's writable layer, records the compose spec, commits the record, starts the services again, and waits for their health checks.

On a terminal you watch the steps as a live tree on stderr, and the result is printed as a summary. When stdout is a pipe or a file — or with `--format json` — the result is the savepoint record as JSON:

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ savepoint.create before-migration      10.9s
├── ✔ project.introspect                  0.1s
├── ✔ compose.stop                        0.3s
├── ✔ savepoint.capture before-migration  4.2s
└── ✔ compose.start                       6.1s

Savepoint before-migration · 019e843e · 19:41:12 · stored 206 MB
            count     size   stored
  volumes       1  40.8 MB  40.8 MB
  layers        1   159 MB  16.7 KB
  images        1   658 MB   165 MB

  roll back:  arcave -p myapp rollback before-migration
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "id": "019e843e-1650-7000-8000-000000000000",
  "name": "before-migration",
  "description": "",
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
            {
              "service": "db",
              "target": "/var/lib/postgresql/data"
            }
          ]
        }
      },
      "type": "fullcopy",
      "size_bytes": 40805376,
      "stored_bytes": 40805376
    }
  ],
  "writable_layers": [
    {
      "service": "db",
      "replica": 1,
      "container_id": "b6477db82dd008786a6819c72ff68799bbd4f669a696aef1ac2d8fb447dd24cb",
      "container_name": "myapp-db-1",
      "committed_image_id": "sha256:8769512644fabe547bc758133294a85b95543624a65f84924708eb2cb64b1d24",
      "imagebackend": "dedupcopy",
      "upstream_image_id": "sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
      "upstream_image_ref": "postgres:16",
      "size_bytes": 158913306,
      "stored_bytes": 16707
    }
  ],
  "images": [
    {
      "id": "sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
      "refs": [
        "postgres:16"
      ],
      "repo_digests": [
        "postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54"
      ],
      "size_bytes": 657586103,
      "stored_bytes": 164698711,
      "stored": true
    }
  ],
  "spec": {
    "format": "compose-yaml"
  }
}
```

Progress goes to **stderr**, so it never contaminates the JSON. Off a terminal it is one log line per step, as with `--progress log` here (timestamps and the `started` lines trimmed):

```text title="stderr"
INF savepoint.create[before-migration]/project.introspect elapsed=96ms status=succeeded
INF savepoint.create[before-migration]/compose.stop msg=Stopped resource=container:myapp-db-1 status=in_progress
INF savepoint.create[before-migration]/compose.stop elapsed=312ms status=succeeded
INF savepoint.create[before-migration]/savepoint.capture[before-migration]/volume.capture/volume[volume-named-myapp_pgdata] bytes=41MB/41MB elapsed=672ms status=succeeded stored=41MB
INF savepoint.create[before-migration]/savepoint.capture[before-migration]/image.capture/image[postgres:16] bytes=658MB/658MB elapsed=1.341s status=succeeded stored=165MB
INF savepoint.create[before-migration]/savepoint.capture[before-migration]/layer.capture/layer[myapp-db-1] bytes=159MB/159MB elapsed=2.224s reused=159MB status=succeeded stored=17KB
INF savepoint.create[before-migration]/savepoint.capture[before-migration] elapsed=4.237s status=succeeded
INF savepoint.create[before-migration]/compose.start msg=Healthy resource=container:myapp-db-1 status=in_progress
INF savepoint.create[before-migration]/compose.start elapsed=6.104s status=succeeded
INF savepoint.create[before-migration] elapsed=10.873s status=succeeded
```

</TabItem>
</Tabs>

`size` is what the savepoint holds and `stored` what it added to the store. The layer adds little because only what the container wrote over `postgres:16` is new; the image is stored once and reused by every later savepoint. Anything the savepoint left out, or that puts its data at risk, is listed under `Warnings`. In the JSON, `stats` holds one entry per captured volume, `writable_layers` one per container, and `images` one per distinct image; a savepoint that left something out also carries `not_covered`. [Output](reference/output.md) has every field.

`rollback` and `savepoint remove` take that `id` or the name.

## 5. Break something on purpose

```shell title="The mistake"
docker compose exec -T db psql -U postgres -c 'DROP TABLE orders;'
```

## 6. Roll it back

```shell
arcave rollback before-migration
```

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ rollback before-migration     9.7s
├── ✔ project.introspect        0.1s
├── ✔ rollback.prepare          1.6s
├── ✔ rollback.safety-snapshot  3.9s
├── ✔ rollback.apply            3.1s
└── ✔ rollback.cleanup          0.0s

Rolled back to before-migration · 019e843e · 2026-06-01 19:41:12
  volumes   1
  layers    1

Warnings (1)
  ! the containers run on the savepoint's writable layers
    → `docker compose up` of your compose file recreates them on its images and drops those layers
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout (rolled_back_to trimmed)"
{
  "rolled_back_to": {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "before-migration",
    "description": "",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    …
  },
  "restored": [
    {
      "object_id": "volume-named-myapp_pgdata",
      "compose_project": "myapp",
      "type": "named",
      "backend": "fullcopy",
      "named": {
        "volume_name": "myapp_pgdata",
        "used_by": [
          {
            "service": "db",
            "target": "/var/lib/postgresql/data"
          }
        ]
      }
    }
  ],
  "writable_layers": [
    {
      "service": "db",
      "replica": 1,
      "container_name": "myapp-db-1",
      "image_id": "sha256:8769512644fabe547bc758133294a85b95543624a65f84924708eb2cb64b1d24",
      "tag": "arcave/myapp-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000"
    }
  ]
}
```

</TabItem>
</Tabs>

In the JSON, `rolled_back_to` is the whole savepoint record, as `savepoint create` printed it. `restored` lists the volumes written back, and `writable_layers` the containers put back on their captured layer.

The table is back. So is everything else the project held at 19:41:12.

The `db` container now runs on an `arcave/…` tag rather than on `postgres:16` — the warning above: a `docker compose up` from your compose file recreates the container on `postgres:16` and drops the restored layer — roll back again to get it back.

Before overwriting anything, arcave took a **safety snapshot** of the state you are throwing away. If the restore had failed, or the services had come up unhealthy, arcave would have replayed that snapshot automatically and reported the error rather than leaving you between two states. See [How arcave works](how_it_works.md#the-safety-snapshot).

## 7. See what you have

```shell
arcave savepoint list
```

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
NAME              ID                                    CREATED           VOLUMES  LAYERS  STORED  NOT COVERED
before-migration  019e843e-1650-7000-8000-000000000000  2026-06-01 19:41  1        1       206 MB  -
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout (each record trimmed)"
[
  {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "before-migration",
    "description": "",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    …
  }
]
```

</TabItem>
</Tabs>

One row per savepoint — in the JSON a full savepoint record — newest first; `--sort asc` lists oldest first. The safety snapshot the rollback took is gone: a rollback that succeeds drops it. If it could not, the rollback's result names it under `safety_snapshot_kept` and says how to drop it.

## 8. Clean up

```shell
arcave savepoint remove before-migration      # by name, or by id
arcave savepoint remove --retain-recent 5     # keep the newest five
arcave savepoint remove --retain-for 7d --dry-run
arcave gc                                     # free what they shared
```

`savepoint remove` deletes each savepoint's record and its own directory; what it shared with other savepoints — stored images and layers — and its `cdc` snapshots are freed by [`arcave gc`](reference/cli/gc.md). `sp`, `ls` and `rm` are shorter spellings: `arcave sp ls`, `arcave sp rm before-migration` — see [Commands](reference/cli/index.md).

## 9. Branch off, if you need to

Savepoints live in a session, `baseline` until you choose another. `arcave session create --from baseline --name experiment` copies the whole session into a new one, and `arcave session set experiment` makes the commands that follow work there. See [Sessions and branching](sessions.md).

## What to read next

* **[Use cases](use_cases/index.md)** — the workflows this is actually for, starting with [agent iteration loops](use_cases/agent_loops.md).
* **[Examples](examples/index.md)** — complete walkthroughs on a multi-service project.
* **[Commands](reference/cli/index.md)** — every flag, every output shape.
* **[Configuration](reference/configuration.md)** — timeouts, storage, and the incremental `cdc` backend.
* **[Limitations](reference/limitations.md)** — read this before you build a workflow on arcave.
