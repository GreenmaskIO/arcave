---
title: Concepts
description: The arcave vocabulary — savepoint, safety snapshot, the compose project versus the storage tenant, session, store, snapshot backend and the inflight marker — in one page.
keywords: [arcave concepts, savepoint, safety snapshot, snapshot backend, compose project, storage tenant, Open-Source, Greenmask]
---

# Concepts

Seven terms carry the whole tool. This page defines each one and points at the page that uses it.

## Savepoint

A **savepoint** is a consistent snapshot of every stateful volume in a Docker Compose project, plus every container's writable layer, plus the compose spec of the project it was taken from.

It has an id — a Unix timestamp as decimal digits, assigned by arcave — and optionally a name and a description that you assign.

```json title="A savepoint record"
{
  "id": "1780335122",
  "name": "before-migration",
  "description": "Before applying migration 2026_001",
  "created_at": "2026-06-01T19:41:12.123456789Z"
}
```

A savepoint is **immutable** and **self-contained**: everything it names lives under one directory, so `cp -r <id>/` moves the whole thing.

## Savepoint id, name, and `latest`

Three ways to refer to a savepoint, and which one you mean is decided by the token itself:

| Token | Resolves as |
|---|---|
| `latest` | The most recently created savepoint. Reserved word. |
| A run of decimal digits | An **id**. Exact, and never falls back to a name. |
| Anything else | A **name**. |

An id is the only form that reaches a hidden safety snapshot. `arcave rollback 1780335122` will not resolve to a savepoint merely *called* `1780335122`.

A name matching several savepoints is an **error** that lists the candidate ids, never a silent guess. Names are yours to keep unique — arcave does not enforce it at creation time.

## Safety snapshot

A **safety snapshot** is an ordinary savepoint that a rollback takes of the live state before overwriting it. Same store, same format, same record. It differs in exactly three ways: it is named `safety-snapshot-<target id>`, it carries the label `arcave-safety-snapshot`, and that label hides it from `arcave savepoint list` and from every scanning selector — `latest`, a name lookup, `--retain-recent`, `--retain-for`, `--before-date`.

An explicit id still reaches it. That asymmetry is the point: a safety snapshot that outlived its rollback is the last copy of your pre-rollback state, and you must be able to name it. See [How arcave works](how_it_works.md#the-safety-snapshot).

## The compose project versus the storage tenant

Two different things, and conflating them is the most common source of confusion.

**The compose project** is what arcave acts on — what `docker compose ls` lists. You name it per invocation with `-p/--project-name` or `ARCAVE_PROJECT_NAME`. It is deliberately **not** a config key: one directory may hold several compose projects, and a persisted name would pin arcave to one of them.

**The arcave project** is the *storage tenant* — `project.name` in `.arcave/config.yaml`. It scopes the savepoint store, so several compose projects can share one configured storage path without colliding.

They are frequently the same string. They do not have to be.

:::warning
`ARCAVE_PROJECT_NAME` sets both. It is read directly as the compose project's name, and viper also maps it onto the `project.name` config key. Exporting it therefore retargets the storage tenant as well — see [Configuration](reference/environment_variables.md).
:::

## Session

A **session** is the lifetime everything under a store root dies with. It is a path segment between the arcave project and the compose project.

Today there is exactly one session, `global`, and nothing creates another. The segment exists so that a future disposable timeline — a sandbox, a throwaway branch — is a new session rather than a new storage layout. You do not configure it.

## Store

The **store** is where savepoint data lives, rooted at:

```text
<storage.directory.path>/arcave/projects/<project>/sessions/global/compose/<compose-project>/
```

The `arcave` segment is static and not configurable: it bounds every write and every delete arcave performs to a directory it owns.

`.arcave/config.yaml` is the one file arcave reads through the operating system rather than through the store — it is what says where the store points, so it cannot live inside it.

## Snapshot backend

A **snapshot backend** owns the read-and-write pipeline for one volume. arcave ships two:

| Backend | How it captures |
|---|---|
| `fullcopy` | One `data.tar` per volume per savepoint, streamed through the Docker engine. Simple, portable, and every savepoint costs full size. |
| `cdc` | Content-defined chunking into a per-volume restic repository that outlives any one savepoint. Only changed chunks are written. |

`snapshot.backend` in the config says which backend a **new** savepoint uses. A rollback reads the backend out of the record it is restoring, so changing the setting never orphans an existing savepoint. See [Configuration](reference/configuration.md#snapshot).

## Inflight marker

`.arcave/inflight.json` records that a rollback is in progress, or died in progress. It is written before the first destructive write and cleared when the environment is consistent again, and it carries the phase — `recreating`, `restoring`, `restore-done` or `inconsistent` — because the remedy differs per phase.

While a marker stands, the next mutating workflow is refused. The one exception is a rollback to that marker's own safety snapshot, which is the remedy. See [Troubleshooting](reference/troubleshooting.md#an-interrupted-rollback).

## Related docs

* [How arcave works](how_it_works.md) — these terms in motion.
* [Configuration](reference/configuration.md) — the keys that set them.
* [Commands](reference/cli/index.md) — the commands that use them.
