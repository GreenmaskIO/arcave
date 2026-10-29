---
title: Limitations
description: What arcave does not do — downtime on every operation, no point-in-time recovery, one environment and one writer, manual cleanup in the alpha, and the platform-specific constraints.
keywords: [arcave limitations, downtime, point-in-time recovery, storage cost, virtiofs, docker compose]
---

# Limitations

Read this before you build a workflow on arcave. For what arcave needs to run, see [Requirements](requirements.md).

## The project is stopped for every operation

`savepoint create` and `rollback` stop the services, move the data, and start them again. Expect seconds on a small project, and minutes when the volumes are large or the services are slow to start. Stopping is what keeps the data consistent. It is also why arcave suits environments that are allowed to go down, and not production.

## A slow shutdown can be captured mid-crash

A container that doesn't shut down within [`compose.stop_timeout`](configuration.md#compose), or within its own `stop_grace_period` if that is longer, is killed. Its volume is then captured as it was at the moment it was killed, and a database will have to recover from that state on its next start. The savepoint still reports success. The killed container is named in a warning and in the savepoint's `not_covered.killed_containers`. Give slow services a long enough `stop_grace_period`.

## A service without a health check counts as up

arcave waits for health checks after every restart. A service without one counts as up as soon as its container starts. A savepoint may then capture a database that hasn't finished starting, and a rollback may report success before the project is usable. Give every stateful service a health check. arcave doesn't warn when one is missing.

## No point-in-time recovery

You can go back to a savepoint you took. You can't go back to an arbitrary moment in between: arcave reads no WAL or binlog and replays nothing.

## One environment at a time

A rollback replaces the project's state rather than starting a second copy beside it. Two savepoints can't run side by side, and arcave can't diff or merge them.

## One writer per project

Only one command that changes something — `savepoint create`, `rollback`, `savepoint remove`, `session create`, `remove`, `set` or `unset` — runs in a project at a time. A second one fails right away rather than waiting:

```text
another arcave command is already running in this project
```

Commands that only read — `savepoint list`, `session list`, `session current`, `savepoint remove --dry-run` — always run. This guard works on one machine only. Don't point two machines at the same store.

## A rollback puts back the project, not the outside world

A rollback restores volumes, container files and the compose setup. Anything outside the compose project stays as it is: a cloud database, a webhook already sent, a message another system already consumed. Whether your services behave correctly after the rollback is up to them.

## Some compose settings do not survive a rollback

arcave rebuilds the project from what Docker reports, and Docker doesn't keep everything a compose file says. Your compose file itself is never changed. After a rollback:

* `build`, `profiles`, `develop` and `x-` keys are not applied; the image that was already built keeps running.
* `post_start` and `pre_stop` hooks do not run.
* `env_file` is not applied; the variables it set are kept.
* An optional `depends_on` (`required: false`) becomes required.
* A container gets a new MAC address.
* A network's subnet may change, unless a service has a fixed address in it.

Running `docker compose up` by hand after a rollback recreates the containers from your compose file, which drops the container files the rollback restored. Volume data stays.

## Some volumes are captured differently than you might expect

* **`subpath:` mounts.** The whole volume is captured and restored, not only the subdirectory a service mounts.
* **Anonymous volumes.** They can only be reached through their container. If the container was removed, the capture fails. A named volume doesn't have this problem.
* **Unmounted external volumes.** An `external: true` volume that no service mounts isn't captured.

## A savepoint holds only what its labels and config let it

[Capture labels](capture_labels.md) and the config's [`capture`](configuration.md#capture) section can leave volumes, container files and images out of a savepoint. A rollback can't put back what was left out: an excluded anonymous volume comes back empty, and an excluded named volume or bind keeps what it holds at the time. The savepoint lists what it left out in `not_covered`.

A rollback doesn't load the images a savepoint stored back into Docker. A service whose container files were left out is rebuilt from its `image:`, so that image must still be in Docker or in a registry. If it is in neither, the rollback is refused before anything stops.

## gc frees space in more than one run

[`arcave gc`](cli/gc.md) deletes dead savepoint trees, the images and blobs no savepoint uses, the `cdc` snapshots of removed savepoints, and the layer tags no container uses. `arcave gc --dry-run` reports without deleting anything. Some space takes more than one run to come back:

* **An unused image.** Its layers go on the run after the image.
* **A recent layer tag.** A tag younger than `gc.restored_layer_grace`, an hour by default, waits for a later run.
* **An interrupted prune.** If a `cdc` repository's `restic forget --prune` is cut short after the snapshots are forgotten, the next gc prunes that repository again.

`arcave session remove` frees everything a session holds, `cdc` repositories included; `arcave session list --all` lists the trees a failed fork left, and `session remove <id>` deletes one. The default `baseline` session can't be deleted: once you no longer need any of its savepoints, `arcave session remove baseline --force` empties it.

### gc on a shared store

The session's lock in the store is taken by writing it, waiting a second, and reading it back, because the stores arcave supports offer no write-if-absent. Two machines claiming it at the same moment can, rarely, both believe they hold it. For a capture that only costs a savepoint, but a gc that deletes could then remove a blob a capture on the other machine has just reused, and that savepoint would fail to roll back. Run `arcave gc` without `--dry-run` only while no other machine works in the session. `--dry-run` is safe anywhere.

### Some layer tags gc never judges

Every rollback leaves an `arcave/…:sp-<savepoint id>` image tag in Docker, and `arcave gc` removes it once no container uses it. gc only judges the tags of compose projects its session holds: a tag of a compose project no session holds any more, or one committed by an earlier arcave version, stays. List the tags with `docker image ls --filter 'reference=arcave/*'` and remove those by hand.

## An interrupted rollback is not finished for you

If a rollback is killed partway through, arcave refuses every command that changes state until you recover. It doesn't resume on its own. A rollback of a project that had no containers has no safety snapshot to go back to: you recover by running the same rollback again. [Troubleshooting](troubleshooting.md#an-interrupted-rollback) shows how to recover.

## A fork copies the whole session

`session create --from` copies everything the source session holds, so it costs as much space and time as that session's `size_bytes`. Every other command that changes something in the project is refused until the copy is done; commands that only read still run.

## `cdc` and host bind mounts on macOS

On Docker Desktop for macOS, the `cdc` backend can't capture a host bind mount (VirtioFS returns `EIO`). Named volumes are fine. Use a named volume, switch Docker Desktop's file sharing to gRPC FUSE, or use `fullcopy` for that project.

## SFTP storage does not check host keys

The `ssh` storage doesn't verify the server's host key. Use it only on a network you trust.

## Next step

[Troubleshooting](troubleshooting.md) — recovering when something has already gone wrong.
