# arcave demo

A self-contained demo of `arcave` savepoint / rollback across **all three persistent
Docker volume types** arcave handles. A small Go web app reads and writes three
stores; you add data, take a savepoint, change the data, then roll back and watch
every store return to its savepoint state.

| Store      | Mount in `docker-compose.yaml`     | arcave volume type     |
|------------|------------------------------------|--------------------|
| PostgreSQL | `pgdata:/var/lib/postgresql/data`  | **named volume**   |
| MinIO      | `./minio-data:/data`               | **host bind mount**|
| Redis      | `- /data`                          | **anonymous volume**|
| app (Go)   | *(none)*                           | — stateless UI     |

## Prerequisites

- Docker with the Compose plugin (`docker compose`)
- The `arcave` binary — build it from the repo root: `make build` → `bin/arcave`

## Walkthrough

Run everything from this directory (`examples/demo/`) so arcave finds
`docker-compose.yaml` and the project name resolves to `arcave-demo`.

```bash
cd examples/demo

# 1. Start the stack (builds the app image, pulls postgres/minio/redis).
#    --wait blocks until every service is healthy.
docker compose up -d --wait

# 2. Open the UI and add a few notes to each store.
open http://localhost:8080            # macOS; otherwise just browse there
#    Each store starts pre-seeded with distinct, labelled data.

# 3. Point arcave at this project (writes .arcave/config.yaml).
../../bin/arcave init

# 4. Snapshot all three volumes.
../../bin/arcave savepoint create before-changes
../../bin/arcave savepoint list           # confirm it's recorded

# 5. Now change things: add or remove notes in the UI so the stores differ
#    from the savepoint.

# 6. Roll back — arcave stops the services, wipes each mount, restores the
#    savepoint, and restarts. Output lists every restored service/mount.
../../bin/arcave rollback before-changes
```

Refresh <http://localhost:8080>: every change you made after the savepoint is
gone, and all three panels show exactly the data captured at savepoint time —
proving arcave restored the named volume, the bind mount, and the anonymous volume
together.

## How it works

- `arcave savepoint create` runs `docker compose stop`, copies each mount's data into
  `.arcave/savepoints/arcave-demo/<id>/<service>/<mount>/data.tar`, then `up -d --wait`.
- `arcave rollback` first backs up the current live state to `.arcave/tmp/`, wipes each
  mount, extracts the savepoint back, and restarts. If a service comes up
  unhealthy it auto-reverts to that backup, so a failed rollback leaves the
  environment where it started.

## Cleanup

```bash
docker compose down -v                # stop and remove containers + named volume
rm -rf .arcave minio-data                 # remove arcave metadata and the bind-mount data
```

## Notes

- Only the app (`8080`) publishes a host port, to avoid clashing with other
  local stacks. Postgres/Redis/MinIO are reached over the compose network. To
  use the MinIO web console, uncomment the `ports` block for `minio` in
  `docker-compose.yaml`.
- Rollback is **destructive** to the `./minio-data` host folder by design — it is
  inside this demo directory and gitignored, so it's safe to wipe.
