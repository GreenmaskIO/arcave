---
title: Examples
description: Worked arcave examples on a multi-service Docker Compose project — rolling back a failed migration, resetting a seeded environment, pruning savepoints and driving arcave from an AI agent.
keywords: [arcave examples, docker compose, postgres rollback, minio, redis, savepoint, tutorial, Open-Source, Greenmask]
---

# Examples

Complete walkthroughs, each starting from the same project. Where [Use cases](../use_cases/index.md) explain *when* to reach for a savepoint, these show every command and its output.

## The project

Every example below runs against one compose project with two kinds of persistent mount across three services, so a single savepoint exercises all of them. A runnable copy, with a small web app on top that shows each store's data, is in [`examples/demo`](https://github.com/GreenmaskIO/arcave/tree/main/examples/demo).

```yaml title="docker-compose.yaml"
name: arcave-demo

services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: demo
      POSTGRES_DB: demo
    volumes:
      - pgdata:/var/lib/postgresql/data        # named volume
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 3s
      timeout: 3s
      retries: 10

  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    volumes:
      - miniodata:/data                        # named volume
    healthcheck:
      test: ["CMD-SHELL", "timeout 5s bash -c ':> /dev/tcp/127.0.0.1/9000' || exit 1"]
      interval: 3s
      timeout: 6s
      retries: 10

  redis:
    image: redis:7
    command: ["redis-server", "--appendonly", "yes", "--dir", "/data"]
    volumes:
      - /data                                  # anonymous volume
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 3s
      timeout: 3s
      retries: 10

volumes:
  pgdata:
  miniodata:
```

Three volumes, two volume types:

| Service | Mount | Volume type |
|---|---|---|
| `postgres` | `pgdata:/var/lib/postgresql/data` | named |
| `minio` | `miniodata:/data` | named |
| `redis` | `/data` | anonymous |

Every service defines a **health check**. arcave waits for them after each restart, so without one a service is considered up the moment its container starts — and a savepoint may be taken of a database that has not finished recovering.

## Setup, once

```shell
docker compose up -d --wait
arcave init
export ARCAVE_PROJECT_NAME=arcave-demo
```

`ARCAVE_PROJECT_NAME` names the compose project, as `name:` declares it in the compose file, so the commands below need no `-p`. The variable also sets `project.name`, the storage tenant, so savepoints are stored under `arcave-demo` too — see [Concepts](../concepts.md#the-compose-project-versus-the-storage-tenant).

## The examples

* **[Roll back a failed migration](migration_rollback.md)** — the core loop, with real output at every step.
* **[Reset a seeded environment](fixture_reset.md)** — capture a baseline once and return to it forever, across all three services.
* **[Prune savepoints with retention rules](retention.md)** — keep the loop from filling the disk.
* **[Drive arcave from an AI agent](agent_mcp.md)** — wire the MCP server into an agent host and let it checkpoint itself.

## Related docs

* [Getting started](../getting_started.md) — the shortest possible version of the first example.
* [Commands](../reference/cli/index.md) — every flag used below.
