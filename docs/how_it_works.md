---
title: How arcave works
description: The arcave savepoint and rollback mechanism end to end — what a savepoint covers, why the stack is stopped, how the record commits, and what the automatic safety snapshot guarantees.
keywords: [arcave architecture, savepoint mechanism, docker volume snapshot, rollback, safety snapshot, docker compose, Open-Source, Greenmask]
---

# How arcave works

arcave sits between you and the Docker daemon. It does not install an agent, patch a filesystem, or ask for a copy-on-write volume driver. It stops the stack, moves the bytes through the Docker API, and starts the stack again.

## The stack is stopped, on purpose

Every arcave workflow stops the project's services before it reads or writes a byte, and starts them again afterwards.

This is the design's central trade. A running database holds state in memory and in files it is midway through writing; copying those files produces a snapshot that may not restore. Stopping the containers first is what makes "consistent" a claim arcave can actually make, without a per-engine hook for Postgres, MySQL, Redis, Mongo and everything else a compose file might hold.

The cost is downtime, proportional to the data being moved plus the time your services take to become healthy again.

:::note
arcave uses `docker compose stop`, never `down`. Containers are stopped, not removed, and the same containers start again — nothing is recreated and nothing on the host is touched beyond the volume data being captured.
:::

Two bounds control that window, and both are configurable: [`compose.stop_timeout`](configuration.md#compose-section) is how long a container gets to exit on its own before it is killed, and [`compose.wait_timeout`](configuration.md#compose-section) is how long arcave waits for health checks after the restart. The stop timeout matters more here than it does to plain `docker compose stop`: arcave captures the volumes the moment the containers are down, so a database killed mid-shutdown is captured mid-shutdown.

## The two halves of a savepoint

A savepoint captures two things, and they never overlap.

**The volumes.** Every persistent mount in the project, addressed as a volume rather than as a mount path.

**The writable layer.** Every container's writable layer, committed as an image. `docker commit` excludes the contents of every mount by contract, so the layer capture sees exactly what the volume capture cannot: whatever a service wrote past all of its mounts — a file dropped in `/tmp`, a package installed at runtime, a config the entrypoint rewrote in place.

Data belongs in volumes. The layer capture is what keeps a stack honest when it is not there.

:::warning
The layer capture is not deduplicated. Each savepoint stores the whole image of each container, base layers included. `images[].size_bytes` in the savepoint record is what that costs — see [Limitations](limitations.md#storage-cost).
:::

## What a savepoint covers

The unit of backup is the **volume**, not the mount:

* **Every named volume the daemon reports for the project** — including ones no service mounts, and including `external: true` volumes that some service does mount.
* **Every bind mount and every anonymous volume**, one unit per service and target.
* A named volume that three services mount is **one** unit, captured once.

Mounts that are not persistent data are skipped, and each skip is reported with its reason: `tmpfs`, `npipe`, `image` and `cluster`.

A volume is addressed directly — a named volume by its Docker name, a bind mount by its host path — and read and written through a throwaway container of arcave's own. The project's containers need only *exist*; they do not need to be running.

The one exception is an **anonymous volume**. Its Docker name is generated per container, so arcave has to ask the daemon through that service's container, and a capture fails if the container is gone.

:::warning
A named volume the project declares but the daemon does not have is an **error**, not a skip. An empty capture would silently wipe live data at the next rollback. Bring the project up once (`docker compose up`) so the volumes it declares actually exist.
:::

An external volume that nothing mounts is invisible to arcave: it reads the running stack, and only compose's own volumes carry the project label that would reveal it.

## The record is the commit marker

Each savepoint is one self-contained directory under the store root, named by its id:

```text title="One savepoint's namespace"
<savepoint-id>/
├── savepoint.json                            # the record — written LAST
├── project.yaml                              # the stack's compose spec
├── <object-id>/data.tar                      # one archive per volume (fullcopy)
└── images/<service>/<replica>/image.tar      # the container's writable layer
```

`savepoint.json` is written **last**, after every archive beside it is complete, and deleted **first** when a savepoint is removed. That single ordering rule is what makes the whole thing safe to interrupt:

* A run killed before the record is written leaves data that no record names. `arcave savepoint list` does not show it, and nothing will ever restore it.
* A removal killed after the record is deleted leaves data that no record names — the same harmless state.
* There is **no** window in which a savepoint lists without its data behind it.

The consequence worth internalising: **on any failure the record is zero**. A half-finished capture is garbage no caller may address, so arcave does not repair it, does not resume it, and does not delete it either. It is logged, named, and left for you — a failure path is the worst possible moment to start deleting data.

:::note
If a new savepoint's directory is not empty, arcave **refuses** the create rather than sweeping it. Only an interrupted run can have left objects there, and those objects are the last copy of whatever that run captured. They get named, not deleted.
:::

## The savepoint flow

```text title="arcave savepoint create before-migration"
1.  Claim the project lock
2.  Read the running stack from the daemon
3.  Stop the services
4.  Capture every volume
5.  Commit each container's writable layer
6.  Write project.yaml — the stack's compose spec
7.  Write savepoint.json — the commit marker
8.  Start the services
9.  Wait for health checks
```

## What a rollback restores

A rollback restores **the stack the savepoint recorded**, not the one that is running. A service added since is gone. One removed comes back. A changed image, port, environment variable or healthcheck goes back to what it was. That is why `project.yaml` is committed beside the record: it is what the stack is rebuilt from.

```text title="arcave rollback before-migration"
 1.  Claim the project lock
 2.  Resolve the savepoint record
 3.  Refuse if an interrupted rollback is marked
 4.  Build both backend sets — the savepoint's and the live one
 5.  Load and parse the savepoint's compose spec
 6.  Load the stored writable layers into the daemon
 7.  Stop the services
 8.  Safety snapshot of the live state
 9.  Write the inflight marker
10.  Remove containers and networks — volumes are kept
11.  Recreate the stack from the spec, on the restored images
12.  Wipe and restore every volume
13.  Start the services and wait for health checks
14.  Clear the marker, drop the safety snapshot
```

Steps 4 through 6 all run **while the project is still up**, and that is the point of them:

* A backend that cannot be constructed — a missing volume, an unreachable store — fails while the project is still running.
* A savepoint whose compose spec will not load fails before anything is torn down: a rollback that cannot say what to rebuild must not destroy what is there.
* An image archive the daemon will not accept fails there too.

Volumes are **kept** when the containers are removed: a volume arcave did not capture has no copy anywhere, so deleting it would be unrecoverable.

## The safety snapshot

Before a rollback overwrites anything, it takes an ordinary savepoint of the live state. It is not a separate tier or a separate store — it is a normal savepoint carrying the label `arcave-safety-snapshot`, named `safety-snapshot-<id of the savepoint being restored>`, hidden from `arcave savepoint list` unless you pass [`--include-safety-snapshots`](commands/savepoint_list.md), and rollback-able by id like any other.

If any step past that point fails — a restore error, or services that never become healthy — arcave replays the safety snapshot through the very same backend instances that took it, restarts the services, and reports the original error.

:::note
The revert runs on a context that ignores cancellation, with its own 10-minute budget. The Ctrl-C that aborted your rollback must not tear down the revert that is trying to save your environment.
:::

The safety snapshot is deleted when the rollback succeeds, and when a revert succeeds. It is **kept** when the revert itself fails — at that point it is the last copy of your pre-rollback state, and its id is printed.

With the `cdc` backend the safety capture is an increment in the same per-volume repository, on the same host and the same path, which is exactly what restic picks a parent by. The undo therefore costs almost nothing.

## The interrupted-rollback marker

A rollback writes `.arcave/inflight.json` before its first destructive write — the teardown — and clears it once the environment is consistent again. The marker carries the phase it died in, because the remedy differs, along with the savepoint being restored and the safety snapshot that undoes it.

| Phase | How far it got |
|---|---|
| `recreating` | From the teardown until the containers exist again. The volumes are still untouched. |
| `restoring` | Restoring volumes. The environment is partially overwritten. |
| `restore-done` | Volumes restored, but the stack never came up cleanly. |
| `inconsistent` | The automatic revert itself failed. The safety snapshot is retained. |

While a marker stands, the next mutating workflow is **refused** — with one exception: a rollback to that marker's own safety snapshot, which is the remedy, and is allowed through.

The marker is cleared *before* the safety snapshot is dropped, so a failed clear can never leave a marker pointing at a snapshot that no longer exists.

arcave does not finish a rollback it died in the middle of. Recovery is explicit and yours to drive — see [Troubleshooting](troubleshooting.md#an-interrupted-rollback).

## One project at a time

Every mutating workflow holds a non-blocking file lock on `.arcave/lock`, bound to the project root. A second arcave run against the same project fails immediately rather than queuing behind the first. Two workflows interleaving stop, capture and start on one stack has no correct outcome.

## Where the data lives

One store holds everything, rooted by the configured path:

```text title="Store root"
<storage.directory.path>/arcave/projects/<project>/sessions/global/compose/<stack>/
```

The `arcave` segment is not configurable, and it is the whole safety story: point the path at `/etc` and arcave operates in `/etc/arcave`, never in `/etc`. Every segment below it is validated against an allowlist rather than a denylist, because a list of what a path segment must *not* be is never provably complete.

`<project>` is the storage tenant, from `project.name` in the config. `<stack>` is the compose project you named with `-p`. They are different concepts — see [Concepts](concepts.md#project-versus-stack).

## Related docs

* [Concepts](concepts.md) — the vocabulary used above.
* [Getting started](getting_started.md) — run the flow yourself.
* [Configuration](configuration.md) — the bounds and backends named here.
* [Limitations](limitations.md) — where this design stops.
