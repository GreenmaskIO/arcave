---
title: Reset a seeded environment
description: Seed a Postgres, MinIO and Redis project once, capture it as an arcave savepoint, and return the whole environment to that state in one command.
keywords: [arcave example, test fixtures, seed data, reset environment, docker compose, minio, redis, Open-Source, Greenmask]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Reset a seeded environment

Seeding is slow. Do it once, snapshot, and reset from the snapshot for the rest of the project's life.

Runs against the [demo project](index.md#the-project).

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

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ savepoint.create clean      10.6s
├── ✔ project.introspect       0.1s
├── ✔ compose.stop             0.2s
├── ✔ savepoint.capture clean  5.7s
└── ✔ compose.start            3.6s

Savepoint clean · 019e83ff · 18:36:40 · stored 317 MB
            count     size   stored
  volumes       3  49.2 MB  49.2 MB
  layers        3   260 MB  40.6 KB
  images        3   1.1 GB   268 MB

  roll back:  arcave -p arcave-demo rollback clean
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout (trimmed)"
{
  "id": "019e83ff-30c0-7000-8000-000000000000",
  "name": "clean",
  "description": "seeded baseline — do not delete",
  "created_at": "2026-06-01T18:36:40.002114Z",
  "stats": [
    { "volume": { "object_id": "volume-named-arcave-demo_miniodata",   "type": "named", "backend": "fullcopy" }, "type": "fullcopy" },
    { "volume": { "object_id": "volume-named-arcave-demo_pgdata",      "type": "named", "backend": "fullcopy" }, "type": "fullcopy" },
    { "volume": { "object_id": "volume-anon-redis-1-data",             "type": "anon",  "backend": "fullcopy" }, "type": "fullcopy" }
  ],
  "writable_layers": [
    { "service": "minio",    "replica": 1, "container_name": "arcave-demo-minio-1",    "upstream_image_ref": "minio/minio:latest" },
    { "service": "postgres", "replica": 1, "container_name": "arcave-demo-postgres-1", "upstream_image_ref": "postgres:16" },
    { "service": "redis",    "replica": 1, "container_name": "arcave-demo-redis-1",    "upstream_image_ref": "redis:7" }
  ],
  "images": [
    { "id": "sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e", "refs": ["minio/minio:latest"], "stored": true },
    { "id": "sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54", "refs": ["postgres:16"],        "stored": true },
    { "id": "sha256:b2b95679e3b46fb51864949ed25ea976fc3a6bcc00a40a1bc00d568cb2822e50", "refs": ["redis:7"],            "stored": true }
  ],
  "spec": { "format": "compose-yaml" }
}
```

</TabItem>
</Tabs>

Three volumes, three writable layers, three images, one savepoint. The JSON is trimmed to the fields that identify each entry; [Roll back a failed migration](migration_rollback.md#2-take-the-savepoint) shows one of each in full.

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

This is the point of whole-project capture. A `pg_restore` would have given you the first of those three, and left the object store empty and the cache cold.

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

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
NAME            ID                                    CREATED           VOLUMES  LAYERS  STORED   NOT COVERED
after-refactor  019e8488-3ac8-7000-8000-000000000000  2026-06-01 21:13  3        3       49.3 MB  -
clean           019e83ff-30c0-7000-8000-000000000000  2026-06-01 18:36  3        3       317 MB   -
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout (trimmed)"
[
  { "id": "019e8488-3ac8-7000-8000-000000000000", "name": "after-refactor", "description": "", "created_at": "2026-06-01T21:13:01.114872Z" },
  { "id": "019e83ff-30c0-7000-8000-000000000000", "name": "clean", "description": "seeded baseline — do not delete", "created_at": "2026-06-01T18:36:40.002114Z" }
]
```

</TabItem>
</Tabs>

:::warning
`arcave savepoint remove --retain-recent 5` will delete `clean` once five newer savepoints exist. Retention rules do not know which savepoint is precious.
:::

Remove by name instead, and let `arcave gc` free the space:

```shell
arcave savepoint remove after-refactor
arcave gc
```

Or keep the baseline in a [session](../sessions.md) of its own. Retention works within one session, so no sweep in the session you work in can reach it:

```shell
arcave session create --name fixtures
arcave --session fixtures savepoint create clean --description "seeded baseline — do not delete"
```

```shell title="run-tests.sh, resetting from the fixtures session"
arcave --session fixtures rollback clean
./integration-tests.sh
```

Take the savepoint right after seeding, as in step 2. The commands that name no session keep working in yours, and `arcave savepoint remove --retain-recent 5` there never sees `clean`.

## Related docs

* [Resetting fixtures and demos](../use_cases/fixture_reset.md) — the pattern.
* [Prune savepoints with retention rules](retention.md) — the next example.
* [savepoint remove](../reference/cli/savepoint_remove.md) — the retention rules in full.
