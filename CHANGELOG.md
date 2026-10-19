# Changelog

## v0.1.0-alpha.1 — 2026-10-19

The first public release of arcave.

> [!WARNING]
> **This is an alpha.** Commands, flags, output, config keys and the storage format may change between alpha releases with no migration path, so a savepoint taken with one alpha may not be readable by the next. Do not use it in production yet.

### What arcave is

arcave saves the whole state of a running Docker Compose project and lets you come back to it in one command. A savepoint holds every volume, the files each container wrote, the images the services run on, and the compose setup itself. arcave reads the project from Docker, so it works with any compose project and needs no changes to your compose file.

### What you can do

- **Save the whole environment.** `arcave savepoint create <name>` captures databases, caches, object storage and container files in one go. The services are stopped briefly so the data is consistent.
- **Go back in one command.** `arcave rollback <name>`. If something fails halfway, arcave puts you back where you started. It also works after `docker compose down`.
- **Keep it tidy.** `savepoint list` shows the newest first. `savepoint remove` deletes savepoints by id or by age (`--retain-recent`, `--retain-for`), and `--dry-run` previews what would be deleted.
- **Branch your environment.** Sessions are separate lines of savepoints. Fork one with `session create --from`, switch with `session set`, and delete it when you're done.
- **Keep repeated savepoints cheap.** The `cdc` backend stores only what changed since the last savepoint. Container files are deduplicated with either backend.
- **Choose what is saved.** Use `arcave.*` labels in your compose file to leave out a cache directory or a whole service.
- **Store savepoints where you want.** A local folder (the default), S3-compatible storage, Azure Blob Storage, or SFTP.
- **Let agents use it.** `arcave mcp` gives AI agents savepoint, rollback and session tools. An agent can checkpoint before it acts and undo an attempt that failed.
- **Script it.** Every command prints JSON.

### Quick start

```shell
curl -fsSL https://greenmask.io/arcave/install.sh | bash

docker compose up -d
arcave init
export ARCAVE_PROJECT_NAME=myapp            # as `docker compose ls` names it

arcave savepoint create before-migration
# ...run the migration, the tests, the agent...
arcave rollback before-migration

arcave savepoint list
```

Try a change on its own branch, leaving the baseline untouched:

```shell
arcave session create --name feature-x --from baseline
arcave session set feature-x
```

Leave a cache directory out of every savepoint:

```yaml
services:
  app:
    labels:
      arcave.mounts.exclude: /app/cache
```

### Requirements

- Docker 25 or newer.
- Linux (amd64, arm64, armv6, armv7, ppc64le, riscv64, s390x) or macOS (Intel, Apple silicon). There is no Windows build.
- The `cdc` backend pulls and runs the `restic/restic` image.

### Good to know before you rely on it

- The services stop during every savepoint and rollback, for longer when the volumes are large.
- You can only go back to a savepoint you took. There is no point-in-time recovery.
- If you leave a service's container files out of savepoints, a rollback rebuilds it from its `image:`, which must still be in Docker or a registry. The image copy stored in the savepoint isn't used yet.
- Give every stateful service a health check. Without one, arcave treats a service as up as soon as its container starts.
- **There is no garbage collector yet, so in this alpha you clean up by hand:**
  - Removing a savepoint deletes its own data, but container data shared between savepoints and `cdc` chunks stay on disk. `arcave session delete` frees everything a session holds. `baseline` can't be deleted: once you no longer need any of its savepoints, delete its directory in the store by hand.
  - Every rollback leaves an `arcave/…` image tag in Docker. List them with `docker image ls --filter 'reference=arcave/*'` and remove the ones no running container uses.
- After a rollback, running `docker compose up` by hand recreates the containers from your compose file, and the restored container files are lost. Volume data stays.
- Don't point two machines at the same store. Only one command that changes something runs at a time, and that guard works within a single machine.
- On Docker Desktop for macOS, `cdc` cannot capture bind mounts. Use named volumes, or `fullcopy` for that project.
- SFTP storage does not check the server's host key yet.

The full list is in [Limitations](https://www.greenmask.io/docs/arcave/reference/limitations/).

### Links

- [Documentation](https://www.greenmask.io/docs/arcave/)
- [Discord](https://discord.com/invite/rKBKvDECfd)
- [Issues](https://github.com/GreenmaskIO/arcave/issues)
