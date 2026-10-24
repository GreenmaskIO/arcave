---
title: Storage layout
description: Where arcave writes — the store root and its segments, a session's and a savepoint's own directories, how a volume is named, the blobs and images savepoints share, and the host-local files under .arcave/.
keywords: [arcave storage layout, store root, object id, savepoint directory, deduplication, restic]
---

# Storage layout

Everything arcave stores lives under one root, computed from the [storage](configuration.md#storage) path — `storage.directory.path`, or the `prefix` of a bucket, a container or an SFTP directory:

```text
<path or prefix>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>/
```

Four things are named there, outermost first:

| Segment | What it is | What it accepts |
|---|---|---|
| `arcave` | The static namespace every store is rooted under. Not configurable. | — |
| `<project>` | The storage tenant, `project.name`. | Starts with `[a-z0-9]`, continues with `[a-z0-9_-]` |
| `<session>` | The session — the line of savepoints, and the lifetime everything below it dies with. | A canonical UUID; the default session `baseline` is `00000000-0000-0000-0000-000000000001` |
| `<compose-project>` | The compose project, named per invocation with `-p`. | Starts with `[a-z0-9]`, continues with `[a-z0-9_-]` |

The static `arcave` segment is what bounds every write and every delete: point the path at `/etc` and arcave operates in `/etc/arcave`. Each segment below it is checked against an allowlist rather than a denylist, because a list of what a path segment must *not* be is never provably complete.

`compose/` has a reserved sibling for a second timeline that is not built yet, which is why the compose project sits one level down rather than at the root.

## Inside a session

```text
sessions/<session>/
  session.json                                 the session's record — written last, deleted first
  heartbeat.json                               the state of the fork that wrote the tree
  compose/<compose-project>/                   one store root per compose project
```

A session directory without `session.json` is not a session: no `--session` finds it, and only `session list --all` shows it, with the status its `heartbeat.json` gives. The default session `baseline` has no record; it always exists. See [Sessions](../sessions.md).

## Inside a compose project's store root

```text
<compose-project root>/
  019e843e-1650-7000-8000-000000000000/                                  a savepoint, named by its id
    savepoint.json                             the record — written last, and it is the commit
    project.yaml                               the compose spec the project was captured under
    heartbeat.json                             the state of the capture that wrote the tree
    volume-named-myapp_pgdata/
      data.tar                                 a fullcopy capture of one volume
    images/app/replicas/1/                     one container's writable layer, dedupcopy
      index.json                               verbatim from the image archive
      manifest.json                            verbatim from the image archive
      oci-layout                               verbatim from the image archive
      image.json                               what to rebuild the layer from — written last
    images/worker/replicas/1/                  one container's writable layer, fullcopy
      image.tar                                the whole image archive
      image.json                               written last
  019e8488-3ac8-7000-8000-000000000000/                                  the next savepoint
  shared/oci/blobs/sha256/<digest>             the image blobs every savepoint here shares
  shared/oci/images/sha256/<hex>/              one image a service runs on, keyed by its id
  restic/volume-named-myapp_pgdata/            one cdc repository per volume
```

A savepoint id is a canonical UUID, so a directory that is not one is never mistaken for a savepoint, and `shared/` and `restic/` cannot collide with a savepoint of any age.

**The record is the commit.** `savepoint.json` is written after everything it names; a capture that died before it leaves a directory nothing lists and nothing puts back. Its `heartbeat.json` says whether that capture is still running or died, which is how [`arcave gc`](cli/gc.md) tells the two apart. That rule and its consequences are [the savepoint flow](../how_it_works.md#the-savepoint-flow).

## Object ids

Each volume in a savepoint is named by its `object_id`, in the output, in the logs and in the store. It says which volume it is:

| Object id | Volume |
|---|---|
| `volume-named-myapp_pgdata` | The named volume `myapp_pgdata`, whichever services mount it. |
| `volume-service-app-app-src` | The bind mount at `/app/src` of service `app`, shared by its replicas. |
| `volume-anon-redis-1-data` | The anonymous volume at `/data` of `redis` replica 1 — each replica has its own. |

The slashes of a mount target become dashes.

## Shared blobs

Under the default [`writable_layers.backend: dedupcopy`](configuration.md#writable_layers), a container's writable layer is committed as an image and split as it is stored: every blob goes to `shared/oci/blobs/sha256/` under the digest its own name already carries, and only the archive's small metadata stays with the savepoint. The base layer a compose project's every savepoint carries is therefore written once, and `stored_bytes` against `size_bytes` in the record is where you see it.

The images services run on — `postgres:16`, say — are kept the same way, always: each one's metadata under `shared/oci/images/sha256/<hex>/`, named by its image id, and its blobs in the same blob store. An image is written once for every savepoint and replica naming it, and a layer's `stored_bytes` is what it adds over that image. Under `writable_layers.backend: fullcopy` a layer is stored whole, as `image.tar`, and shares nothing.

The blob store is shared by every savepoint of one **compose project**, inside the session root. That keeps a session self-contained and dying whole, at the price of no sharing across sessions or compose projects.

Nothing collects those blobs. `savepoint remove` frees the savepoint's own directory and leaves the shared store alone, so removing every savepoint of a compose project frees no image bytes — see [Limitations](limitations.md#nothing-collects-the-shared-blobs).

## cdc repositories

The [`cdc` backend](configuration.md#snapshot) keeps one restic repository per volume under `restic/<object-id>/`, beside the savepoint directories rather than inside any one of them: a repository outlives every savepoint that wrote into it, which is what makes the second capture an increment. It dies with the session, and cannot be shared across sessions.

## Moving a savepoint

Everything about one savepoint is under its own directory, except the blobs and images it shares and its cdc repositories. Copying a whole compose project's store root — savepoint directories, `shared/` and `restic/` together — moves every savepoint in it, and arcave finds them again as soon as the storage path, `project.name` and `-p` point at the same three segments.

## On the host

Three files under the project's `.arcave/` belong to the machine, not to the store, and are never copied with it:

| File | What it is |
|---|---|
| `.arcave/lock` | The lock every command that changes something takes. |
| `.arcave/inflight.json` | The marker an interrupted rollback leaves; see [Troubleshooting](troubleshooting.md). |
| `.arcave/current_session` | The current session, set by `session set`, cleared by `session unset`. |

## Next step

[Requirements](requirements.md) — what the machine underneath has to provide. Nothing on this page asks anything of the filesystem.
