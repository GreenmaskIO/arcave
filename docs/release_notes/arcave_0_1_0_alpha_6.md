---
title: arcave 0.1.0-alpha.6
description: Capture modes and hooks for savepoints without a restart, arcave up and down, and tools generate-compose removed.
keywords: [arcave release notes, 0.1.0-alpha.6, changelog, capture modes, docker pause, capture hooks, arcave up, arcave down, Open-Source, Greenmask]
---

# arcave 0.1.0-alpha.6

arcave 0.1.0-alpha.6 was released on 2026-11-18. Take a savepoint without restarting a service, prepare the application for it with hooks, and end an environment or bring it back from a savepoint with `arcave down` and `arcave up`.

:::warning

**This is still an alpha.** Commands, flags, output, config keys and the storage format may change between alpha releases with no migration path. Savepoints taken with alpha.5 may not be readable by alpha.6. Do not use it in production yet.

:::

## Breaking changes

- **`arcave tools generate-compose` is gone, and so is the MCP `compose_generate` tool.** `arcave tools` no longer exists, and `arcave mcp` serves fifteen tools.
- **`rollback.leftover_volumes` is `compose.leftover_volumes`**, and its variable `ARCAVE_COMPOSE_LEFTOVER_VOLUMES`. A config that still sets the old key is refused: `'rollback' has invalid keys: leftover_volumes`. The values and what they do are the same. See [compose](../reference/configuration.md#compose).
- **A savepoint that committed but did not come back fails with `savepoint <id> committed; bringing the project back: …`**, not `…; services failed to start: …`. Under it, `starting services: …` is the restart, `post-capture hook of <replica> failed: …` a hook. The savepoint exists and is complete, as before.
- **A capture failure is prefixed `capturing the savepoint:`**, not `capturing volumes:`.
- **`--progress json`: a savepoint's stop and start are `savepoint.hold` and `savepoint.release`**, steps of type `workflow`, with `compose.stop` and `compose.start` under them. Only the kinds a workflow opens itself — the roots, `session.lock`, `rollback.prepare` and its siblings, `savepoint.drop`, `session.copy`, `gc.find`, `gc.retain`, `gc.erase` — are the stream's contract now; the ones under them that read and change the compose project are shown as they are and may change. See [step kinds](../reference/output.md#step-kinds).

## New

- **Capture modes: a savepoint without a restart.** `arcave.capture.mode` on a service, or `capture.mode` for every service no label names, picks how it is held still while its volumes are copied: `stop` (the default, consistent), `pause` (frozen with `docker pause` and resumed after, crash-consistent — clients keep their connections) or `live` (left running; consistency is yours). A savepoint resumes every replica it finds frozen, yours included, with a warning, and leaves the project running. A rollback, its safety snapshot included, always stops. See [Capture modes](../capture_modes.md).
- **Capture hooks.** `arcave.hook.pre-capture` runs a command in every running replica of a service before it is held, `arcave.hook.post-capture` once the project is back — a `CHECKPOINT`, `redis-cli SAVE`, a maintenance switch, a lock. `.user`, `.timeout` and `.on-error` tune each one; `capture.hooks.timeout` (60s) and `capture.hooks.on_error` (`fail`) are the defaults. A failed `pre-capture` hook fails the savepoint before anything is held; one let through by `on-error: continue` is recorded in the savepoint's `not_covered.failed_hooks`. A rollback runs no hook. See [Hooks](../capture_modes.md#hooks-preparing-the-application).
- **`arcave up` and `arcave down`.** `down` ends the environment on the daemon — containers, networks, the volumes it owns, its containers' anonymous volumes and its restored layer tags — once every replica's deployment labels name this session; bind mounts and external volumes stay, and it takes no savepoint. `up [savepoint]` builds the compose project from a savepoint, the newest by default, and is refused while the daemon holds anything of it. `--force` takes down an environment someone else deployed and clears this compose project's interrupted-rollback marker on `down`, and drops everything of the compose project first, with no backup, on `up`. See [arcave up](../reference/cli/up.md) and [arcave down](../reference/cli/down.md).
- **The `up` and `down` MCP tools**, with the arguments of the commands and the same refusals, their hints in the tools' own terms. Both are marked destructive.

## Changed

- **gc deletes retired safety snapshots last.** It picks the ones past `gc.safety_snapshots.retain_recent` in a new `gc.retain` step, after every collector has looked, and deletes them after every compose project's other kinds. Shared data only a retired one used is collected by the next run. See [arcave gc](../reference/cli/gc.md).
- **Under the hood, arcave now runs its workflows over a provider contract**, with docker compose as the one provider behind it. Savepoint records and command results are unchanged.

## Fixes

- A rollback's safety snapshot keeps its warning about ignored capture labels when its plan fails.
- A rollback whose target savepoint record cannot be read says so, instead of reporting an invalid capture policy.

## Good to know before you rely on it

- A `pause` savepoint is crash-consistent, not clean: the database replays its journal on the next start. Everything it needs to recover — for Postgres `pg_wal` too, if it lives on its own volume — must be in volumes the savepoint captures.
- Podman rootless on cgroup v1 cannot pause: a `pause` savepoint fails there before anything is captured, and the project is started back.
- A hook that times out, or whose savepoint is interrupted, is not killed: the Docker API cannot stop an exec, so it runs on in the container and may finish after the `post-capture` hook that undoes it. Make `post-capture` idempotent.
- Compose's own `pre_stop` and `post_start` hooks are not run by arcave.
- The labels a rollback puts on its containers are plain text in the Docker daemon, the session's and the savepoint's names included, under `encryption.enabled` too: anyone who can `docker inspect` the containers reads them.
- `status` on another machine, or against another daemon, does not see a rollback interrupted on this one: `.arcave/inflight.json` is local, so it reads `absent` or `degraded` there, not `rollback-pending`.
- On Podman, a rollback does not put back what Podman does not store: `sysctls`, a fixed `ipv4_address`, `domainname`, a network's IPAM options and a few more. A project with `links:` does not run on Podman at all.
- A savepoint taken on Docker is not meant to be rolled back on Podman, or the other way round. arcave checks nothing, and such a rollback can fail after the project has stopped.
- A gc on another machine sharing the store doesn't see a rollback interrupted on this one, and may delete the safety snapshot its recovery needs. Recover first.
- **Lose the encryption key and the store is lost.** There is no recovery key.
- The services stop during every rollback, and during a savepoint unless their capture mode is `pause` or `live`, for longer when the volumes are large.
- After a rollback, running `docker compose up` by hand recreates the containers from your compose file, and the restored container files are lost. Volume data stays. `arcave status` names the containers recreated that way.
- On a store shared by several machines, run `arcave gc` without `--dry-run` only while no other machine works in the same session. In rare cases two machines claiming the store lock at the same moment both believe they hold it, and gc could then delete data another machine's savepoint has just reused.
- SFTP storage does not check the server's host key yet.

The full list is in [Limitations](../reference/limitations.md).

#### Full Changelog: [v0.1.0-alpha.5...v0.1.0-alpha.6](https://github.com/GreenmaskIO/arcave/compare/v0.1.0-alpha.5...v0.1.0-alpha.6)

## Links

- [Documentation](../index.md)
- [Discord](https://discord.com/invite/rKBKvDECfd)
- [Issues](https://github.com/GreenmaskIO/arcave/issues)
