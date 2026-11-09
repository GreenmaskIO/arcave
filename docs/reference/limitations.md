---
title: Limitations
description: What arcave does not do — downtime on every operation, no point-in-time recovery, one environment and one writer per session, cleanup in more than one gc run, and the platform-specific constraints.
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

Only one command that changes something — `savepoint create`, `rollback`, `savepoint remove`, `arcave gc`, `session create`, `remove`, `set` or `unset` — runs in a project at a time. A second one on the same machine fails right away rather than waiting:

```text
another arcave command is already running in this project
```

Commands that only read — `savepoint list`, `session list`, `session current`, `savepoint remove --dry-run` — always run.

Machines sharing one store take turns per session: a command that finds the session held by another machine waits up to 60 seconds, then fails naming the holder. See [One session at a time across hosts](../how_it_works.md#one-session-at-a-time-across-hosts). The one thing not to run on a shared store while another machine works in the same session is a deleting `arcave gc`, see [gc on a shared store](#gc-on-a-shared-store).

## A rollback puts back the project, not the outside world

A rollback restores volumes, container files and the compose setup. Anything outside the compose project stays as it is: a cloud database, a webhook already sent, a message another system already consumed. Whether your services behave correctly after the rollback is up to them.

## Some compose settings do not survive a rollback

arcave rebuilds the project from what Docker reports, and Docker doesn't keep everything a compose file says. Your compose file itself is never changed. After a rollback:

* `build`, `profiles`, `develop` and `x-` keys are not applied; the image that was already built keeps running.
* `pre_start`, `post_start` and `pre_stop` hooks do not run.
* `env_file` is not applied; the variables it set are kept.
* An optional `depends_on` (`required: false`) becomes required.
* A container gets a new MAC address.
* A network's subnet may change, unless a service has a fixed address in it.
* A named volume whose driver, options or labels changed since the savepoint is recreated with the savepoint's settings when the savepoint holds its data. One the savepoint does not hold keeps its current settings and data, with a warning naming it.
* On Podman, what it does not store is not put back: `sysctls`, a fixed `ipv4_address`, `domainname`, a volume's `nocopy`, the `max-file` of a log, and a `cap_drop` of a capability Podman never grants are dropped. A long-syntax bind declaring no propagation comes back with `propagation: private`, Podman's default for it, and a long-syntax `tmpfs` comes back in the `tmpfs:` shorthand. A tag beside a digest (`postgres:16@sha256:…`) comes back as the digest alone, and a port published without a host port comes back fixed to the one Podman picked. What Podman set itself and reports as if declared — `oom_score_adj` and the `nofile`/`nproc` limits — is recorded as is.
* Podman refuses `links:` when it creates a container (`link is not supported`), so a project declaring them does not run on it.
* Nothing is guaranteed to work between Docker and Podman: Docker Engine, Docker Desktop, Colima and OrbStack are one side, Podman the other. arcave converts nothing and checks nothing. The spec it assembles on one carries defaults the other refuses or spells differently, so a rollback across can fail after the project has stopped, with compose's own error.

Running `docker compose up` by hand after a rollback recreates the containers from your compose file, which drops the container files the rollback restored. Volume data stays. [`arcave status`](cli/status.md) says which containers were recreated; once every one was, nothing on the daemon remembers the rollback, and it reports the compose project as not deployed by arcave.

## A deployment lives in the daemon

A rollback labels the containers it builds, and `status` reads what is deployed off those labels. They live in the daemon the rollback ran against: another daemon (`DOCKER_HOST`) holds none. They are plain text there, the session's and the savepoint's names included, under `encryption.enabled` too: anyone who can `docker inspect` the containers reads them.

The inflight marker an interrupted rollback leaves, `.arcave/inflight.json`, is local to the machine that ran the rollback. `status` on another machine, or against another daemon, does not see it: an interrupted rollback reads there as `absent` or `degraded`, not `rollback-pending`.

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
* **An interrupted prune.** If a `cdc` repository's `restic forget --prune` is cut short after the snapshots are forgotten, the next gc prunes that repository again.

`arcave session remove` frees everything a session holds, `cdc` repositories included; `arcave session list --all` lists the trees a failed fork left, and `session remove <id>` deletes one. The default `baseline` session can't be deleted: once you no longer need any of its savepoints, `arcave session remove baseline --force` empties it.

### gc on a shared store

The session's lock in the store is taken by writing it, waiting a second, and reading it back, because the stores arcave supports offer no write-if-absent. Two machines claiming it at the same moment can, rarely, both believe they hold it. For a capture that only costs a savepoint, but a gc that deletes could then remove a blob a capture on the other machine has just reused, and that savepoint would fail to roll back. Run `arcave gc` without `--dry-run` only while no other machine works in the session. `--dry-run` is safe anywhere.

### Safety snapshots pile up between gc runs

Every rollback of a running project leaves a safety snapshot, and only a run of `arcave gc` retires them: nothing runs it for you. On `fullcopy`, each one is a full copy of the volumes until then. Run gc periodically, or right after a rollback, or turn them off with `rollback.safety_snapshot: false`.

A rollback that failed and reverted, or was interrupted before the teardown, leaves one too, and it counts toward the retention. Under `retain_recent: 1`, a gc after such a rollback retires the undo of the last rollback that did finish.

### A rollback without a safety snapshot goes by the savepoint's spec

`--no-safety-snapshot` reads nothing of the live project, so `compose down` knows only what the savepoint recorded. Containers go whatever the spec says, but a network the live project declares and the savepoint doesn't stays in Docker, and the next savepoint records it. A service is stopped within the savepoint's `stop_grace_period`, not the live one. Remove such a network with `docker network rm`.

### gc on another machine doesn't see an interrupted rollback

The marker a rollback leaves when it is interrupted, `.arcave/inflight.json`, is local to the machine it ran on. A gc there refuses to delete while it stands; a gc on another machine sharing the store doesn't see it, and may retire a safety snapshot that interrupted rollback still needs, its revert having failed included. After an interrupted rollback, recover before running gc from elsewhere.

### gc is not kept out of a rollback's window from elsewhere

A rollback loads its layers into the daemon and tags them before it creates the containers that run on them. The project lock keeps a gc in the same checkout out of that window, and the session's lock one in the same session on any host sharing the store. A gc of the same arcave project on the same daemon, in another session or over another store, is kept out by neither: it removes the tags, and the rollback fails when it creates or rebuilds its containers and goes back to its safety snapshot, or takes down what it created. No data is lost: the layers stay in the store, and running the rollback again passes. Run `arcave gc` while no rollback runs on the daemon from another checkout or session.

### Some layer tags gc never judges

Every rollback leaves an `arcave/…:sp-<savepoint id>` image tag in the daemon, and `arcave gc` removes it once no container uses it. gc only judges the tags of compose projects its session holds: a tag of a compose project no session holds any more, or one committed by an earlier arcave version, stays. List the tags with `docker image ls --filter 'reference=arcave/*'` and remove those by hand.

## An interrupted rollback is not finished for you

If a rollback is killed partway through, arcave refuses every command that changes state until you recover. It doesn't resume on its own. A rollback that took no safety snapshot — of a project that had no containers, or under `--no-safety-snapshot` — has none to go back to: you recover by running the same rollback again, or one to another savepoint. [Troubleshooting](troubleshooting.md#an-interrupted-rollback) shows how to recover.

## A failed rollback can leave anonymous volumes behind

When a rollback fails after building the project and arcave puts the previous state back, the anonymous volumes of the containers it built and then removed stay in Docker. They hold nothing the project needs; remove them by name with `docker volume rm`.

## A fork copies the whole session

`session create --from` copies everything the source session holds, so it costs as much space and time as that session's `size_bytes`. Every other command that changes something in the project is refused until the copy is done; commands that only read still run.

## Podman's image blobs are keyed by their compressed digest

On Podman, arcave stores an image's layers as the compressed blobs Podman exports, and deduplicates them by that digest. A Podman release that compresses differently writes them again once; nothing is lost, the image id stays the same.

## `cdc` and host bind mounts on macOS

On Docker Desktop for macOS with VirtioFS file sharing, the `cdc` backend can fail on a host bind mount (VirtioFS returns `EIO`). Named volumes are fine. Use a named volume, switch Docker Desktop's file sharing to gRPC FUSE, or use `fullcopy` for that project.

## Encryption hides contents, not shape

[`encryption`](configuration.md#encryption) keeps whoever reads the store — the provider, a leaked read-only bucket key, a shared SFTP account — from reading or silently altering any object. It does not hide:

- **Names**: savepoint and session ids, volume object ids (`volume-named-myapp_pgdata`), compose project names, image digests and restic pack names.
- **Shape**: object sizes, give or take the encryption overhead, and how many objects there are and when they were written.
- **Anything from whoever can write**: they can delete objects, or put one encrypted object under another's name.
- **The host**: the key in `.arcave/config.yaml` when it is written there literally, the process environment, and the images in the Docker daemon — a writable layer holding a secret is encrypted in the store, not in the daemon.

A lost key cannot be recovered, and an unencrypted arcave project is not encrypted in place: switching encryption on over one that holds data is refused, by reading and writing commands alike, and leaves it as it was. Encryption starts on an empty arcave project; moving savepoints across is [by hand](troubleshooting.md#turning-encryption-on-for-a-project-with-data).

## SFTP storage does not check host keys

The `ssh` storage doesn't verify the server's host key. Use it only on a network you trust.

## Next step

[Troubleshooting](troubleshooting.md) — recovering when something has already gone wrong.
