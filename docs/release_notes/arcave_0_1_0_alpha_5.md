---
title: arcave 0.1.0-alpha.5
description: arcave status, session show, a safety snapshot named by its rollback, and seven dependency advisories fixed.
keywords: [arcave release notes, 0.1.0-alpha.5, changelog, status, sessions, Open-Source, Greenmask]
---

# arcave 0.1.0-alpha.5

arcave 0.1.0-alpha.5 was released on 2026-11-11. See what a compose project runs and which savepoint a rollback put there, look into one session, and name a safety snapshot by the rollback it was taken before.

:::warning

**This is still an alpha.** Commands, flags, output, config keys and the storage format may change between alpha releases with no migration path. Savepoints taken with alpha.4 may not be readable by alpha.5. Do not use it in production yet.

:::

## Breaking changes

- **`arcave rollback --no-safety-snapshot` replaces `--safety-snapshot=false`.** Both flags are plain switches now and take no value: `--safety-snapshot` takes a safety snapshot even under `rollback.safety_snapshot: false`, `--no-safety-snapshot` takes none, and the two cannot be passed together. Without either, the config decides.
- **`ARCAVE_COMPOSE_PROJECT_NAME` names the compose project.** `ARCAVE_PROJECT_NAME` no longer does: it is only the config key `project.name`, the storage tenant, as every `ARCAVE_<KEY>` is. A shell that exports it keeps its tenant and now has to name the compose project with `-p` or the new variable.
- **The MCP tools' `project` argument is `compose_project`.**
- **`savepoint remove --retain-recent 0` deletes every savepoint** of the compose project in the session the command works in, and so does `retain_recent: 0` in the MCP `savepoint_remove` tool, which now takes `null` too. Both used to be refused as no selector. Safety snapshots are untouched. Omitting the flag or the field, or sending `null`, still means no recency rule.
- **`session list` names the current session by `current_source`, not `current`.** The JSON field `current: true|false` is gone; the current session carries `current_source`, which says where it was named — `{"source": "flag"}`, `"env"`, `"pointer"` or `"default"` — and is absent on every other. The same holds for the MCP `session_list` tool. Each entry also gains `safety_snapshot_count`, and the terminal table a `COMPOSE PROJECTS` column, its `STATUS` column shown only under `--all`.
- **`savepoint remove` refuses `--retain-recent 0` beside an id or name, and a negative `--retain-for`.** `0` with ids was silently ignored and is now a selector conflict, as any other retention rule there is; the same holds for `retain_recent: 0` with `refs` in the MCP `savepoint_remove` tool. `--retain-for -1h` (and `retain_for: "-1h"`) used to delete everything and is now refused with `retain-for must be zero or positive`.
- **`--format auto` prints text whenever stdout is a terminal.** Before, it printed text only while the live tree was drawn, so `--progress log`, `--log-format json` or a redirected stderr turned the result into JSON on a terminal. A pipe or a file still gets JSON; `--format json` or `ARCAVE_FORMAT=json` asks for it on a terminal. `arcave tools generate-compose` now has a text summary there too.
- **A safety snapshot has no name; `taken_before_rollback_to` says which rollback it was taken before.** It was named `safety-snapshot-<id of the rollback's target>`, which read as the target and selected nothing. Now its `name` is `""` in every record (`savepoint list`, `session show`, `rolled_back_to`, `removed`, the MCP tools), and the new `taken_before_rollback_to: {"id", "name"}` names that rollback's target as it was named then. In `status` JSON, `deployment.savepoint.name` is absent for a safety snapshot, and the container label `io.arcave.deployment.savepoint-name` is `""`. On a terminal a safety snapshot lists as `before rollback to <target> (safety)`, and rolling back to one prints `Rolled back to the state before rollback to <target> · <id> · <time>`.

## New

