---
title: Limitations
description: What arcave does not do — downtime on every operation, no point-in-time recovery, no concurrent branches, storage cost of writable-layer capture, and platform-specific constraints.
keywords: [arcave limitations, known issues, downtime, point-in-time recovery, storage cost, virtiofs, docker compose, Open-Source, Greenmask]
---

# Limitations

Read this before you build a workflow on arcave. Every item here is a design consequence, not a bug waiting to be fixed.

## The stack is stopped for every operation

`savepoint create` and `rollback` both stop the project's services, move data, and start them again — then wait for health checks.

The window is proportional to the data being moved plus your services' startup time. On a large volume with a slow-starting stack it is minutes, not seconds.

This is the trade that makes "consistent" a claim arcave can actually make. A running database holds state in memory and in files it is midway through writing, and copying those files produces a snapshot that may not restore. Stopping first is what avoids needing a per-engine quiescing hook for Postgres, MySQL, Redis, Mongo and everything else a compose file might hold.

**arcave is therefore a development-machine tool.** It is not for production, and not for any environment where the stack must stay up.

## No point-in-time recovery

arcave captures volumes at rest. It does not read WAL, binlogs, or any transaction stream.

You can return to a savepoint you took. You cannot return to "11:42 this morning" unless you happened to take one then. There is no way to replay forward from a savepoint to a later moment.

## Branches are serial, not concurrent

There is one environment. Rolling back to a savepoint **replaces** the current state — it does not create a second stack running beside the first.

You cannot have two savepoints live and queryable at the same time, you cannot diff two savepoints, and nothing merges. If you need concurrent branches or instant switching, you want a database with real branching. See [Environment branching](use_cases/environment_branching.md#where-the-analogy-stops) for the full comparison.

## One workflow at a time

Every mutating workflow holds a non-blocking lock on `.arcave/lock`, bound to the project root. A second arcave run against the same project fails immediately rather than queuing:

```text
another arcave command is already running in this project
```

Two workflows interleaving stop, capture and start on one stack has no correct outcome. One agent, one CI job, one developer per project at a time.

## The stack must exist on the daemon

arcave reads the **running stack** from the Docker daemon and never opens a compose file. That is usually an advantage — move your files, delete them, generate them, nothing changes.

But the stack has to be there. `docker compose stop` is fine: stopped containers still describe the stack. **`docker compose down` is not** — it removes the containers, and with them everything arcave knows about the project. A command run against a torn-down stack fails saying so, and a savepoint taken before the teardown is still restorable only if you bring the stack back first.

## A missing named volume is an error

A named volume the project declares but the daemon does not have fails the capture rather than being skipped. An empty capture would silently wipe live data at the next rollback.

Run `docker compose up` once so the volumes the project declares actually exist.

## Anonymous volumes need their container

An anonymous volume's Docker name is generated per container, so arcave has to ask the daemon through that service's container. If the container is gone, the capture fails — there is no other way to address it.

## `subpath:` mounts are captured whole

A volume mounted through `subpath:` is captured **whole**. The subpath is a property of that one mount and the daemon does not report it back, so arcave — which reads the running stack — never sees one.

**A rollback therefore restores the whole volume**, not the subdirectory the service sees. Everything else in that volume goes back to its savepoint state too. If two services share one volume through different subpaths, rolling back for one rolls back for both.

## An external volume nothing mounts is invisible

arcave reads the running stack, and only compose's own volumes carry the project label that would reveal an unmounted external volume. An `external: true` volume that some service *does* mount is captured normally.

## Storage cost

**Writable-layer capture is not deduplicated.** Each savepoint stores the whole image of each container, base layers and all. `images[].size_bytes` in the record is what that costs, and it is charged per savepoint. Ten savepoints of a stack with a 300 MB application image cost 3 GB in layers alone, before any volume data.

**With the `fullcopy` backend, every savepoint costs full volume size.** The [`cdc` backend](configuration.md#snapshot-backends) fixes this for volumes — only changed chunks are written — but not for layers.

Bound your storage with [retention rules](commands/savepoint_remove.md).

## Storage writes are not atomic

There is no temp-file-and-rename commit protocol. The savepoint record is the commit marker, so a savepoint that failed mid-write is invisible — it is dropped whole and re-created, never repaired.

The consequence you can see: a failed capture leaves data behind. arcave logs it and leaves it for you rather than deleting it, because a failure path is the worst possible moment to start deleting data. A new savepoint whose directory is not empty is **refused**, not swept.

## arcave does not finish an interrupted rollback

If a rollback is killed mid-flight, arcave marks the phase and refuses the next mutating workflow. It does not resume, retry, or repair on its own. Recovery is explicit and yours to drive — see [Troubleshooting](troubleshooting.md#an-interrupted-rollback).

## Restored image tags accumulate

A rollback tags each restored writable layer `arcave/<project>-<service>-<replica>-<base-image>:sp-<savepoint-id>`. These are not collected: the running containers reference them, so nothing can be dropped at the end of the workflow. One tag per (service, replica, savepoint) survives each rollback.

A `docker compose up` run by hand afterwards recreates the containers from your own compose file and drops the restored layer with them.

## Savepoints without a compose spec cannot be rolled back to

A rollback rebuilds the stack from the compose spec the savepoint recorded. Savepoints taken before arcave recorded specs carry none, and a rollback to one is **refused** rather than guessed at.

## `cdc` and host bind mounts on macOS

With `snapshot.backend: cdc`, arcave cannot capture a **host bind mount** on Docker Desktop for macOS. restic sets `O_NOATIME` on each source file, which VirtioFS answers with `EIO` on the next read.

Named volumes live in the VM's own filesystem and are unaffected. Workarounds: switch that mount to a named volume, switch the file-sharing implementation to gRPC FUSE, or use the `fullcopy` backend for that project.

## `cdc` repositories cannot be shared

Each session's restic repositories live inside that session's own store root and die with it. That keeps a session self-contained, at the price of a repository that cannot be shared across sessions or projects.

## No storage backend but the local filesystem

`storage.type` is a tagged union with exactly one arm implemented: `directory`. There is no S3 or remote arm today.

## `arcave status` is not implemented

The command is registered and returns `not implemented`. It is in the tree to keep it honest about intent — do not build on it.

## Health checks are not optional in practice

arcave waits for health checks after every restart. A service without one is considered up the moment its container starts, so a savepoint may be taken of a database that has not finished recovering, and a rollback may report success before the stack is genuinely usable.

Define health checks on every stateful service. arcave does not warn you when one is missing.

## Related docs

* [Troubleshooting](troubleshooting.md) — recovering when something has already gone wrong.
* [How arcave works](how_it_works.md) — why these constraints exist.
* [Configuration](configuration.md) — the settings that mitigate some of them.
