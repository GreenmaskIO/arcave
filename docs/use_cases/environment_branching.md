---
title: Environment branching
description: Use arcave savepoints as branch points for a local Docker Compose environment — a savepoint per feature branch — and understand exactly where the database-branching analogy stops.
keywords: [database branching, environment branching, savepoint, feature branch, docker compose, local development, Open-Source, Greenmask]
---

# Environment branching

Branch your environment roughly the way you branch your code: mark a point you can come back to, go off and change things, and return to that point when you want to.

A savepoint is that mark. It is not a copy-on-write clone and there is no second environment running beside the first — but for the thing developers actually want from database branching on a laptop, it gets most of the way there with nothing but Docker.

## The workflow

```shell title="Mark the point you branch from"
git checkout -b feature/new-billing
arcave savepoint create main-baseline
```

```shell title="Work — migrations, seeds, whatever the feature needs"
./migrate up
./seed-billing-fixtures.sh
```

```shell title="Go back to the baseline for another branch"
arcave rollback main-baseline
git checkout -b feature/reporting
```

```shell title="Or keep a branch's state to return to later"
arcave savepoint create billing-wip
```

The savepoint names and the git branch names are yours to keep in step. arcave does not read your repository and will not do it for you.

## Where the analogy holds

* **A named point you can return to.** `arcave rollback main-baseline` puts the environment back exactly as it stood, as many times as you like.
* **Cheap to keep several.** With the [`cdc` backend](../reference/configuration.md#snapshot), each savepoint stores only the chunks that changed since the last one. A dozen branch points on a large volume is not a dozen full copies.
* **It covers the whole environment.** Postgres, Redis, MinIO, the app container's writable layer, and the compose spec itself. Switching back does not leave a cache holding the other branch's data, or a service running the other branch's image.
* **No infrastructure to run.** No copy-on-write filesystem, no volume plugin, no storage appliance, no hosted service. Docker, and — for `cdc` — permission to run a stock `restic` container and expose a loopback port to it.

## Where the analogy stops

This matters more than the part above, because building a workflow on the wrong mental model is expensive later.

**Branches are serial, not concurrent.** There is one environment. Rolling back to `main-baseline` *replaces* the current state — it does not create a second stack you can run alongside the first. You cannot have `feature/billing` and `feature/reporting` both live and both queryable.

**Switching costs downtime.** Every rollback stops the stack, moves data, and starts it again, then waits for health checks. Seconds to minutes depending on volume size — not the sub-second clone that hosted branching services advertise.

**There is no point-in-time recovery.** arcave captures volumes at rest; it does not read WAL, binlogs, or any transaction stream. You can return to a savepoint you took. You cannot return to "11:42 this morning" unless you happened to take one then.

**Nothing merges.** There is no diff between two savepoints and no way to combine them. A savepoint is a point you return to, full stop.

**It is not per-database.** The unit is the whole stack. You cannot branch one service's data while leaving the rest alone — a rollback restores everything the savepoint covered.

**It is local, and single-writer.** One project, one lock, one workflow at a time. This is a development-machine tool, not a shared-team environment service.

:::warning
If you need concurrent branches, instant switching, or point-in-time recovery, you want a database with real branching — Neon, PlanetScale, Dolt. arcave is what you use when you want *most* of the ergonomics without adopting any of them, on a stack that is already Docker Compose.
:::

## Keeping the branch points tidy

Branch points accumulate. Prune them the way you prune merged branches:

```shell
arcave savepoint list
arcave savepoint remove --retain-recent 10
arcave savepoint remove --retain-for 14d --dry-run
arcave savepoint remove 1780335122 1780339981
```

`--dry-run` reports what would go without deleting anything. Deletion by id is all-or-nothing: an unknown id is an error and removes nothing, so a typo cannot half-empty your set of branch points.

## Related docs

* [Agent iteration loops](agent_loops.md) — the same mechanism at a much tighter cadence.
* [savepoint remove](../reference/cli/savepoint_remove.md) — the retention rules in full.
* [Limitations](../reference/limitations.md) — the constraints above, stated once for the whole tool.
