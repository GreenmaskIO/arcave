# arcave demo

A self-contained demo of `arcave` savepoint / rollback. A small Go web app reads
and writes three stores; you add data, take a savepoint, change the data, then
roll back and watch every store return to its savepoint state. The project is
configured for the **cdc** snapshot backend, so every capture goes into a restic
repository and only the changed chunks are written.

| Store      | Mount in `docker-compose.yaml`     | arcave volume type     |
|------------|------------------------------------|--------------------|
| PostgreSQL | `pgdata:/var/lib/postgresql/data`  | **named volume**   |
| MinIO      | `miniodata:/data`                  | **named volume**   |
| Redis      | `- /data`                          | **anonymous volume**|
| app (Go)   | *(none)*                           | — stateless UI     |

MinIO mounted `./minio-data` as a host bind until the demo moved to cdc. restic
sets `O_NOATIME` on every source file through `fcntl`, and Docker Desktop's
VirtioFS answers the next read with `EIO` — so a bind mount on macOS cannot be
captured, while volumes, which live in the VM's own filesystem, are unaffected.
Switching Docker Desktop's file sharing implementation to gRPC FUSE is the other
way out if you need a bind mount here.

## Prerequisites

- Docker with the Compose plugin (`docker compose`)
- The `arcave` binary — build it from the repo root: `make build` → `bin/arcave`

## Walkthrough

Run everything from this directory (`examples/demo/`) so arcave finds
`docker-compose.yaml` and the project name resolves to `arcave-demo`.

```bash
cd examples/demo

# 1. Start the compose project (builds the app image, pulls postgres/minio/redis).
#    --wait blocks until every service is healthy.
docker compose up -d --wait

# 2. Open the UI and add a few notes to each store.
open http://localhost:8080            # macOS; otherwise just browse there
#    Each store starts pre-seeded with distinct, labelled data.

# 3. Point arcave at this project (writes .arcave/config.yaml).
../../bin/arcave init

#    Select the cdc backend, then let arcave fill the backends.cdc defaults
#    (.arcave/ is gitignored, so this is a step you run once per clone).
sed -i '' 's/backend: fullcopy/backend: cdc/' .arcave/config.yaml
../../bin/arcave init --override

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

- `arcave savepoint create` runs `docker compose stop`, then backs each mount up
  with restic into a per-volume repository under
  `.arcave/storage/demo/arcave/shared/restic/<object-id>/`, records the savepoint
  under `.arcave/storage/demo/arcave/global/<id>/savepoint.json`, and `up -d --wait`.
  The repositories outlive the savepoints, so every capture after the first
  writes only the chunks that changed. Open one with a plain
  `RESTIC_PASSWORD=arcave restic -r .arcave/storage/demo/arcave/shared/restic/volume/arcave-demo_pgdata snapshots`
  — the password is a constant, since whoever can read the store can read
  `.arcave` anyway.
- `arcave rollback` first backs up the current live state to `.arcave/tmp/`, wipes each
  mount, restores the savepoint back, and restarts. If a service comes up
  unhealthy it auto-reverts to that backup, so a failed rollback leaves the
  environment where it started.

## Cleanup

```bash
docker compose down -v                # stop and remove containers + named volumes
rm -rf .arcave                        # remove arcave metadata, savepoints and restic repos
```

## Notes

- Only the app (`8080`) publishes a host port, to avoid clashing with other
  local compose projects. Postgres/Redis/MinIO are reached over the compose network. To
  use the MinIO web console, uncomment the `ports` block for `minio` in
  `docker-compose.yaml`.
- Rollback is **destructive**: every mount it restores is wiped first, so a
  change made after the savepoint is gone for good.
- The first `savepoint create` pulls `restic/restic:0.19.1` (the image
  `backends.cdc.image` names) and initialises one repository per volume, so it is
  slower than the ones after it.
