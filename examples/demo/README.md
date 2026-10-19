# arcave demo

A self-contained demo of `arcave` savepoint / rollback. A small Go web app reads
and writes three stores; you add data, take a savepoint, change the data, then
roll back and watch every store return to its savepoint state. The walkthrough
switches the project to the **cdc** snapshot backend, so every volume capture
goes into a restic repository and only the changed chunks are written.

| Store      | Mount in `docker-compose.yaml`     | arcave volume type   |
|------------|------------------------------------|----------------------|
| PostgreSQL | `pgdata:/var/lib/postgresql/data`  | **named volume**     |
| MinIO      | `miniodata:/data`                  | **named volume**     |
| Redis      | `- /data`                          | **anonymous volume** |
| app (Go)   | *(none)*                           | — the UI             |

The project mounts no host directory. With cdc, restic sets `O_NOATIME` on
every source file, and Docker Desktop's VirtioFS answers the next read with
`EIO` — so a bind mount on macOS cannot be captured by cdc, while volumes, which
live in the VM's own filesystem, are unaffected.

Besides the volumes, every savepoint holds each container's writable layer —
the app's included — and the image each service runs on, so a rollback puts the
containers' own filesystems back too.

## Prerequisites

- Docker 25 or newer, with the Compose plugin (`docker compose`) for the
  walkthrough's own commands
- The `arcave` binary — build it from the repo root: `make build` → `bin/arcave`

## Walkthrough

Run everything from this directory (`examples/demo/`): arcave keeps its config
and its store in `.arcave/` here.

```bash
cd examples/demo

# 1. Start the compose project (builds the app image, pulls postgres/minio/redis).
#    --wait blocks until every service is healthy.
docker compose up -d --wait

# 2. Open the UI and add a few notes to each store.
open http://localhost:8080            # macOS; otherwise just browse there
#    Each store starts pre-seeded with distinct, labelled data.

# 3. Name the compose project for arcave. The compose file pins it as
#    `name: arcave-demo`; arcave reads no compose file, so it needs the name.
export ARCAVE_PROJECT_NAME=arcave-demo

# 4. Write .arcave/config.yaml, then select the cdc backend in it: set
#    `snapshot.backend: cdc`, by hand or with the line below (works with GNU
#    and BSD sed). The backends.cdc defaults apply without being written out.
#    .arcave/ is gitignored, so this is a step you run once per clone.
../../bin/arcave init
sed -i.bak 's/backend: fullcopy/backend: cdc/' .arcave/config.yaml && rm .arcave/config.yaml.bak

# 5. Snapshot the three volumes, the four containers' writable layers and
#    their images.
../../bin/arcave savepoint create before-changes
../../bin/arcave savepoint list           # confirm it's recorded

# 6. Now change things: add or remove notes in the UI so the stores differ
#    from the savepoint.

# 7. Roll back — arcave stops the services, rebuilds the containers on their
#    saved writable layers, restores every volume, and restarts. The output
#    lists every restored volume and writable layer.
../../bin/arcave rollback before-changes
```

Refresh <http://localhost:8080>: every change you made after the savepoint is
gone, and all three panels show exactly the data captured at savepoint time —
proving arcave restored both named volumes and the anonymous volume together.

## How it works

The store is `.arcave/storage/`, and everything this demo writes sits under
`.arcave/storage/arcave/projects/arcave-demo/sessions/00000000-0000-0000-0000-000000000001/compose/arcave-demo/`
— the arcave project, the `baseline` session, and the compose project. Below,
`<root>` stands for that directory.

- `arcave savepoint create` stops the services, then backs each volume up with
  restic into a per-volume repository under `<root>/restic/<object-id>/`,
  stores the writable layers and images, writes the compose spec and the record
  `<root>/<savepoint-id>/savepoint.json`, and starts the services again.
  The repositories outlive the savepoints, so every capture after the first
  writes only the chunks that changed. Open one with a plain
  `RESTIC_PASSWORD=arcave restic -r <root>/restic/volume-named-arcave-demo_pgdata snapshots`
  — the password is a constant, since whoever can read the store can read
  `.arcave` anyway.
- `arcave rollback` first loads the savepoint's writable layers into the
  daemon, then stops the services and takes a **safety snapshot** of the live
  state — an ordinary savepoint in the same store, hidden from
  `savepoint list` unless you pass `--include-safety-snapshots`. It then
  rebuilds the containers from the savepoint's spec and layers, restores each
  volume and restarts. If a step fails or a service never comes up healthy, it
  replays the safety snapshot, so a failed rollback leaves the environment where
  it started. On success the safety snapshot is dropped.
- cdc restores incrementally by default (`backends.cdc.restore_mode:
  incremental`): restic writes only what differs from the savepoint and deletes
  what the savepoint does not hold. Set it to `wipe` to clear each volume and
  restore it whole.

## Cleanup

```bash
docker compose down -v                # stop and remove containers + named volumes
rm -rf .arcave                        # remove arcave metadata, savepoints and restic repos
docker images 'arcave/*'              # the tags a rollback created; remove them with docker rmi
```

## Notes

- Only the app (`8080`) publishes a host port, to avoid clashing with other
  local compose projects. Postgres/Redis/MinIO are reached over the compose
  network. To use the MinIO web console, uncomment the `ports` block for `minio`
  in `docker-compose.yaml`.
- Rollback is **destructive**: every volume it restores ends up exactly as the
  savepoint recorded it, so a change made after the savepoint is gone for good.
- After a rollback each container runs on an `arcave/…:sp-<savepoint-id>` tag
  holding its restored writable layer. A later `docker compose up` that
  recreates the containers puts them back on the compose file's images and drops
  those layers; roll back again to restore them.
- The first `savepoint create` pulls `restic/restic:0.19.1` (the image
  `backends.cdc.image` names) and initialises one repository per volume, so it is
  slower than the ones after it.
