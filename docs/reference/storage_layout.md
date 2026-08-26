---
title: Storage layout
description: Where arcave writes — the store root and its segments, the object-id grammar a volume is keyed by, a savepoint's own directory, and the blobs savepoints share.
keywords: [arcave storage layout, store root, object id, savepoint directory, deduplication, restic]
---

# Storage layout

Everything arcave stores lives under one root, computed from [`storage.directory.path`](configuration.md#storage):

```text
<storage.directory.path>/arcave/projects/<project>/sessions/global/compose/<compose-project>/
```

Four things are named there, outermost first:

| Segment | What it is | What it accepts |
|---|---|---|
| `arcave` | The static namespace every store is rooted under. Not configurable. | — |
| `<project>` | The storage tenant, `project.name`. | Starts with `[a-z0-9]`, continues with `[a-z0-9_-]` |
| `global` | The session — the lifetime everything below it dies with. There is one today. | `global`, or a canonical UUID |
| `<compose-project>` | The compose project, named per invocation with `-p`. | Starts with `[a-z0-9]`, continues with `[a-z0-9_-]` |

The static `arcave` segment is what bounds every write and every delete: point the path at `/etc` and arcave operates in `/etc/arcave`. Each segment below it is checked against an allowlist rather than a denylist, because a list of what a path segment must *not* be is never provably complete.

`compose/` has a reserved sibling for a second timeline that is not built yet, which is why the compose project sits one level down rather than at the root.

## Inside a compose project's store root

```text
<compose-project root>/
  1780335122/                                  a savepoint, named by its id
    savepoint.json                             the record — written last, and it is the commit
    project.yaml                               the compose spec the project was captured under
    volume-named-myapp_pgdata/
      data.tar                                 a fullcopy capture of one volume
    images/app/replicas/1/
      index.json                               verbatim from the image archive
      manifest.json                            verbatim from the image archive
      oci-layout                               verbatim from the image archive
      image.json                               what to rebuild the layer from — written last
  1780339981/                                  the next savepoint
  shared/oci/blobs/sha256/<digest>             the image blobs every savepoint here shares
  restic/volume-named-myapp_pgdata/            one cdc repository per volume
```

A savepoint id is a run of decimal digits — the Unix second the capture started — so a directory that is not one is never mistaken for a savepoint, and `shared/` and `restic/` cannot collide with a savepoint of any age.

**The record is the commit.** `savepoint.json` is written after everything it names; a capture that died before it leaves a directory nothing lists and nothing puts back. That rule and its consequences are [`concepts/savepoint_lifecycle.md`](../concepts/savepoint_lifecycle.md).

## Object ids

Every volume a savepoint covers is keyed by an **object id**: one path segment, and the same string in the record's `stats[].volume.object_id`, in the store, and in a cdc repository's name.

| Volume | Id | Example |
|---|---|---|
| Named | `volume-named-<docker volume name>` | `volume-named-myapp_pgdata` |
| Bind or anonymous | `volume-service-<service>-<mount path>`, the path's leading `/` dropped and its remaining `/` turned into `-` | `volume-service-app-var-lib-data` |

Both open with `volume-`, so no volume can collide with the reserved `images`, `shared` or `restic` keys. The second word says how the volume is addressed, which keeps the identity spaces apart: a volume called `db` and a service called `db` do not produce the same id.

A named volume is keyed by its **docker** name, not its key in the compose file — `pgdata` in project `myapp` is `myapp_pgdata`, and an `external: true` volume keeps whatever name it was given. That is also why renaming a volume produces a new identity, deliberately: docker creates an empty volume under the new name, and an old capture must not map onto it.

Because a mount path is flattened into the id, two different mounts can render the same one — `{app, /var/lib}` meets `{app-var, /lib}`. arcave refuses that rather than merging them:

```text
two volumes address the same object id: service "app" at "/var/lib" and service "app-var" at "/lib" both address "volume-service-app-var-lib"; rename one of them
```

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
