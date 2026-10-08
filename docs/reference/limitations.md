---
title: Limitations
description: What arcave does not do — downtime on every operation, no point-in-time recovery, one environment and one writer, uncollected blobs and tags, and the platform-specific constraints.
keywords: [arcave limitations, downtime, point-in-time recovery, storage cost, virtiofs, docker compose]
---

# Limitations

Each item here is a consequence of how arcave works, and each is stated once.

## The project is stopped for every operation

`savepoint create` and `rollback` both stop the services, move data, and start them again. The window is proportional to the bytes moved plus the project's own startup time: on a large volume with a slow-starting project, minutes rather than seconds.

Stopping is what makes the result application-consistent, and it is why arcave is a development-machine tool. It is not for production, or for any environment that has to stay up.

## A `stop_timeout` that is too short captures a crash state, and reports success

arcave captures the volumes the moment the containers are down. A container that did not finish shutting down inside [`compose.stop_timeout`](configuration.md#compose) is killed, and what its volume holds at that instant is what the savepoint carries — a state a database has to recover from on its next start. The command reports success, because from arcave's side nothing failed. On a large cluster, raise the bound.

## No point-in-time recovery

arcave captures volumes at rest. It reads no WAL, no binlog, no transaction stream. You can return to a savepoint you took; you cannot return to 11:42 this morning unless you happened to take one then, and nothing replays forward from a savepoint to a later moment.

## One environment, and one at a time

There is one compose project. A rollback replaces its state rather than putting a second one beside it. Two savepoints cannot be live and queryable at once, nothing diffs two savepoints, and nothing merges them.

## One writer per project

Every mutating workflow takes a non-blocking lock on `.arcave/lock`. A second run against the same project fails at once rather than queuing:

```text
another arcave command is already running in this project
```

The lock is host-local: it serialises processes on this machine, and does not protect a store two machines share over a network.

## What a rollback returns, and what it cannot

A rollback returns volumes, container writable layers and the compose spec the savepoint recorded. It returns a **fact**, not a working system: it puts the bytes back, and whether the services then behave is up to them.

Nothing outside the project moves. A cloud database, a webhook already delivered, a message already consumed by something outside compose — none of it rolls back with the volumes.

## Some compose settings do not survive a rollback

arcave rebuilds the project from what Docker reports, not from your compose file. Docker does not keep everything a compose file can say, so the rebuilt project differs from it. Your compose file itself is never changed. After a rollback:

* `build`, `profiles`, `develop` and `x-` keys are not applied; the image that was built keeps running.
* `post_start` and `pre_stop` hooks do not run.
* `env_file` is not applied; the variables it set are kept.
* An optional `depends_on` (`required: false`) becomes required.
* A container gets a new MAC address.
* A network's subnet may change, unless a service has a fixed address in it.

`volumes_from: [x:ro]` is writable before any rollback too: compose drops `:ro` before the mount reaches Docker.

## A `subpath:` mount is captured and put back whole

The unit is the volume, not the mount. A service that sees only a subdirectory through `subpath:` still has the whole volume captured, and a rollback puts the whole volume back — including everything another service reaches through a different subpath of it.

## An anonymous volume needs its container

An anonymous volume's docker name is generated per container, so arcave reaches it through the service's container and nowhere else. If the container is gone, the capture fails. A named volume is addressable directly and survives container removal.

## An unmounted external volume is invisible

arcave finds a project's volumes by compose's own labels. An `external: true` volume that no service mounts carries none of them and is not captured. One that a service does mount is captured normally.

## Nothing collects the shared blobs

Image blobs are shared by every savepoint of a compose project. `savepoint remove` frees that savepoint's own directory and leaves the shared store alone, so deleting every savepoint of a compose project frees no image bytes. Removing that store root by hand is what reclaims them today. See [Storage layout](storage_layout.md#shared-blobs).

## Nothing collects what a rollback leaves in the Docker daemon

A rollback tags each restored layer `arcave/<compose-project>-<service>-<replica>-<base image>:sp-<savepoint id>`. The running containers reference those tags, so nothing can drop them when the workflow ends: one tag per service, replica and savepoint survives each rollback. A `docker compose up` run by hand afterwards recreates the containers from your own compose file and drops the restored layer with them.

## `fullcopy` costs full volume size every time

The default backend writes one archive per volume per savepoint. The [`cdc` backend](configuration.md#snapshot) writes only the chunks that changed, which is what to switch to when volumes are large or savepoints are frequent. Writable layers are deduplicated under either backend.

## There is no write-and-rename commit protocol

The record is the commit marker, so a savepoint that failed mid-write is invisible and is dropped whole and taken again, never repaired. What the failed run wrote stays where it is, named in the log: a failure path is the worst moment to start deleting data. The next capture that lands on the same directory refuses it rather than sweeping it.

## arcave does not finish an interrupted rollback

A rollback killed mid-flight leaves a marker recording the phase it died in, and the next mutating command is refused. arcave does not resume, retry or repair on its own — recovery is explicit and yours to drive, and the remedy is in [Troubleshooting](troubleshooting.md#an-interrupted-rollback).

## A savepoint with no compose spec cannot be rolled back to

The project is rebuilt from the compose spec the savepoint recorded. Savepoints taken before arcave recorded one carry none, and a rollback to one is refused rather than guessed at. Their volume data is still on disk; there is no supported path that puts it back.

## Five volumes at a time

A capture, a restore and a removal each work at most five volumes concurrently. The bound is not configurable.

## A service without a health check counts as up

arcave waits for health checks after every restart. A service that declares none, or sets `healthcheck: {disable: true}`, is considered up the moment its container starts, so a savepoint may be taken of a database that has not finished coming up, and a rollback may report success before the project is genuinely usable. Declare a health check on every stateful service; arcave does not warn when one is missing.

## `cdc` and host bind mounts on macOS

With `snapshot.backend: cdc`, a host bind mount cannot be captured on Docker Desktop for macOS: restic opens each source file with `O_NOATIME`, and VirtioFS answers the next read with `EIO`. Named volumes live in the VM's own filesystem and are unaffected. Three ways out: make that mount a named volume, switch Docker Desktop's file sharing to gRPC FUSE, or use `fullcopy` for that project.

## `cdc` repositories cannot be shared

Each session's restic repositories live inside that session's own store root and die with it, which is what keeps a session self-contained. They cannot be shared across sessions or projects.

## The local filesystem is the only store

`storage.type` is a union with one arm implemented: `directory`. Two machines can share a store through a filesystem they both mount, with the one-writer rule above as the only thing keeping them apart.

## Next step

[Troubleshooting](troubleshooting.md) — recovering when something has already gone wrong.
