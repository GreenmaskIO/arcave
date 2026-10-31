---
title: Use cases
description: What arcave savepoints are good for — prod-like data, agent iteration loops, environment branching, destructive migration testing and fixture resets — and the three properties that make them work.
keywords: [arcave use cases, savepoint workflow, agent development loop, database branching, migration testing, docker compose, Open-Source, Greenmask]
---

# Use cases

Everything on the pages below rests on three properties of a savepoint.

* **Whole-project** — a savepoint is not a database dump. It captures every stateful volume in the project, every container's writable layer, and the compose spec of the project itself. Restoring one puts the *environment* back, not one service's data.
* **Consistent** — the project is stopped before a byte is read, so nothing is captured mid-write. The same mechanism covers every service in the project, whatever each of them runs.
* **Reversible** — a rollback takes a safety snapshot of what it is about to overwrite, and replays it automatically if anything fails. A failed rollback leaves you where you started, not somewhere new.

And on one constraint, which shapes every workflow here:

* **Stopped, not instant** — a savepoint and a rollback both stop and restart the project. The window is proportional to the data moved plus your services' health-check time. This is not zero-downtime branching, and no page here will pretend otherwise.

## Taking one

```shell title="The whole API surface"
arcave savepoint create <NAME>       # capture
arcave savepoint list                # find
arcave rollback <NAME|ID|latest>     # restore
arcave savepoint remove <NAME|ID>    # prune
arcave gc                            # reclaim the space
```

Four commands, and `gc` to give back the space removed savepoints held. Every workflow below is a way of arranging them. When the savepoints need more than one line — a branch per feature, a baseline kept apart — the [`arcave session`](../reference/cli/session.md) commands add independent lines of savepoints beside the default one; see [Sessions and branching](../sessions.md).

## A workflow for every situation

### [Prod-like data](prod_like_data.md)

Every workflow below is only as good as the state it saves. Load a masked copy of production — with Greenmask for PostgreSQL, or any seeding approach — save it once, and reuse it everywhere.

### [Agent iteration loops](agent_loops.md)

An autonomous agent modifies schemas, runs migrations, and writes to databases as part of its normal loop. Give it a checkpoint per attempt and a failed iteration costs a rollback instead of a rebuild. `arcave mcp` puts savepoint and rollback in the agent's tool list directly.

### [Environment branching](environment_branching.md)

A session per feature branch, forked from a shared baseline and switched by name. It resembles database branching enough to be useful and differs from it enough to be worth stating precisely — that page does both.

### [Testing destructive migrations](migration_testing.md)

Run the migration for real, against real data, and again, and again, until it is right. The reset is one command and does not involve reloading a dump.

### [Resetting fixtures and demos](fixture_reset.md)

Seed the environment once, snapshot it, and return to that exact state for the rest of the project's life — including the object storage and the cache, not just the database.

## Choosing a backend for the loop

How often you snapshot decides which [snapshot backend](../reference/configuration.md#snapshot) you want.

| | `fullcopy` | `cdc` |
|---|---|---|
| Cost per savepoint | Full size, every time | Only the chunks that changed |
| Good for | Occasional checkpoints, small volumes | Tight loops, large volumes, many savepoints |
| Dependencies | None beyond Docker | Pulls and runs `restic/restic` containers |

For the agent and branching workflows, `cdc` is what makes the loop cheap enough to actually run.

## Related docs

* [How arcave works](../how_it_works.md) — the mechanism behind these properties.
* [Examples](../examples/index.md) — the same workflows as complete walkthroughs.
* [Limitations](../reference/limitations.md) — where each of these stops working.
