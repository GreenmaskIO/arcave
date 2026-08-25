---
title: Testing destructive migrations
description: Run a schema migration against real data as many times as it takes — snapshot before, roll back after, and iterate without reloading a dump.
keywords: [schema migration, migration testing, destructive migration, rollback, docker compose, postgres, Open-Source, Greenmask]
---

# Testing destructive migrations

A migration that only ever runs against an empty schema is not tested. The interesting failures — a `NOT NULL` added to a column that has nulls, a type change that truncates, a backfill that takes locks it should not — need real data to show up.

The reason people do not test that way is the reset. Once the migration has run, getting back to "before" means dropping the database and reloading a dump, and after the fourth iteration that is where all the time goes.

A savepoint makes the reset one command.

## The loop

```shell title="Capture the pre-migration state once"
arcave savepoint create pre-migration --description "schema v14, seeded"
```

```shell title="Run it for real"
./migrate up
```

```shell title="Inspect what it did"
docker compose exec -T db psql -U postgres -c '\d+ orders'
docker compose exec -T db psql -U postgres -c 'SELECT count(*) FROM orders WHERE tenant_id IS NULL;'
```

```shell title="Reset and try the fixed version"
arcave rollback pre-migration
./migrate up
```

Repeat until the migration is right. Every iteration starts from byte-identical data, so a difference in the outcome is a difference in the migration.

## What this catches that a fresh schema does not

* **Backfills that fail on real distributions** — nulls, duplicates, encodings, rows that predate a constraint.
* **Down migrations that lose data.** Run `up`, run `down`, roll back to `pre-migration`, and compare. A `down` that silently drops a column is visible immediately.
* **Migrations that are not idempotent.** Run `up` twice from the same savepoint and see whether the second run is a no-op or a disaster.
* **Cross-service drift.** The migration changed the schema, but the cache still holds rows in the old shape and the object store still holds files named the old way. A savepoint covers all three, so the rollback puts all three back and the next attempt is genuinely clean.

## Testing the down migration properly

```shell
arcave savepoint create pre-migration
./migrate up
arcave savepoint create post-migration
./migrate down

# Now compare the state after `down` against the state before `up`.
# Then get back to a known point either way:
arcave rollback pre-migration      # to try `up` again
arcave rollback post-migration     # to try `down` again
```

Two savepoints and two names give you both ends of the migration as fixed points, which is what makes the comparison meaningful.

## In CI

The same loop runs unattended. Machine-readable output is the default, so there is nothing to parse:

```shell
arcave --log-format json savepoint create ci-baseline
./migrate up || { arcave rollback ci-baseline; exit 1; }
./run-integration-tests.sh || { arcave rollback ci-baseline; exit 1; }
arcave savepoint remove --retain-recent 3
```

:::note
Budget for the stop-and-start window. Every `savepoint create` and every `rollback` restarts the stack and waits for health checks — up to [`--wait-timeout`](../reference/configuration.md#compose), two minutes by default. On a slow-starting stack, raise it rather than letting a timeout read as a migration failure.
:::

## Related docs

* [Resetting fixtures and demos](fixture_reset.md) — the same reset, for seeded data rather than schemas.
* [savepoint create](../reference/cli/savepoint_create.md) — flags and output.
* [rollback](../reference/cli/rollback.md) — what a rollback restores, exactly.
