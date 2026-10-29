---
title: Roll back a failed migration
description: A complete arcave walkthrough — snapshot a Postgres, MinIO and Redis project, run a migration that goes wrong, and restore every service in one command.
keywords: [arcave example, postgres migration, rollback, docker compose, savepoint, Open-Source, Greenmask]
---

# Roll back a failed migration

The core loop, end to end, on the [demo project](index.md#the-project).

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

```text title="stderr (trimmed)"
INF stopping services
INF stopping compose services stop_timeout=10s
INF volume operation started volume=volume-named-arcave-demo_pgdata backend=fullcopy op=snapshot
INF volume operation started volume=volume-named-arcave-demo_miniodata backend=fullcopy op=snapshot
INF volume operation started volume=volume-anon-redis-1-data backend=fullcopy op=snapshot
INF volume operation finished volume=volume-anon-redis-1-data backend=fullcopy op=snapshot
INF volume operation finished volume=volume-named-arcave-demo_miniodata backend=fullcopy op=snapshot
INF volume operation finished volume=volume-named-arcave-demo_pgdata backend=fullcopy op=snapshot
INF image capture started image=["postgres:16"] op=save
INF image capture finished image=["postgres:16"] op=save stored=true
INF writable layer capture started service=postgres replica=1 op=commit
INF writable layer capture finished service=postgres replica=1 op=commit
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF savepoint.create[pre-migration]/compose.start msg=Healthy resource=container:arcave-demo-postgres-1 status=in_progress
```

Trimmed to one image and one writable layer, and to the fields that tell the lines apart; every line also carries `snapshot_id`, and the image lines `image_id`. The three volumes are captured concurrently, so their lines interleave. Then each distinct image is kept once, and every container's writable layer is committed over it.

```json title="stdout (trimmed)"
{
  "id": "019e843e-1650-7000-8000-000000000000",
  "name": "pre-migration",
  "description": "orders table, 1000 rows",
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [
    {
      "volume": {
        "object_id": "volume-named-arcave-demo_pgdata",
        "compose_project": "arcave-demo",
        "type": "named",
        "backend": "fullcopy",
        "named": {
          "volume_name": "arcave-demo_pgdata",
          "used_by": [
            { "service": "postgres", "target": "/var/lib/postgresql/data" }
          ]
        }
      },
      "type": "fullcopy",
      "size_bytes": 48273408,
      "stored_bytes": 48273408
    },
    {
      "volume": {
        "object_id": "volume-anon-redis-1-data",
        "compose_project": "arcave-demo",
        "type": "anon",
        "backend": "fullcopy",
        "anon": { "service": "redis", "replica": 1, "target": "/data" }
      },
      "type": "fullcopy",
      "size_bytes": 10240,
      "stored_bytes": 10240
    }
  ],
  "writable_layers": [
    {
      "service": "postgres",
      "replica": 1,
      "container_id": "5f0c8d7e2a41…",
      "container_name": "arcave-demo-postgres-1",
      "committed_image_id": "sha256:3b1f…",
      "imagebackend": "dedupcopy",
      "upstream_image_id": "sha256:9c2e…",
      "upstream_image_ref": "postgres:16",
      "size_bytes": 452984832,
      "stored_bytes": 1048576
    }
  ],
  "images": [
    {
      "id": "sha256:9c2e…",
      "refs": ["postgres:16"],
      "repo_digests": ["postgres@sha256:4a7d…"],
      "size_bytes": 451936256,
      "stored_bytes": 451936256,
      "stored": true
    }
  ],
  "spec": { "format": "compose-yaml" }
}
```

The output is trimmed to one entry of each kind: the real record holds three `stats` — the MinIO volume as well — three `writable_layers` and three `images`, one per service. Every service was captured, including the anonymous volume behind Redis, which arcave had to ask the Docker daemon about through the `redis` container. A layer's `stored_bytes` is what it adds over its image, which is why it is so much smaller than its `size_bytes`.

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

```text title="stderr (trimmed)"
INF writable layer restored from the savepoint service=postgres origin=config backend=dedupcopy
INF mount restored from the savepoint service=postgres origin=config backend=fullcopy target=/var/lib/postgresql/data
INF writable layer restore started service=postgres replica=1 op=load
INF writable layer restore finished service=postgres replica=1 op=load tag=arcave/arcave-demo-postgres-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000
INF stopping services
INF stopping compose services stop_timeout=10s
INF backing up current state
INF volume operation started volume=volume-named-arcave-demo_pgdata backend=fullcopy op=snapshot
INF volume operation finished volume=volume-named-arcave-demo_pgdata backend=fullcopy op=snapshot
INF writable layer capture started service=postgres replica=1 op=commit
INF writable layer capture finished service=postgres replica=1 op=commit
INF removing the compose project
INF removing compose containers and networks stop_timeout=10s
INF creating the compose project from the spec
INF creating compose containers
INF volume operation started volume=volume-named-arcave-demo_pgdata backend=fullcopy op=restore
INF volume operation finished volume=volume-named-arcave-demo_pgdata backend=fullcopy op=restore
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF rollback[pre-migration]/rollback.apply/compose.start msg=Healthy resource=container:arcave-demo-postgres-1 status=in_progress
INF the containers run on the savepoint's writable layers: `docker compose up` from your compose file recreates them on its images and drops those layers; roll back again to restore them writable_layers=3
```

Trimmed to the `postgres` lines. arcave first reads what the savepoint says it holds, then loads the writable layers back into the daemon — all before anything is stopped, so a savepoint that cannot be rebuilt leaves the project running. Then arcave takes the safety snapshot of the live state, rebuilds the project from the savepoint's spec, and restores every volume into it.

```json title="stdout (trimmed)"
{
  "rolled_back_to": {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "pre-migration",
    "description": "orders table, 1000 rows",
    "created_at": "2026-06-01T19:41:12.123456789Z"
  },
  "restored": [
    {
      "object_id": "volume-named-arcave-demo_pgdata",
      "compose_project": "arcave-demo",
      "type": "named",
      "backend": "fullcopy",
      "named": {
        "volume_name": "arcave-demo_pgdata",
        "used_by": [
          { "service": "postgres", "target": "/var/lib/postgresql/data" }
        ]
      }
    },
    {
      "object_id": "volume-anon-redis-1-data",
      "compose_project": "arcave-demo",
      "type": "anon",
      "backend": "fullcopy",
      "anon": { "service": "redis", "replica": 1, "target": "/data" }
    }
  ],
  "writable_layers": [
    {
      "service": "postgres",
      "replica": 1,
      "container_name": "arcave-demo-postgres-1",
      "image_id": "sha256:3b1f…",
      "tag": "arcave/arcave-demo-postgres-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000"
    }
  ]
}
```

`rolled_back_to` is the whole savepoint record, trimmed here as in step 2; `restored` and `writable_layers` hold all three volumes and all three containers. After the rollback each container runs on the `arcave/…` tag of its restored layer rather than on the image its compose file names.

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

That is an **error** — `remove` takes ids, not names, and no savepoint has the id `pre-migration`:

```text
Error: savepoint(s) not found: pre-migration
```

```shell
arcave savepoint list | jq -r '.[] | select(.name == "pre-migration") | .id'
arcave savepoint remove 019e843e-1650-7000-8000-000000000000
```

```json title="stdout (trimmed)"
{
  "dry_run": false,
  "removed": [
    {
      "id": "019e843e-1650-7000-8000-000000000000",
      "name": "pre-migration",
      "description": "orders table, 1000 rows",
      "created_at": "2026-06-01T19:41:12.123456789Z"
    }
  ]
}
```

Each entry in `removed` is the whole savepoint record, trimmed here to its first fields.

## Related docs

* [Testing destructive migrations](../use_cases/migration_testing.md) — the pattern behind this walkthrough.
* [rollback](../reference/cli/rollback.md) — the command reference.
* [Reset a seeded environment](fixture_reset.md) — the next example.
