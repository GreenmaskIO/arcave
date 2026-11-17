---
title: Capture modes
description: How a savepoint holds each service still while it copies the data — stop, pause or live — what each one guarantees, the pre-capture and post-capture hooks that prepare an application for it, and how arcave brings a frozen or half-held compose project back to running.
keywords: [arcave capture mode, docker pause, crash-consistent, savepoint without downtime, arcave.capture.mode, arcave.hook.pre-capture, capture hooks, postgres checkpoint, docker compose]
---

# Capture modes

A savepoint copies volumes while nothing writes to them. By default arcave gets there by stopping the services, which costs a restart: connections drop, caches go cold. The capture mode chooses how each service is held still instead.

| Mode | What happens to the service | What the copy is |
|---|---|---|
| `stop` (default) | Stopped before the capture, started after it. | Consistent: the service shut down cleanly. |
| `pause` | Frozen with `docker pause` before the capture, resumed after it. | Crash-consistent: the volumes as they were at one instant. |
| `live` | Left running. | Whatever the volumes hold while the copy reads them. |

Set it per service with the [`arcave.capture.mode`](reference/capture_labels.md) label, or for every service no label names with [`capture.mode`](reference/configuration.md#capture):

```yaml
services:
  db:
    image: postgres:16
    labels:
      arcave.capture.mode: pause
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata: {}
```

## `pause`: no restart, crash-consistent

`docker pause` freezes every process of the container. While it is frozen nothing it runs can change its volumes, so the copy is a snapshot of one instant — what a power cut would leave on disk. Postgres, MySQL/InnoDB, MongoDB/WiredTiger and Redis with AOF recover from that state on their next start, replaying their own journal, exactly as after a filesystem snapshot. Nothing is restarted, and clients keep their connections: a query sent while the service is frozen waits on TCP and is answered once it is resumed.

For the copy to be recoverable, everything the database needs to recover must be in volumes the savepoint captures: for Postgres both `PGDATA` and `pg_wal`, if you moved the WAL to a volume of its own.

A service with a health check reads `unhealthy` while it is frozen, and Docker keeps saying so after the resume until its next probe. arcave waits for that probe before it goes on — at most one health check interval — so a service that waits on it with `condition: service_healthy` is not refused.

## `live`: consistency is yours

A `live` service is not held at all: its volumes are copied while it writes to them, and a copy of a volume being written can be torn. Use it only where that is fine:

- the volume is a filesystem snapshot already (ZFS, LVM) and the service's own tooling made it consistent;
- the service is stopped or quiesced by something outside arcave;
- an inconsistent copy is acceptable — a cache, a scratch directory.

## Hooks: preparing the application

A hook is a command arcave runs inside a service's containers around the capture, as `docker exec` would: `arcave.hook.pre-capture` before the service is held still, `arcave.hook.post-capture` once everything is back. Use them to flush what a mode alone does not — a checkpoint, a maintenance switch, a lock:

```yaml
services:
  db:
    image: postgres:16
    labels:
      arcave.capture.mode: pause
      arcave.hook.pre-capture: sh -c 'psql -U "$$POSTGRES_USER" -c CHECKPOINT'
      arcave.hook.pre-capture.timeout: 2m
  app:
    labels:
      arcave.hook.pre-capture: /app/bin/maintenance on
      arcave.hook.post-capture: /app/bin/maintenance off
      arcave.hook.post-capture.on-error: continue
```

- **When.** `pre-capture` runs in every running replica of the service, before anything is stopped or frozen. `post-capture` runs in every replica once the whole project is started and resumed, whether its `pre-capture` ran or not, and even after a failed capture or a failed start.
- **How.** The command is split into words as a shell would and run without one: a pipe, a `&&` or a variable needs `sh -c '…'`. Compose interpolates `$` in labels, so write `$$` for one the shell should see. `.user` runs it as another user, `.timeout` bounds it (a Go duration; default [`capture.hooks.timeout`](reference/configuration.md#capture), 60s).
- **All at once.** Every hook of a phase runs at the same time, in every service; there is no order between services. When one service's hook must run before another's, have one hook do both steps.
- **On failure.** A non-zero exit, a timeout or an exec the daemon refused fails the hook. With `.on-error: fail` (the default, [`capture.hooks.on_error`](reference/configuration.md#capture)) a failed `pre-capture` hook fails the savepoint before anything is held; nothing is captured, the other hooks still run to their end, and the `post-capture` hooks still run. A failed `post-capture` hook fails the command after the savepoint is committed: the error names the savepoint, which exists and is complete. With `.on-error: continue` the failure is a warning: a `pre-capture` one is also recorded in the savepoint's `not_covered.failed_hooks`, since the application may not have been prepared. `ARCAVE_CAPTURE_HOOKS_ON_ERROR=continue` lets one run through a broken hook.
- **A replica not running** — frozen, stopped or created — is skipped with a warning; it is captured as it is.
- **Make `post-capture` idempotent.** It runs after a capture that failed too, and its `pre-capture` may not have run — or may still be running: a hook past its timeout is not killed, and can finish after the `post-capture` hook that undoes it.
- **A rollback runs no hook**, its safety snapshot included.

Recipes:

| Service | `pre-capture` | `post-capture` |
|---|---|---|
| Postgres | `sh -c 'psql -U "$$POSTGRES_USER" -c CHECKPOINT'` — shortens the recovery a crash-consistent copy replays | — |
| Redis | `redis-cli SAVE` — the dump on disk is the moment of the capture | — |
| MongoDB | `mongosh --quiet --eval 'db.fsyncLock()'` | `mongosh --quiet --eval 'db.fsyncUnlock()'` |
| Your app | `/app/bin/maintenance on` | `/app/bin/maintenance off` |

`pg_backup_start` is not a pre-capture hook: it is bound to the session that called it, and the hook's session ends with the hook.

## Coming back

Whatever state the project was in, a `savepoint create` leaves it running. Every service it froze is resumed, and so is every replica that was already frozen — paused by you, or by an arcave run that was killed halfway — with a warning naming it. Then the whole compose project is started, as after a `stop`. There is no marker of what a run did: the next run converges on its own.

A rollback replaces the project, so it always stops it. A frozen replica is resumed first, with a warning, and the rollback's safety snapshot is always taken with `stop`, whatever the labels or the config say.

## In the progress output

Whatever the modes, the hold is one step, `savepoint.hold`, and the way back one more, `savepoint.release`; a replica found paused is a warning on them. Under them, `compose.stop` and `compose.start` show compose's containers, and `hook.pre-capture` and `hook.post-capture` a `hook` step per replica. See [Output](reference/output.md#step-kinds).
