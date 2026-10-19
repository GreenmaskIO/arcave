---
title: Concepts
description: The arcave vocabulary — savepoint, safety snapshot, the compose project versus the storage tenant, session, store, snapshot backend, capture policy and the inflight marker — in one page.
keywords: [arcave concepts, savepoint, safety snapshot, snapshot backend, compose project, storage tenant, Open-Source, Greenmask]
---

# Concepts

A handful of terms carry the whole tool. This page defines each one and points at the page that uses it.

## Savepoint

A **savepoint** is a consistent snapshot of every stateful volume in a Docker Compose project, plus every container's writable layer, plus the images the services run on, plus the compose spec of the project it was taken from — each of them unless the [capture policy](#capture-policy) leaves it out.

It has an id — a UUID (version 7) assigned by arcave — and optionally a name and a description that you assign.

```json title="A savepoint record (trimmed)"
{
  "id": "019e843e-1650-7000-8000-000000000000",
  "name": "before-migration",
  "description": "Before applying migration 2026_001",
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [ … ],
  "writable_layers": [ … ],
  "images": [ … ],
  "spec": { "format": "compose-yaml" }
}
```

`stats` is one entry per captured volume, `writable_layers` one per container, `images` one per distinct image, and `not_covered` — present only when something was left out — says what the savepoint does not hold and why. [Output](reference/output.md) has every field.

A savepoint is **immutable**. Its record, spec and volume archives live under one directory named by its id, but not everything it names does: deduplicated layer and image data is shared by every savepoint of the compose project, and `cdc` keeps one repository per volume beside them. Copying `<id>/` alone does not move a savepoint — see [Storage layout](reference/storage_layout.md#moving-a-savepoint).

## Savepoint id, name, and `latest`

Three ways to refer to a savepoint, and which one you mean is decided by the token itself:

| Token | Resolves as |
|---|---|
| `latest` | The most recently created savepoint. Reserved word. |
| A canonical UUID | An **id**. Exact, and never falls back to a name. |
| Anything else | A **name**. |

An id is the only form that reaches a hidden safety snapshot. A name can never be mistaken for one: `savepoint create` refuses a name that is itself a savepoint id.

A name matching several savepoints is an **error** that lists the candidate ids, never a silent guess. `savepoint create` refuses a name another savepoint of the compose project already carries, and a name containing `/`, so a name you create today matches one savepoint; the error guards records written before that rule.

## Safety snapshot

A **safety snapshot** is an ordinary savepoint that a rollback takes of the live state before overwriting it. Same store, same format, same record. It differs in exactly three ways: it is named `safety-snapshot-<target id>`, it carries the label `arcave-safety-snapshot`, and that label hides it from `arcave savepoint list` and from every scanning selector — `latest`, a name lookup, `--retain-recent`, `--retain-for`, `--before-date`.

An explicit id still reaches it. That asymmetry is the point: a safety snapshot that outlived its rollback is the last copy of your pre-rollback state, and you must be able to name it. See [How arcave works](how_it_works.md#the-safety-snapshot).

## The compose project versus the storage tenant

Two different things, and conflating them is the most common source of confusion.

**The compose project** is what arcave acts on — what `docker compose ls` lists. You name it per invocation with `-p/--project-name` or `ARCAVE_PROJECT_NAME`. It is deliberately **not** a config key: one directory may hold several compose projects, and a persisted name would pin arcave to one of them.

**The arcave project** is the *storage tenant* — `project.name` in `.arcave/config.yaml`. It scopes the savepoint store, so several compose projects can share one configured storage path without colliding.

They are frequently the same string. They do not have to be.

:::warning
`ARCAVE_PROJECT_NAME` sets both. It is read directly as the compose project's name, and viper also maps it onto the `project.name` config key. Exporting it therefore retargets the storage tenant as well — see [Environment variables](reference/environment_variables.md).
:::

## Session

A **session** is an independent line of savepoints inside a project. `arcave init` puts you in `baseline`, the default session; a fork — `arcave session create --from` — copies a whole session into a new one beside it, and the lines do not mix. A branch is a session.

A command works in the session `--session` or `ARCAVE_SESSION` names, by id or by name. When neither does, it works in the **current session** — the one `arcave session set` recorded in `.arcave/current_session`, on this host only — and in `baseline` when no current session is set. `arcave session current` shows which one that is, and `arcave session unset` goes back to `baseline`. A session is also the lifetime everything below it dies with: deleting one deletes its savepoints and the data they name.

See [Sessions and branching](sessions.md).

## Store

The **store** is where savepoint data lives, rooted at:

```text
<storage path or prefix>/arcave/projects/<arcave-project>/sessions/<session>/compose/<compose-project>/
```

The storage path or prefix is the configured backend's — a local directory, an S3 bucket, an Azure container or an SFTP server. The `arcave` segment is static and not configurable: it bounds every write and every delete arcave performs to a directory it owns.

`.arcave/config.yaml` is the one file arcave reads through the operating system rather than through the store — it is what says where the store points, so it cannot live inside it.

## Snapshot backend

A **snapshot backend** owns the read-and-write pipeline for one volume. arcave ships two:

| Backend | How it captures |
|---|---|
| `fullcopy` | One `data.tar` per volume per savepoint, streamed through the Docker engine. Simple, portable, and every savepoint costs full size. |
| `cdc` | Content-defined chunking into a per-volume restic repository that outlives any one savepoint. Only changed chunks are written. |

`snapshot.backend` in the config says which backend a **new** savepoint uses, and an `arcave.*` label can pick another one per volume. A rollback reads the backend out of the record it is restoring, so changing the setting never orphans an existing savepoint. See [Configuration](reference/configuration.md#snapshot).

Writable layers have a backend of their own, [`writable_layers.backend`](reference/configuration.md#writable_layers): `dedupcopy`, the default, stores the layer data every savepoint shares once; `fullcopy` stores each layer whole.

## Capture policy

What a savepoint captures is the compose file's to say. By default it is everything above. `arcave.*` labels on a service or a volume leave a service, a mount, a writable layer or an image out, or pick a backend for it; the [`capture`](reference/configuration.md#capture) keys in the config set the defaults per kind. The most specific setting wins: a volume's own label, then the service's, then the config. What a savepoint left out is listed in its `not_covered`. See [Capture labels](reference/capture_labels.md).

## Inflight marker

`.arcave/inflight.json` records that a rollback is in progress, or died in progress. It is written before the first destructive write and cleared when the environment is consistent again, and it carries the phase — `recreating`, `restoring`, `restore-done` or `inconsistent` — because the remedy differs per phase.

While a marker stands, the next mutating workflow is refused. The one exception is the remedy: a rollback to that marker's own safety snapshot — or, when the interrupted rollback started from a compose project with no containers and so took no safety snapshot, the same rollback again. See [Troubleshooting](reference/troubleshooting.md#an-interrupted-rollback).

## Related docs

* [How arcave works](how_it_works.md) — these terms in motion.
* [Configuration](reference/configuration.md) — the keys that set them.
* [Commands](reference/cli/index.md) — the commands that use them.
