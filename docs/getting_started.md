---
title: Getting started
description: Initialise arcave in a Docker Compose project, take your first savepoint, break something on purpose, and roll the environment back in one command.
keywords: [arcave quickstart, getting started, savepoint, rollback, docker compose, tutorial, Open-Source, Greenmask]
---

# Getting started

Five minutes, one compose project, and one deliberate mistake to undo.

If you have not installed arcave yet, start at [Installation](installation.md).

## 1. Bring a stack up

arcave reads the **running stack** from the Docker daemon — it never opens your compose file. So the stack has to exist before arcave can do anything with it.

```shell title="A minimal stateful stack"
docker compose up -d
docker compose ls        # note the project name in the first column
```

Any project with a persistent volume works. The examples below assume a project named `myapp` with a Postgres service.

:::warning
`docker compose stop` is fine — a stopped container still describes the stack. `docker compose down` is not: it removes the containers, and with them everything arcave knows about your project. A command run against a torn-down stack fails saying so.
:::

## 2. Initialise arcave

```shell
arcave init
```

This writes `.arcave/config.yaml` and nothing else. It opens no storage and contacts no daemon — it is the one command that runs before arcave has a project to speak of.

```yaml title=".arcave/config.yaml"
project:
    name: myapp
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
log:
    level: info
    format: text
```

Two flags are worth knowing now:

```shell
arcave init --name myapp                        # the storage tenant name
arcave init --savepoint-dir /backups/myapp      # where savepoint data goes
```

You will want `.arcave/` in your `.gitignore`.

## 3. Name the stack once

Every command that touches a stack needs to know which one. Set it once for the shell instead of passing `-p` every time:

```shell
export ARCAVE_PROJECT_NAME=myapp
```

:::note
`-p/--project-name` names the **compose project**, as `docker compose ls` lists it. That is a different thing from `project.name` in the config, which names the *storage tenant*. See [Concepts](concepts.md#project-versus-stack) — and note that the environment variable sets both.
:::

## 4. Take your first savepoint

```shell
arcave savepoint create before-migration
```

arcave stops the services, captures every volume and every container's writable layer, records the compose spec, commits the record, starts the services again, and waits for their health checks.

```json title="stdout"
{
  "id": "1780335122",
  "name": "before-migration",
  "description": "",
  "created_at": "2026-06-01T19:41:12.123456789Z"
}
```

Progress goes to **stderr**, so it never contaminates the JSON:

```text title="stderr"
INF stopping services
INF volume operation started object_id=volume-named-myapp_pgdata
INF volume operation finished object_id=volume-named-myapp_pgdata
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF compose kind=Container name=myapp-db-1 status=Healthy
```

That `id` is what `rollback` and `remove` take.

## 5. Break something on purpose

```shell title="The mistake"
docker compose exec -T db psql -U postgres -c 'DROP TABLE orders;'
```

## 6. Roll it back

```shell
arcave rollback before-migration
```

```json title="stdout"
{
  "rolled_back_to": {
    "id": "1780335122",
    "name": "before-migration",
    "description": "",
    "created_at": "2026-06-01T19:41:12.123456789Z"
  },
  "restored": [
    { "service": "db", "mount_path": "/var/lib/postgresql/data" }
  ],
  "images": [
    {
      "service": "db",
      "replica": 1,
      "image_id": "sha256:aaaa…",
      "image_ref": "arcave/myapp-db-1-postgres-16:sp-1780335122"
    }
  ]
}
```

The table is back. So is everything else the stack held at 19:41:12.

Before overwriting anything, arcave took a **safety snapshot** of the state you are throwing away. If the restore had failed, or the services had come up unhealthy, arcave would have replayed that snapshot automatically and reported the error rather than leaving you between two states. See [How arcave works](how_it_works.md#the-safety-snapshot).

## 7. See what you have

```shell
arcave savepoint list
```

```json title="stdout"
[
  {
    "id": "1780335122",
    "name": "before-migration",
    "description": "",
    "created_at": "2026-06-01T19:41:12.123456789Z"
  }
]
```

## 8. Clean up

```shell
arcave savepoint remove 1780335122            # by id
arcave savepoint remove --retain-recent 5     # keep the newest five
arcave savepoint remove --retain-for 7d --dry-run
```

## What to read next

* **[Use cases](use_cases/index.md)** — the workflows this is actually for, starting with [agent iteration loops](use_cases/agent_loops.md).
* **[Examples](examples/index.md)** — complete walkthroughs on a multi-service stack.
* **[Commands](commands/index.md)** — every flag, every output shape.
* **[Configuration](configuration.md)** — timeouts, storage, and the incremental `cdc` backend.
* **[Limitations](limitations.md)** — read this before you build a workflow on arcave.
