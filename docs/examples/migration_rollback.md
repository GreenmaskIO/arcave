---
title: Roll back a failed migration
description: A complete arcave walkthrough — snapshot a Postgres, MinIO and Redis project, run a migration that goes wrong, and restore every service in one command.
keywords: [arcave example, postgres migration, rollback, docker compose, savepoint, Open-Source, Greenmask]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

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

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ savepoint.create pre-migration      10.2s
├── ✔ project.introspect               0.1s
├── ✔ compose.stop                     0.2s
├── ✔ savepoint.capture pre-migration  5.2s
└── ✔ compose.start                    3.6s

Savepoint pre-migration · 019e843e · 19:41:12 · stored 317 MB
            count     size   stored
  volumes       3  48.9 MB  48.9 MB
  layers        3   260 MB  40.6 KB
  images        3   1.1 GB   268 MB

  roll back:  arcave -p arcave-demo rollback pre-migration
```

</TabItem>
<TabItem value="json" label="JSON">

```text title="stderr (trimmed)"
INF savepoint.create[pre-migration] status=started
INF savepoint.create[pre-migration]/compose.stop elapsed=205ms status=succeeded
INF savepoint.create[pre-migration]/savepoint.capture[pre-migration]/volume.capture items=0/3 status=started
INF savepoint.create[pre-migration]/savepoint.capture[pre-migration]/volume.capture/volume[volume-named-arcave-demo_miniodata] bytes=24KB/24KB elapsed=369ms status=succeeded stored=24KB
INF savepoint.create[pre-migration]/savepoint.capture[pre-migration]/volume.capture/volume[volume-anon-redis-1-data] bytes=6.1KB/6.1KB elapsed=402ms status=succeeded stored=6.1KB
INF savepoint.create[pre-migration]/savepoint.capture[pre-migration]/volume.capture/volume[volume-named-arcave-demo_pgdata] bytes=49MB/49MB elapsed=909ms status=succeeded stored=49MB
INF savepoint.create[pre-migration]/savepoint.capture[pre-migration]/image.capture/image[postgres:16] bytes=658MB/658MB elapsed=1.883s reused=493MB status=succeeded stored=165MB
INF savepoint.create[pre-migration]/savepoint.capture[pre-migration]/layer.capture/layer[arcave-demo-postgres-1] bytes=159MB/159MB elapsed=2.446s reused=159MB status=succeeded stored=17KB
INF savepoint.create[pre-migration]/compose.start msg=Healthy resource=container:arcave-demo-postgres-1 status=in_progress
INF savepoint.create[pre-migration] elapsed=10.194s status=succeeded
```

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
      "size_bytes": 48843264,
      "stored_bytes": 48843264
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
      "size_bytes": 6144,
      "stored_bytes": 6144
    },
    …
  ],
  "writable_layers": [
    {
      "service": "postgres",
      "replica": 1,
      "container_id": "c433fe7e348f8a2a8d492417f911dd5264a6825e587e52f2af0ba177bbbc51e0",
      "container_name": "arcave-demo-postgres-1",
      "committed_image_id": "sha256:b8ac8e2ebd778aadb0316246331435eaa1bc01bdbe201d6216c57fb420fc23e0",
      "imagebackend": "dedupcopy",
      "upstream_image_id": "sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
      "upstream_image_ref": "postgres:16",
      "size_bytes": 158913433,
      "stored_bytes": 16834
    },
    …
  ],
  "images": [
    {
      "id": "sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
      "refs": ["postgres:16"],
      "repo_digests": [
        "postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54"
      ],
      "size_bytes": 657586103,
      "stored_bytes": 164698711,
      "stored": true
    },
    …
  ],
  "spec": { "format": "compose-yaml" }
}
```

</TabItem>
</Tabs>

The summary counts what the savepoint holds: three volumes, three writable layers and three images, one per service. The JSON is trimmed to the PostgreSQL and Redis volumes and one entry of each other kind: the real record holds the MinIO volume as well, and a layer and an image for every service. Every service was captured, including the anonymous volume behind Redis, which arcave had to ask the Docker daemon about through the `redis` container. A layer's `stored_bytes` is what it adds over its image, which is why it is so much smaller than its `size_bytes`.

