# Changelog

## v0.1.0-alpha.2 — 2026-10-31

Clean up after yourself with `arcave gc`, share one store between machines, and watch what each command is doing as it runs.

![arcave: savepoint, break, rollback](https://raw.githubusercontent.com/GreenmaskIO/arcave/main/docs/assets/demo/base-cycle.gif)

> [!WARNING]
> **This is still an alpha.** Commands, flags, output, config keys and the storage format may change between alpha releases with no migration path. Savepoints taken with alpha.1 may not be readable by alpha.2. Do not use it in production yet.

### Breaking changes

- **`session delete` is now `session remove`.** The MCP tool `session_delete` is now `session_remove`, and the JSON field `deleted` is now `removed`.
- **The MCP tool `savepoint_remove` takes `refs` instead of `ids`.** A ref is an id or a name.
- **The terminal shows a live progress tree and a text summary.** Before, every command printed JSON. Now a command run on a terminal (with `log.format: text`) draws its steps on stderr and prints its result as text. When stdout is a pipe or a file, the result is still JSON, so `ID=$(arcave savepoint create | jq -r .id)` keeps working. `--format json` asks for JSON on a terminal too.
- **`log.level` is empty by default.** Above the live tree only warnings are printed; with `--progress log` the default is `info`. Set `--log-level`, `ARCAVE_LOG_LEVEL` or `log.level` to choose.
- **Volume sizes moved in the savepoint record.** Each `stats` entry now carries `size_bytes`, `stored_bytes` and `reused_bytes` itself. They are no longer inside `payload`.
- **`savepoint remove` no longer deletes `cdc` data.** The restic snapshots of a removed savepoint stay until `arcave gc` forgets and prunes them.

### New

- **`arcave gc` gives back the space removed savepoints held.** It deletes savepoint directories no record names, stored images and layers no savepoint uses, the `cdc` snapshots of removed savepoints, and the `arcave/…` tags rollbacks left in Docker once no container uses them. `--dry-run` shows what would go and deletes nothing. It covers every compose project of the session, so it takes no `-p`. Agents get it as the MCP tool `gc`. New tags are kept for an hour, set by `gc.restored_layer_grace`.

  ```shell
  arcave savepoint remove --retain-recent 5
  arcave gc --dry-run
  arcave gc
  ```

- **Several machines can share one store.** Every command that changes a session now also locks it in the store, so two machines take turns instead of writing over each other. A command that finds the session busy waits up to 60 seconds, then fails naming the machine and the command that holds it. The lock of a crashed command is taken over after 30 seconds. If a rollback succeeds but can't drop its safety snapshot, the result names it in `safety_snapshot_kept`.
- **See progress as it happens.** `--progress tty` draws a live tree of the steps with bytes, rates and warnings. `--progress log` writes one log line per step. `--progress json` writes every step as a JSON line on stdout, with the result last. The default picks the tree on a terminal and the log elsewhere. `--format text|json` chooses how the result is printed. Both can also be set with `ARCAVE_PROGRESS` and `ARCAVE_FORMAT`.
- **Get a compose file back from a running project.** `arcave tools generate-compose -o recovered.yaml` writes a compose file from what Docker reports about the project. It is the same spec a savepoint records, for a project whose compose file was lost or never written. Agents get it as the MCP tool `compose_generate`.

  ```shell
  arcave tools generate-compose -p myapp -o recovered.yaml
  ```

- **Shorter commands.** `sp` and `save` for `savepoint`, `ls` for `list` and `rm` for `remove`:

  ```shell
  arcave sp ls
  arcave sp rm before-migration
  ```

### Fixes

- `savepoint remove` takes names as well as ids, as `rollback` does. Before, a name was reported as not found.
- `session remove baseline --force` empties the default session. It deletes all of its savepoints and keeps the session itself. Without `--force` it is refused, with a hint.

### Good to know before you rely on it

- The services stop during every savepoint and rollback, for longer when the volumes are large.
- You can only go back to a savepoint you took. There is no point-in-time recovery.
- If you leave a service's container files out of savepoints, a rollback rebuilds it from its `image:`, which must still be in Docker or a registry. The image copy stored in the savepoint isn't used yet.
- Give every stateful service a health check. Without one, arcave treats a service as up as soon as its container starts.
- After a rollback, running `docker compose up` by hand recreates the containers from your compose file, and the restored container files are lost. Volume data stays.
- Some space takes two `arcave gc` runs to come back: an unused image's layers go on the run after the image.
- On a store shared by several machines, run `arcave gc` without `--dry-run` only while no other machine works in the same session. In rare cases the store lock can't tell two machines apart, and gc could then delete data another machine's savepoint has just reused.
- gc leaves alone an `arcave/…` tag of a compose project no session holds any more, or one an earlier arcave version made. List them with `docker image ls --filter 'reference=arcave/*'` and remove those by hand.
- On Docker Desktop for macOS, `cdc` cannot capture bind mounts. Use named volumes, or `fullcopy` for that project.
- SFTP storage does not check the server's host key yet.

The full list is in [Limitations](https://www.greenmask.io/docs/arcave/reference/limitations/).

### Links

- [Documentation](https://www.greenmask.io/docs/arcave/)
- [Discord](https://discord.com/invite/rKBKvDECfd)
- [Issues](https://github.com/GreenmaskIO/arcave/issues)

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
