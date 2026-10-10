---
title: Storage layout
description: Where arcave writes — the store root and its segments, a savepoint's own directory, how a volume is named, and the blobs savepoints share.
keywords: [arcave storage layout, store root, object id, savepoint directory, deduplication, restic]
---

# Storage layout

Everything arcave stores lives under one root, computed from [`storage.directory.path`](configuration.md#storage):

```text
<storage.directory.path>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>/
```

Four things are named there, outermost first:

| Segment | What it is | What it accepts |
|---|---|---|
| `arcave` | The static namespace every store is rooted under. Not configurable. | — |
| `<project>` | The storage tenant, `project.name`. | Starts with `[a-z0-9]`, continues with `[a-z0-9_-]` |
| `<session>` | The session — the line of savepoints, and the lifetime everything below it dies with. | `baseline` (the default), or a canonical UUID |
| `<compose-project>` | The compose project, named per invocation with `-p`. | Starts with `[a-z0-9]`, continues with `[a-z0-9_-]` |

The static `arcave` segment is what bounds every write and every delete: point the path at `/etc` and arcave operates in `/etc/arcave`. Each segment below it is checked against an allowlist rather than a denylist, because a list of what a path segment must *not* be is never provably complete.

`compose/` has a reserved sibling for a second timeline that is not built yet, which is why the compose project sits one level down rather than at the root.

## Inside a compose project's store root

```text
<compose-project root>/
  019e843e-1650-7000-8000-000000000000/                                  a savepoint, named by its id
    savepoint.json                             the record — written last, and it is the commit
    project.yaml                               the compose spec the project was captured under
    volume-named-myapp_pgdata/
      data.tar                                 a fullcopy capture of one volume
    images/app/replicas/1/
      index.json                               verbatim from the image archive
      manifest.json                            verbatim from the image archive
      oci-layout                               verbatim from the image archive
      image.json                               what to rebuild the layer from — written last
  019e8488-3ac8-7000-8000-000000000000/                                  the next savepoint
  shared/oci/blobs/sha256/<digest>             the image blobs every savepoint here shares
  restic/volume-named-myapp_pgdata/            one cdc repository per volume
```

A savepoint id is a canonical UUID, so a directory that is not one is never mistaken for a savepoint, and `shared/` and `restic/` cannot collide with a savepoint of any age.

**The record is the commit.** `savepoint.json` is written after everything it names; a capture that died before it leaves a directory nothing lists and nothing puts back. That rule and its consequences are [the savepoint flow](../how_it_works.md#the-savepoint-flow).

## Object ids

Each volume in a savepoint is named by its `object_id`, in the output, in the logs and in the store. It says which volume it is: `volume-named-myapp_pgdata` is the named volume `myapp_pgdata`, and `volume-anon-redis-1-data` is the anonymous volume at `/data` of `redis` replica 1.

## Shared blobs

A container's writable layer is committed as an image and split as it is stored: every blob goes to `shared/oci/blobs/sha256/` under the digest its own name already carries, and only the archive's small metadata stays with the savepoint. The base layer a compose project's every savepoint carries is therefore written once, and `stored_bytes` against `size_bytes` in the record is where you see it.

The blob store is shared by every savepoint of one **compose project**, inside the session root. That keeps a session self-contained and dying whole, at the price of no sharing across sessions or compose projects.

Nothing collects those blobs. `savepoint remove` frees the savepoint's own directory and leaves the shared store alone, so removing every savepoint of a compose project frees no image bytes — see [Limitations](limitations.md#nothing-collects-the-shared-blobs).

## cdc repositories

The [`cdc` backend](configuration.md#snapshot) keeps one restic repository per volume under `restic/<object-id>/`, beside the savepoint directories rather than inside any one of them: a repository outlives every savepoint that wrote into it, which is what makes the second capture an increment. It dies with the session, and cannot be shared across sessions.

## Moving a savepoint

Everything about one savepoint is under its own directory, except the image blobs it shares. Copying a whole compose project's store root — savepoint directories, `shared/` and `restic/` together — moves every savepoint in it, and arcave finds them again as soon as `storage.directory.path`, `project.name` and `-p` point at the same three segments.

## Next step

[Requirements](requirements.md) — what the machine underneath has to provide. Nothing on this page asks anything of the filesystem.