The log in the JSON tab is what `--progress log` writes off a terminal, trimmed to one image and one writable layer, the timestamps left out. Each line names its step by its path in the tree, and a step that measures bytes ends with what it read, stored and found already stored. The three volumes are captured concurrently, so their lines interleave. Then each distinct image is kept once, and every container's writable layer is committed over it.

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

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ rollback pre-migration       12.1s
├── ✔ project.introspect        0.1s
├── ✔ rollback.prepare          1.8s
├── ✔ rollback.safety-snapshot  3.9s
├── ✔ rollback.apply            5.3s
└── ✔ rollback.cleanup          0.0s

Rolled back to pre-migration · 019e843e · 2026-06-01 19:41:12
  volumes   3
  layers    3

Warnings (1)
  ! the containers run on the savepoint's writable layers
    → `docker compose up` of your compose file recreates them on its images and drops those layers
```

</TabItem>
<TabItem value="json" label="JSON">

```text title="stderr (trimmed)"
INF writable layer restored from the savepoint backend=dedupcopy origin=config service=postgres
INF mount restored from the savepoint backend=fullcopy origin=config service=postgres target=/var/lib/postgresql/data
INF rollback[pre-migration]/rollback.prepare/layer.restore elapsed=1.754s items=3/3 status=succeeded
INF rollback[pre-migration]/rollback.prepare elapsed=1.771s status=succeeded
INF rollback[pre-migration]/rollback.safety-snapshot/compose.stop elapsed=177ms status=succeeded
INF rollback[pre-migration]/rollback.safety-snapshot/savepoint.capture[safety-snapshot-019e843e-1650-7000-8000-000000000000] elapsed=3.769s status=succeeded
INF rollback[pre-migration]/rollback.apply/compose.down elapsed=133ms status=succeeded
INF rollback[pre-migration]/rollback.apply/compose.create elapsed=142ms status=succeeded
INF rollback[pre-migration]/rollback.apply/volume.restore/volume[volume-named-arcave-demo_pgdata] bytes=49MB/49MB elapsed=1.396s status=succeeded
INF rollback[pre-migration]/rollback.apply/compose.start msg=Healthy resource=container:arcave-demo-postgres-1 status=in_progress
INF rollback[pre-migration]/rollback.apply elapsed=5.308s status=succeeded
INF the containers run on the savepoint's writable layers: `docker compose up` from your compose file recreates them on its images and drops those layers; roll back again to restore them writable_layers=3
INF rollback[pre-migration] elapsed=12.126s status=succeeded
```

```json title="stdout (trimmed)"
{
  "rolled_back_to": {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "pre-migration",
    "description": "orders table, 1000 rows",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    …
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
    },
    …
  ],
  "writable_layers": [
    {
      "service": "postgres",
      "replica": 1,
      "container_name": "arcave-demo-postgres-1",
      "image_id": "sha256:b8ac8e2ebd778aadb0316246331435eaa1bc01bdbe201d6216c57fb420fc23e0",
      "tag": "arcave/arcave-demo-postgres-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000"
    },
    …
  ]
}
```

</TabItem>
</Tabs>

arcave first reads what the savepoint says it holds, then loads the writable layers back into the daemon — `rollback.prepare`, all before anything is stopped, so a savepoint that cannot be rebuilt leaves the project running. Then arcave takes the safety snapshot of the live state, rebuilds the project from the savepoint's spec, and restores every volume into it. The log in the JSON tab is trimmed to the `postgres` lines.

`rolled_back_to` is the whole savepoint record, trimmed here as in step 2; `restored` and `writable_layers` are trimmed as `stats` was: the real result holds all three volumes and all three containers, as the summary counts them. After the rollback each container runs on the `arcave/…` tag of its restored layer rather than on the image its compose file names.

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

`remove` takes a name as well as an id. Preview first, then remove:

```shell
arcave savepoint remove pre-migration --dry-run
arcave savepoint remove pre-migration --format json
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

Removing a savepoint deletes its record and its own directory; what it shared with other savepoints, and its `cdc` snapshots, go with [`arcave gc`](../reference/cli/gc.md).

## Related docs

* [Testing destructive migrations](../use_cases/migration_testing.md) — the pattern behind this walkthrough.
* [rollback](../reference/cli/rollback.md) — the command reference.
* [Reset a seeded environment](fixture_reset.md) — the next example.
