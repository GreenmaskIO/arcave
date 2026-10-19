---
title: Prod-like data
description: Get production-like, masked data into a Docker Compose project once, save it as a savepoint, and reuse it for every migration, demo and agent run — with Greenmask for PostgreSQL, or any seeding approach.
keywords: [production-like data, masked data, data anonymization, test data management, greenmask, postgresql, savepoint, docker compose, Open-Source, Greenmask]
---

# Prod-like data

A savepoint is only as useful as the state it holds. A migration tested against an empty schema, a demo on three
hand-typed rows, an agent iterating on fixtures that look nothing like production — each of them passes where the
real thing would fail.

Production data is what exposes those failures, and production data cannot be copied onto a laptop. The answer is
a production-like copy with the sensitive values masked: load it into the project once, save it, and come back to
it for the rest of the project's life instead of loading it again.

## With Greenmask (PostgreSQL)

[Greenmask](/docs/greenmask/next/) dumps a PostgreSQL database, masks it on the way out — names, emails, card
numbers, free text, with deterministic transformers that keep references between tables consistent — and restores
the result into another database. Restore it into the project's database, then save it:

```shell title="Load the masked copy once"
docker compose up -d
greenmask --config config.yml restore latest       # points at the project's Postgres
arcave savepoint create masked-2026-10 --description "masked production dump, October"
```

```shell title="Fork a line of work off it"
arcave session create --from=baseline/masked-2026-10 --name=feat/billing --force
export ARCAVE_SESSION=feat/billing
# ...run the migration, the test suite, the agent...
arcave rollback masked-2026-10
```

Keep the `baseline` session for masked baselines only — one savepoint per dump — and do the work in sessions forked
from it. Pruning inside a work session never reaches a baseline, and `arcave session delete` drops a finished line in
one command. See [Sessions and branching](../sessions.md).

The full walkthrough — pointing the restore at the project, refreshing the baseline when a new dump arrives, and
using it with the Greenmask playground — is on the Greenmask side:
[Save and reset test environments with arcave](/docs/greenmask/next/arcave/).

## With anything else

arcave does not care how the data got there. A masked dump from another tool, a MySQL or MongoDB restore, a seed
script that generates realistic volumes, files copied into a MinIO bucket — once the project holds the state you
want, `arcave savepoint create` saves it the same way. The workflow above is unchanged from that point on.

## Two things to get right

:::danger[Keep unmasked data out of the project you save]
A savepoint captures **every** volume and container filesystem in the project. If the source database you dump from
runs in the same project — or in the same database instance as the restore target — the savepoint holds the
original, unmasked data as well, written to arcave's [store](../concepts.md#store). Run the source outside the
project, and protect the store as you would the data inside it.
:::

:::warning[A rollback also reverts dump storage inside the project]
If the dumps live in a service of the same project — a MinIO bucket, for example — a rollback returns that service to
the state it had at the savepoint, and dumps created after it disappear. Keep the dump storage outside the project,
or take a new savepoint after each dump you want to keep.
:::

## Related docs

* [Resetting fixtures and demos](fixture_reset.md) — the reset loop, once the data is in.
* [Testing destructive migrations](migration_testing.md) — the failures that only real data shows.
* [Agent iteration loops](agent_loops.md) — let an agent work on masked data, never on production.
* [Sessions and branching](../sessions.md) — the lines savepoints live in.
