---
title: Environment branching
description: Use arcave sessions as branches of a local Docker Compose environment — a session per feature branch, forked from a shared baseline — and understand exactly where the database-branching analogy stops.
keywords: [database branching, environment branching, session, fork, savepoint, feature branch, docker compose, local development, Open-Source, Greenmask]
---

# Environment branching

Branch your environment roughly the way you branch your code: fork a line off a known state, go off and change things on it, and switch between lines when you want to.

<video src={require('../assets/demo/sessions.mp4').default} autoPlay loop muted playsInline controls width="100%" />

*What it shows:* `session create --from baseline` copies the baseline into `feature-x`; with `feature-x` current, a dropped table and its savepoint stay in `feature-x`; `session list` shows both lines; back in `baseline`, `rollback seed` brings the table back.

In arcave a branch is a [session](../sessions.md) — an independent line of savepoints. A fork copies every savepoint of the session it starts from, and nothing written on one line reaches the other. It is not a copy-on-write clone and there is no second environment running beside the first — but for the thing developers actually want from database branching on a laptop, it gets most of the way there with nothing but Docker.

The commands below assume the compose project is named in the shell, `export ARCAVE_PROJECT_NAME=myapp`, or with `-p myapp` on each `savepoint` and `rollback` command.

## The workflow

```shell title="Mark the point every branch starts from"
arcave savepoint create main-baseline        # in baseline, the default session
```

```shell title="Fork a session for the feature branch and switch to it"
git checkout -b feature/new-billing
arcave session create --name feature/new-billing --from baseline
arcave session set feature/new-billing
```

```shell title="Work — migrations, seeds, whatever the feature needs"
./migrate up
./seed-billing-fixtures.sh
arcave savepoint create billing-wip          # recorded in feature/new-billing only
```

```shell title="Start another branch from the same baseline"
git checkout main && git checkout -b feature/reporting
arcave session create --name feature/reporting --from baseline
arcave session set feature/reporting
arcave rollback main-baseline                # the copy in feature/reporting
```

```shell title="Come back to the billing branch later"
git checkout feature/new-billing
arcave session set feature/new-billing
arcave rollback billing-wip
```

A fork deploys nothing: the running project keeps whatever it ran until you roll back inside the new session. `session set` makes a session the current one for every command that names none; to work in another session for one command, pass `--session feature/reporting` instead.

The session names and the git branch names are yours to keep in step. arcave does not read your repository and will not do it for you.

## Where the analogy holds

* **A named line you can return to.** Each session keeps its own savepoints. `arcave rollback billing-wip` in `feature/new-billing` puts the environment back exactly as it stood, as many times as you like, and the other sessions never see that savepoint.
* **A shared starting point.** Every fork from `baseline` starts with a copy of `main-baseline`, under the same name and id.
* **It covers the whole environment.** Postgres, Redis, MinIO, the app container's writable layer, the images, and the compose spec itself. Switching back does not leave a cache holding the other branch's data, or a service running the other branch's image.
* **No infrastructure to run.** No copy-on-write filesystem, no volume plugin, no storage appliance, no hosted service. Docker, and — for [`cdc`](../reference/configuration.md#snapshot) — permission to run a stock `restic` container and expose a loopback port to it.

## Where the analogy stops

This matters more than the part above, because building a workflow on the wrong mental model is expensive later.

**Branches are serial, not concurrent.** There is one environment. Rolling back in `feature/reporting` *replaces* the current state — it does not create a second project you can run alongside the first. You cannot have `feature/billing` and `feature/reporting` both live and both queryable.

**A fork is a copy.** Sessions share no data, so forking costs the size of the session it copies. Check its `size_bytes` in `arcave session list` before you fork a large one.

**Switching costs downtime.** Every rollback stops the project, moves data, and starts it again, then waits for health checks. Seconds to minutes depending on volume size — not the sub-second clone that hosted branching services advertise.

**There is no point-in-time recovery.** arcave captures volumes at rest; it does not read WAL, binlogs, or any transaction stream. You can return to a savepoint you took. You cannot return to "11:42 this morning" unless you happened to take one then.

**Nothing merges.** There is no diff between two savepoints or two sessions and no way to combine them.

**It is not per-database.** The unit is the whole compose project. You cannot branch one service's data while leaving the rest alone — a rollback restores everything the savepoint covered.

**It is single-writer.** One project directory, one lock, one workflow at a time against one Docker daemon. The [store](../reference/configuration.md#storage) can be remote — S3, Azure Blob Storage or SFTP — so the sessions can live off the machine, but the current session is local to each host.

:::warning
If you need concurrent branches, instant switching, or point-in-time recovery, you want a database with real branching — Neon, PlanetScale, Dolt. arcave is what you use when you want *most* of the ergonomics without adopting any of them, on a project that is already Docker Compose.
:::

## Keeping the branches tidy

Branches accumulate. Delete a session once its git branch is merged:

```shell
arcave session list                          # every session, with size_bytes
arcave session set baseline                  # the current session needs --force to remove
arcave session remove feature/new-billing
```

`session remove` removes the session's savepoints and the data they name, prints the `freed_bytes`, and leaves the running project alone. `baseline` always exists: `session remove baseline --force` only empties it.

Within a session, prune savepoints the way you prune merged branches:

```shell
arcave savepoint list
arcave savepoint remove --retain-recent 10
arcave savepoint remove --retain-for 14d --dry-run
arcave savepoint remove before-refactor 019e8488-3ac8-7000-8000-000000000000
arcave gc
```

`--dry-run` reports what would go without deleting anything. Removal by name or id is all-or-nothing: an unknown one is an error and removes nothing, so a typo cannot half-empty your set of branch points. `savepoint remove` deletes the records and each savepoint's own directory; [`arcave gc`](../reference/cli/gc.md) frees what they shared with other savepoints, and their `cdc` snapshots.

## Related docs

* [Sessions and branching](../sessions.md) — what a session is, and how a fork relates to a rollback.
* [arcave session](../reference/cli/session.md) — the commands, their flags and their output.
* [Agent iteration loops](agent_loops.md) — the same mechanism at a much tighter cadence.
* [savepoint remove](../reference/cli/savepoint_remove.md) — the retention rules in full.
* [Limitations](../reference/limitations.md) — the constraints above, stated once for the whole tool.
