---
title: Reset a seeded environment
description: Seed a Postgres, MinIO and Redis stack once, capture it as an arcave savepoint, and return the whole environment to that state in one command.
keywords: [arcave example, test fixtures, seed data, reset environment, docker compose, minio, redis, Open-Source, Greenmask]
---

# Reset a seeded environment

Seeding is slow. Do it once, snapshot, and reset from the snapshot for the rest of the project's life.

Runs against the [demo stack](index.md#the-stack).

## 1. Seed everything, once

```shell title="Schema and reference data"
docker compose exec -T postgres psql -U postgres -d demo <<'SQL'
CREATE TABLE tenants (id serial PRIMARY KEY, name text NOT NULL);
CREATE TABLE orders  (id serial PRIMARY KEY, tenant_id int REFERENCES tenants(id), total numeric);
INSERT INTO tenants (name) VALUES ('acme'), ('globex'), ('initech');
INSERT INTO orders (tenant_id, total) SELECT (n % 3) + 1, n * 1.5 FROM generate_series(1, 5000) n;
SQL
```

```shell title="Uploaded files"
docker compose exec -T minio sh -c '
  mkdir -p /data/demo/invoices
  for i in 1 2 3; do echo "invoice $i" > /data/demo/invoices/inv-$i.txt; done
'
```

```shell title="A warm cache"
docker compose exec -T redis redis-cli MSET \
  tenants:count 3 orders:count 5000 cache:warmed 1
```

## 2. Capture the baseline

```shell
arcave savepoint create clean --description "seeded baseline — do not delete"
```

```json title="stdout"
{
  "id": "1780331000",
  "name": "clean",
  "description": "seeded baseline — do not delete",
  "created_at": "2026-06-01T18:36:40.002114Z",
  "stats": [
    { "volume": { "object_id": "volume-named-arcave-demo_pgdata",    "type": "named", "backend": "copy" }, "type": "copy" },
    { "volume": { "object_id": "volume-named-arcave-demo_miniodata", "type": "named", "backend": "copy" }, "type": "copy" },
    { "volume": { "object_id": "volume-service-redis-data",         "type": "anon",  "backend": "copy" }, "type": "copy" }
  ],
  "spec": { "format": "compose-yaml" }
}
```

Three volumes, three services, one savepoint.

## 3. Let a test suite make a mess

```shell
docker compose exec -T postgres psql -U postgres -d demo -c 'TRUNCATE orders CASCADE;'
docker compose exec -T minio sh -c 'rm -rf /data/demo/invoices'
docker compose exec -T redis redis-cli FLUSHALL
```

## 4. Reset

```shell
arcave rollback clean
```

## 5. Verify all three came back

```shell
docker compose exec -T postgres psql -U postgres -d demo -tAc 'SELECT count(*) FROM orders;'
```

```text
5000
```

```shell
docker compose exec -T minio sh -c 'ls /data/demo/invoices'
```

```text
inv-1.txt
inv-2.txt
inv-3.txt
```

```shell
docker compose exec -T redis redis-cli MGET tenants:count orders:count cache:warmed
```

```text
1) "3"
2) "5000"
3) "1"
```

This is the point of whole-stack capture. A `pg_restore` would have given you the first of those three, and left the object store empty and the cache cold.

## 6. Wire it into the test suite

```shell title="run-tests.sh"
#!/usr/bin/env bash
set -euo pipefail

export ARCAVE_PROJECT_NAME=arcave-demo

arcave rollback clean          # reset BEFORE, not after
./integration-tests.sh
```

Resetting before rather than after has two advantages: a crashed suite cannot skip its own cleanup, and you can inspect the wreckage of a failed run before the next one wipes it.

## 7. Protect the baseline

```shell
arcave savepoint list
```

```json title="stdout"
[
  { "id": "1780339981", "name": "after-refactor", "description": "", "created_at": "2026-06-01T21:13:01.114872Z" },
  { "id": "1780331000", "name": "clean", "description": "seeded baseline — do not delete", "created_at": "2026-06-01T18:36:40.002114Z" }
]
```

:::warning
`arcave savepoint remove --retain-recent 5` will delete `clean` once five newer savepoints exist. Retention rules do not know which savepoint is precious.
:::

Prune by id instead:

```shell
arcave savepoint remove 1780339981
```

Or give the baseline a store of its own, so no retention sweep in the working store can reach it:

```shell
arcave init --override --savepoint-dir /var/lib/arcave-baselines
```

## Related docs

* [Resetting fixtures and demos](../use_cases/fixture_reset.md) — the pattern.
* [Prune savepoints with retention rules](retention.md) — the next example.
* [savepoint remove](../commands/savepoint_remove.md) — the retention rules in full.
