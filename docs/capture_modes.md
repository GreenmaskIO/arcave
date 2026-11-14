---
title: Capture modes
description: How a savepoint holds each service still while it copies the data — stop, pause or live — what each one guarantees, and how arcave brings a frozen or half-held compose project back to running.
keywords: [arcave capture mode, docker pause, crash-consistent, savepoint without downtime, arcave.capture.mode, postgres, docker compose]
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

## Coming back

Whatever state the project was in, a `savepoint create` leaves it running. Every service it froze is resumed, and so is every replica that was already frozen — paused by you, or by an arcave run that was killed halfway — with a warning naming it. Then the whole compose project is started, as after a `stop`. There is no marker of what a run did: the next run converges on its own.

A rollback replaces the project, so it always stops it. A frozen replica is resumed first, with a warning, and the rollback's safety snapshot is always taken with `stop`, whatever the labels or the config say.

## In the progress output

Whatever the modes, the hold is one step, `savepoint.hold`, and the way back one more, `savepoint.release`; a replica found paused is a warning on them. See [Output](reference/output.md#step-kinds).
