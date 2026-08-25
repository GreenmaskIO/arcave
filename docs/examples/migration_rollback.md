---
title: Roll back a failed migration
description: A complete arcave walkthrough — snapshot a Postgres, MinIO and Redis stack, run a migration that goes wrong, and restore every service in one command.
keywords: [arcave example, postgres migration, rollback, docker compose, savepoint, Open-Source, Greenmask]
---

# Roll back a failed migration

The core loop, end to end, on the [demo stack](index.md#the-stack).

## 1. Put some state in every service

```shell title="Postgres"
docker compose exec -T postgres psql -U postgres -d demo <<'SQL'
CREATE TABLE orders (id serial PRIMARY KEY, tenant_id int, total numeric);
INSERT INTO orders (tenant_id, total) SELECT (n % 3) + 1, n * 1.5 FROM generate_series(1, 1000) n;
SQL
```

```shell title="Redis"
docker compose exec -T redis redis-cli SET orders:count 1000
```

```shell title="MinIO"
docker compose exec -T minio sh -c 'mkdir -p /data/demo && echo "report v1" > /data/demo/report.txt'
```

## 2. Take the savepoint

```shell
arcave savepoint create pre-migration --description "orders table, 1000 rows"
```

```text title="stderr"
INF stopping services
INF volume operation started object_id=volume-named-arcave-demo_pgdata
INF volume operation finished object_id=volume-named-arcave-demo_pgdata
INF volume operation started object_id=volume-service-redis-data
INF volume operation finished object_id=volume-service-redis-data
INF image capture started service=postgres replica=1
INF image capture finished service=postgres replica=1
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF compose kind=Container name=arcave-demo-postgres-1 status=Healthy
```

```json title="stdout"
{
  "id": "1780335122",
  "name": "pre-migration",
  "description": "orders table, 1000 rows",
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [
    {
      "volume": {
        "object_id": "volume-named-arcave-demo_pgdata",
        "compose_project": "arcave-demo",
        "type": "named",
        "backend": "copy",
        "named": {
          "volume_name": "arcave-demo_pgdata",
          "used_by": [
            { "service": "postgres", "target": "/var/lib/postgresql/data" }
          ]
        }
      },
      "type": "copy"
    }
  ],
  "spec": { "format": "compose-yaml" }
}
```

All three services were captured — including the anonymous volume behind Redis, which arcave had to ask the daemon about through the `redis` container.

## 3. Run a migration that goes wrong

```shell
docker compose exec -T postgres psql -U postgres -d demo <<'SQL'
ALTER TABLE orders ADD COLUMN region text;
UPDATE orders SET region = 'eu' WHERE tenant_id = 1;
ALTER TABLE orders ALTER COLUMN region SET NOT NULL;
SQL
```

```text title="It fails, halfway"
ERROR:  column "region" of relation "orders" contains null values
```

The `ALTER` failed, but the column and the partial backfill are still there. And the application already updated its cache:

```shell
docker compose exec -T redis redis-cli SET orders:schema_version 15
```

The environment is now inconsistent in two services at once. This is exactly the state that is tedious to unwind by hand.

## 4. Roll back

```shell
arcave rollback pre-migration
```

```text title="stderr"
INF image restore started service=postgres replica=1
INF image restore finished service=postgres replica=1
INF stopping services
INF backing up current state
INF removing compose containers and networks
INF rebuilding the stack from the savepoint's spec
INF creating compose containers
INF volume operation started object_id=volume-named-arcave-demo_pgdata
INF volume operation finished object_id=volume-named-arcave-demo_pgdata
INF volume operation started object_id=volume-service-redis-data
INF volume operation finished object_id=volume-service-redis-data
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF compose kind=Container name=arcave-demo-postgres-1 status=Healthy
```

```json title="stdout"
{
  "rolled_back_to": {
    "id": "1780335122",
    "name": "pre-migration",
    "description": "orders table, 1000 rows",
    "created_at": "2026-06-01T19:41:12.123456789Z"
  },
  "restored": [
    {
      "object_id": "volume-named-arcave-demo_pgdata",
      "compose_project": "arcave-demo",
      "type": "named",
      "backend": "copy",
      "named": {
        "volume_name": "arcave-demo_pgdata",
        "used_by": [
          { "service": "postgres", "target": "/var/lib/postgresql/data" }
        ]
      }
    },
    {
      "object_id": "volume-service-redis-data",
      "compose_project": "arcave-demo",
      "type": "anon",
      "backend": "copy",
      "anon": { "service": "redis", "target": "/data" }
    }
  ]
}
```

## 5. Verify

```shell
docker compose exec -T postgres psql -U postgres -d demo -c '\d orders'
```

```text
 Column   |  Type   
----------+---------
 id       | integer 
 tenant_id| integer 
 total    | numeric 
```

No `region` column.

```shell
docker compose exec -T redis redis-cli GET orders:schema_version
```

```text
(nil)
```

The cache went back too. That is the part a `pg_restore` would not have given you.

## 6. Fix the migration and try again

```shell
docker compose exec -T postgres psql -U postgres -d demo <<'SQL'
ALTER TABLE orders ADD COLUMN region text;
UPDATE orders SET region = 'eu';                    -- all rows, not just tenant 1
ALTER TABLE orders ALTER COLUMN region SET NOT NULL;
SQL
```

The savepoint is still there, so a third attempt is another `arcave rollback pre-migration` away.

## 7. Clean up

```shell
arcave savepoint remove pre-migration --dry-run
```

That is an **error** — `remove` takes ids, not names:

```text
Error: invalid savepoint id "pre-migration": must be a Unix timestamp (digits only)
```

```shell
arcave savepoint list | jq -r '.[] | select(.name == "pre-migration") | .id'
arcave savepoint remove 1780335122
```

```json title="stdout"
{
  "dry_run": false,
  "removed": [
    {
      "id": "1780335122",
      "name": "pre-migration",
      "description": "orders table, 1000 rows",
      "created_at": "2026-06-01T19:41:12.123456789Z"
    }
  ]
}
```

## Related docs

* [Testing destructive migrations](../use_cases/migration_testing.md) — the pattern behind this walkthrough.
* [rollback](../reference/cli/rollback.md) — the command reference.
* [Reset a seeded environment](fixture_reset.md) — the next example.
