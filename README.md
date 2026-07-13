# GM

GM is a local environment lifecycle manager for stateful Docker Compose applications. It adds **savepoints** — consistent snapshots of every stateful volume in your project — so you can iterate on schema migrations, data transformations, and app changes and **roll back** to a known-good state in one command.

GM snapshots the persistent mounts of **all** services in your compose project — named volumes, bind mounts, and anonymous volumes alike. Only non-persistent mounts (tmpfs) are skipped.

## Install

```bash
make build      # compiles to ./bin/gm
```

Then run `./bin/gm` (the examples below assume it is on your `PATH` as `gm`).

Requirements: Docker with the Compose plugin (`docker compose`).

## Quick start

```bash
# 1. Initialise GM in a project that has a docker-compose.yaml
gm init

# 2. Snapshot the current state before a risky change
gm savepoint create before-migration

# 3. ...run your migration / experiment...

# 4. If it went wrong, restore the snapshot
gm rollback before-migration
```

`gm init` writes `.gm/config.yaml` (project name, compose files, savepoint dir). Useful flags:

```bash
gm init --name myapp -f docker-compose.yaml -f docker-compose.override.yaml
gm init --savepoint-dir /backups/myapp   # default: .gm/savepoints/<project-name>
```

All commands print their result as JSON on stdout.

---

## `gm savepoint`

A savepoint is a consistent snapshot of every mount. Creating one stops the
services, copies each volume's data into `.gm/savepoints/<project>/<id>/…`, then
restarts the services and waits for their health checks.

### Create

```bash
gm savepoint create                      # unnamed savepoint
gm savepoint create before-migration     # name as a positional argument
gm savepoint create --name before-migration --description "pre v2 schema"
```

Output:

```json
{
  "id": "1781632724",
  "name": "before-migration",
  "description": "pre v2 schema",
  "created_at": "2026-06-16T17:58:44.468475Z"
}
```

The `id` is a Unix timestamp and is what you pass to `rollback` and `remove`.

### List

```bash
gm savepoint list
```

```json
[
  { "id": "1781632724", "name": "before-migration", "description": "", "created_at": "2026-06-16T17:58:44.468475Z" }
]
```

### Remove

Remove **by ID** (one or more), or by **retention rules** — the two modes can't be mixed.

```bash
# by id
gm savepoint remove 1781632724
gm savepoint remove 1781632724 1781632821

# by retention rule
gm savepoint remove --retain-recent 5            # keep the newest 5
gm savepoint remove --retain-for 1w2d            # keep anything newer than 1 week 2 days
gm savepoint remove --before-date 2026-06-01T00:00:00Z

# preview without deleting
gm savepoint remove --retain-recent 5 --dry-run
```

| Flag | Meaning |
|---|---|
| `--retain-recent N` | keep the newest N savepoints, delete the rest |
| `--retain-for DUR` | keep savepoints newer than DUR (e.g. `1w2d3h4m5s`) |
| `--before-date TS` | delete savepoints older than an RFC3339 timestamp |
| `--dry-run` | show what would be deleted without deleting |

An unknown ID is an error and deletes nothing (no partial deletes). When several
retention rules are combined, a savepoint survives if **any** rule keeps it.

---

## `gm rollback`

Restore the environment to a savepoint — the reverse of `savepoint create`.

```bash
gm rollback before-migration   # by name
gm rollback 1781632724         # by id
gm rollback latest             # the most recently created savepoint
```

The target is resolved as `latest` → exact **ID** → **name**. A name that matches
more than one savepoint is an error (it lists the candidate IDs) rather than a
silent guess.

### What it does

1. Stops the services.
2. **Backs up** the current live state of every mount to `.gm/tmp/rollback-<ts>/` first, so a failed rollback can be undone.
3. Wipes each savepoint mount and extracts its snapshot back.
4. Starts the services and waits for health checks.

### Safety: automatic revert

If a restore step fails, **or** the services come up unhealthy, GM automatically
replays the pre-rollback backup to return the environment to exactly where it
started, restarts the services, and reports an error. The safety backup is
deleted on success and **kept** (its path printed) on failure.

> Rollback is destructive: bind-mounted host folders are wiped and restored too.

### `--wait-timeout`

`gm rollback` waits up to `--wait-timeout` seconds (default **120**) for services
to become healthy before deciding a rollback failed and reverting. Increase it for
slow-starting services:

```bash
gm rollback latest --wait-timeout 300
```

Output on success:

```json
{
  "rolled_back_to": {
    "id": "1781632724",
    "name": "before-migration",
    "description": "",
    "created_at": "2026-06-16T17:58:44.468475Z"
  },
  "restored": [
    { "service": "db", "mount_path": "/var/lib/postgresql/data" },
    { "service": "storage", "mount_path": "/export/data" }
  ]
}
```

---

## Where data lives

```
.gm/
├── config.yaml                         # project config
├── manifest.json                       # savepoint index
└── savepoints/<project>/<id>/<service>/<mount-path>/data.tar
```

`<mount-path>` is the container mount path with `/` replaced by `_` (e.g.
`/export/data` → `_export_data`); each mount is stored as a single `data.tar`.
GM uses `docker compose stop` (never `down`), so containers are preserved and
nothing on the host is removed beyond the volume data being snapshotted.