- **`arcave status`.** `arcave status -p <compose-project>` shows what the Docker daemon runs for a compose project: the savepoint a rollback deployed, from which session and when, each replica's state, health and ports, the containers recreated outside arcave since, and a rollback in progress or interrupted. It takes no lock and writes nothing. Every rollback now labels the containers it builds with `io.arcave.deployment.*`, which `status` reads, so it answers even after the savepoint is removed; the store only adds the savepoint's record, and a store it cannot read leaves the result partial (exit `1`), not failed. Every warning carries a stable code, explained in [`arcave status`](../reference/cli/status.md#what-the-warnings-say). The MCP server serves it as the `status` tool.
- **`arcave session show [<session>]`.** One session's savepoints, every compose project's at once and newest first, safety snapshots among them and counted apart, and the session it was forked from by id and current name. It reads the store alone: no daemon, no lock, no `-p`. `latest` names the newest session, `current` the one a command works in; both work in `session create --from` too. `session list`, `show` and `current` say where the current session was named — `--session`, `ARCAVE_SESSION`, `session set`, or the default — and which session an override hides. The MCP server serves it as the `session_show` tool.
- **A rollback names the leftover volumes it overwrote.** Rolling back a compose project taken down overwrites the named volumes `compose down` kept, and no safety snapshot holds what they held. The result now lists them in `leftover_volumes` and warns of each, first and severe, on the terminal, in JSON and in the MCP tool.

## Changed

- **`savepoint create` no longer compares its capture with the latest savepoint's.** It warned when a unit's capture changed since then; what a savepoint does not hold is in its `not_covered`, as before, and the capture no longer reads the previous savepoint's spec from the store.
- **Turning encryption on over an arcave project that holds data is refused** by every command, reading or writing, and leaves the project as it was. Before, the first command marked it encrypted and what it held became unreadable. Start an encrypted store at an empty location, or move savepoints across [by hand](../reference/troubleshooting.md#turning-encryption-on-for-a-project-with-data).
- **`github.com/greenmaskio/storages` 0.5.0**, with `golang.org/x/crypto` 0.59.0 and `golang.org/x/net` 0.62.0: seven published advisories in the SSH and HTTP/2 code the SFTP and Azure storage reach. `pkg/sftp` 1.13.12 rejects malformed server replies.

## Fixes

- A rollback, its revert and `savepoint create` no longer fail on a compose project whose one-shot service — a migration, a seed — exits `0` while no other service waits on it with `service_completed_successfully`. arcave now waits for the start itself: an exit `0` is ready, any other exit fails.
- Under `encryption.enabled`, a command that writes nothing — `savepoint list`, `session list` — no longer marks the store encrypted, and the new `status` never does. Before, any command did, and every host without the key was then refused until `encryption.json` was deleted by hand.
- On Docker Desktop, a bind mount is recorded with its host path again. Its VM path (`/host_mnt/…`) reached the savepoint's spec, `tools generate-compose` and a replica a rollback rebuilt whenever another mount came before it.
- `arcave rollback` to an id or a name no savepoint carries says so right away, instead of after waiting for the session lock.
- The savepoint create and rollback titles print the full savepoint id. The first eight characters were the same for savepoints taken within about a minute and no selector accepted them.
- A failure to open a command's dependencies is no longer wrapped in `init dependencies:`; the failed step names itself.
- gc's `FREED` total prints the known part with a `+` when some deletion freed an unknown amount, as `SIZE` does, instead of `?`.

## Good to know before you rely on it

- The labels a rollback puts on its containers are plain text in the Docker daemon, the session's and the savepoint's names included, under `encryption.enabled` too: anyone who can `docker inspect` the containers reads them.
- `status` on another machine, or against another daemon, does not see a rollback interrupted on this one: `.arcave/inflight.json` is local, so it reads `absent` or `degraded` there, not `rollback-pending`.
- On Podman, a rollback does not put back what Podman does not store: `sysctls`, a fixed `ipv4_address`, `domainname` and a few more. A project with `links:` does not run on Podman at all.
- A savepoint taken on Docker is not meant to be rolled back on Podman, or the other way round. arcave checks nothing, and such a rollback can fail after the project has stopped.
- A gc on another machine sharing the store doesn't see a rollback interrupted on this one, and may delete the safety snapshot its recovery needs. Recover first.
- **Lose the encryption key and the store is lost.** There is no recovery key.
- The services stop during every savepoint and rollback, for longer when the volumes are large.
- After a rollback, running `docker compose up` by hand recreates the containers from your compose file, and the restored container files are lost. Volume data stays. `arcave status` names the containers recreated that way.
- On a store shared by several machines, run `arcave gc` without `--dry-run` only while no other machine works in the same session. In rare cases two machines claiming the store lock at the same moment both believe they hold it, and gc could then delete data another machine's savepoint has just reused.
- SFTP storage does not check the server's host key yet.

The full list is in [Limitations](../reference/limitations.md).

#### Full Changelog: [v0.1.0-alpha.4...v0.1.0-alpha.5](https://github.com/GreenmaskIO/arcave/compare/v0.1.0-alpha.4...v0.1.0-alpha.5)

## Links

- [Documentation](../index.md)
- [Discord](https://discord.com/invite/rKBKvDECfd)
- [Issues](https://github.com/GreenmaskIO/arcave/issues)
