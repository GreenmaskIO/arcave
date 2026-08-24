---
title: About arcave
description: arcave is an open-source CLI that adds savepoints and one-command rollback to stateful Docker Compose projects, built for migration testing and AI-agent development loops.
keywords: [arcave, docker compose, savepoint, rollback, stateful development, database branching, AI agents, agentic pipeline, development cycle, Open-Source, Greenmask]
sidebar_label: About
slug: /
---

# About arcave

## Savepoints and rollback for stateful Docker Compose projects

Snapshot your environment the way you commit your code — and put it back when the experiment goes wrong.

**arcave** is an open-source CLI utility that adds **savepoints** to a Docker Compose project. A savepoint is a consistent snapshot of every stateful volume the stack holds, taken with the containers stopped so nothing is captured mid-write. A **rollback** puts those volumes back, restores the containers' writable layer, rebuilds the stack as the savepoint recorded it, and starts it again.

Source code is easy to version. The state underneath it is not: a migration that half-applied, a seed script that ran twice, an agent that dropped the wrong table. Recovering usually means recreating the environment or restoring a backup by hand. arcave makes that one command.

```shell title="The whole idea"
arcave savepoint create before-migration
# ...run the migration, the test suite, the agent...
arcave rollback before-migration
```

## Key features

* **[Whole-stack capture](how_it_works.md#what-a-savepoint-covers)** — named volumes, bind mounts and anonymous volumes, for every service in the project. A named volume three services share is captured once. Non-persistent mounts (`tmpfs`, `npipe`, `image`, `cluster`) are skipped and each skip is reported with its reason.
* **[The writable layer too](how_it_works.md#the-two-halves-of-a-savepoint)** — arcave commits each container's writable layer alongside the volumes, so data a service wrote *outside* all of its mounts comes back with everything else.
* **[Rebuilt from the recorded spec](how_it_works.md#what-a-rollback-restores)** — a savepoint stores the compose spec of the stack it was taken from. A rollback rebuilds *that* stack: a service added since is gone, one removed comes back, a changed image or port goes back to what it was.
* **[Automatic safety snapshot](how_it_works.md#the-safety-snapshot)** — before a rollback touches anything it snapshots the live state. If the restore fails, or the services come up unhealthy, arcave replays that snapshot and reports the failure. You are never left between two states without a way back.
* **[Incremental snapshots](configuration.md#snapshot-backends)** — the `cdc` backend captures into a per-volume restic repository through stock `restic` containers, writing only the chunks that changed. No copy-on-write filesystem, no storage appliance: Docker and a container image.
* **[Built for agents](use_cases/agent_loops.md)** — `arcave mcp` exposes savepoint and rollback as MCP tools, so an agent can snapshot before it acts and roll back when validation fails, without shelling out or parsing prose.
* **[Machine-readable by default](commands/index.md#output-and-exit-codes)** — every command prints JSON on stdout and logs on stderr, so a CI job or an agent parses the result instead of scraping it.
* **Reads the running stack, not your files** — arcave never opens a compose file. Move them, delete them, generate them: nothing changes.

## Use cases

* **[Agent iteration loops](use_cases/agent_loops.md)** — give an autonomous agent a checkpoint per attempt, so a failed iteration costs a rollback instead of a rebuild.
* **[Environment branching](use_cases/environment_branching.md)** — a savepoint per feature branch, switched by name. Not database branching in the copy-on-write sense, and the page says exactly where the analogy stops.
* **[Testing destructive migrations](use_cases/migration_testing.md)** — run the migration for real, against real data, as many times as it takes to get it right.
* **[Resetting fixtures and demos](use_cases/fixture_reset.md)** — seed once, snapshot, and reset to that state in seconds for the rest of the project's life.

## Where to start

* **[Installation](installation.md)** — install the binary.
* **[Getting started](getting_started.md)** — initialise a project, take your first savepoint, roll it back.
* **[How arcave works](how_it_works.md)** — the mechanism, end to end.
* **[Limitations](limitations.md)** — what arcave does not do, stated plainly, before you build on it.

## Links

* [GitHub](https://github.com/GreenmaskIO/arcave)
* [Greenmask](https://greenmask.io)
