---
title: arcave 0.1.0-alpha.2
description: arcave gc, one store shared between machines, and a live progress tree.
keywords: [arcave release notes, 0.1.0-alpha.2, changelog, Open-Source, Greenmask]
---

# arcave 0.1.0-alpha.2

arcave 0.1.0-alpha.2 was released on 2026-10-31. Clean up after yourself with `arcave gc`, share one store between machines, and watch what each command is doing as it runs.

<video src={require('../assets/demo/base-cycle.mp4').default} autoPlay loop muted playsInline controls width="100%" />

:::warning

**This is still an alpha.** Commands, flags, output, config keys and the storage format may change between alpha releases with no migration path. Savepoints taken with alpha.1 may not be readable by alpha.2. Do not use it in production yet.

:::

## Breaking changes

- **`session delete` is now `session remove`.** The MCP tool `session_delete` is now `session_remove`, and the JSON field `deleted` is now `removed`.
- **The MCP tool `savepoint_remove` takes `refs` instead of `ids`.** A ref is an id or a name.
- **The terminal shows a live progress tree and a text summary.** Before, every command printed JSON. Now a command run on a terminal (with `log.format: text`) draws its steps on stderr and prints its result as text. When stdout is a pipe or a file, the result is still JSON, so `ID=$(arcave savepoint create | jq -r .id)` keeps working. `--format json` asks for JSON on a terminal too.
- **`log.level` is empty by default.** Above the live tree only warnings are printed; with `--progress log` the default is `info`. Set `--log-level`, `ARCAVE_LOG_LEVEL` or `log.level` to choose.
- **Volume sizes moved in the savepoint record.** Each `stats` entry now carries `size_bytes`, `stored_bytes` and `reused_bytes` itself. They are no longer inside `payload`.
- **`savepoint remove` no longer deletes `cdc` data.** The restic snapshots of a removed savepoint stay until `arcave gc` forgets and prunes them.

## New

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

## Fixes

- `savepoint remove` takes names as well as ids, as `rollback` does. Before, a name was reported as not found.
- `session remove baseline --force` empties the default session. It deletes all of its savepoints and keeps the session itself. Without `--force` it is refused, with a hint.

## Good to know before you rely on it

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

The full list is in [Limitations](../reference/limitations.md).

#### Full Changelog: [v0.1.0-alpha.1...v0.1.0-alpha.2](https://github.com/GreenmaskIO/arcave/compare/v0.1.0-alpha.1...v0.1.0-alpha.2)

## Links

- [Documentation](../index.md)
- [Discord](https://discord.com/invite/rKBKvDECfd)
- [Issues](https://github.com/GreenmaskIO/arcave/issues)
