---
title: Resetting fixtures and demos
description: Seed a Docker Compose environment once, snapshot it, and return to that exact state in one command — including caches and object storage, not just the database.
keywords: [test fixtures, seed data, demo environment, reset database, docker compose, savepoint, Open-Source, Greenmask]
---

# Resetting fixtures and demos

Seeding a realistic environment is slow and you should only do it once.

The usual approach — a `seed.sh` that runs before every test suite, or a dump reloaded between demos — pays that cost repeatedly, and it usually only covers the database. The Redis cache, the MinIO bucket and the files the app wrote to its own filesystem stay dirty, so "reset" quietly means "the database is reset".

Seed once, snapshot, and roll back instead.

## Seed once, snapshot once

```shell title="Pay the cost a single time"
docker compose up -d
./seed.sh                 # schema, reference data, fixtures, uploaded files, warm caches
arcave savepoint create clean --description "seeded baseline, do not delete"
```

## Reset for the rest of the project's life

```shell
arcave rollback clean
```

That is the entire reset. It covers every stateful volume in the project, plus each container's writable layer, plus the compose spec — so the stack comes back on the same images with the same ports, not just with the same rows.

## Between test runs

```shell title="A suite that leaves state behind"
arcave rollback clean
./run-suite.sh
```

Running the rollback *before* the suite rather than after has two advantages: a crashed suite does not skip its own cleanup, and you can inspect the wreckage of a failed run before the next one wipes it.

## For demos

A demo environment drifts. Someone deletes the example order, someone uploads a file with their own name on it, someone leaves the app in a broken state five minutes before the call.

```shell
arcave rollback clean
```

Keep the baseline savepoint permanently and exclude it from pruning by removing explicitly rather than by retention:

```shell
arcave savepoint list                          # find the ids you want gone
arcave savepoint remove 1780339981 1780341002  # and remove only those
```

:::warning
Retention rules do not know which savepoint is precious. `--retain-recent 5` will delete a `clean` baseline once five newer savepoints exist. If a savepoint must survive, prune by id, or keep it in a store of its own via [`storage.directory.path`](../configuration.md#storage-section).
:::

## Why the whole stack matters here

This is the use case where whole-stack capture earns its cost most visibly. After a rollback to `clean`:

* the database holds exactly the seeded rows,
* the cache holds exactly the seeded keys — not a stale entry pointing at a row that no longer exists,
* the object store holds exactly the seeded files,
* and anything the app wrote outside its mounts comes back with the container's writable layer.

A dump-based reset gets the first of those four.

## Related docs

* [Testing destructive migrations](migration_testing.md) — the same reset around a schema change.
* [How arcave works](../how_it_works.md#what-a-savepoint-covers) — precisely what is and is not captured.
* [savepoint remove](../commands/savepoint_remove.md) — retention rules and `--dry-run`.
