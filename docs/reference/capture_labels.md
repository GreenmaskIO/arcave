---
title: Capture labels
description: The arcave.* compose labels that decide what a savepoint captures — per service, per mount and per volume — which backend captures it, how the labels and the config's capture defaults combine, and what a label arcave cannot read does.
keywords: [arcave labels, capture policy, arcave.capture, arcave.mounts.exclude, writable layer, docker compose labels, cdc]
---

# Capture labels

By default a savepoint captures everything: every named volume, bind mount and anonymous volume, every container's writable layer, and the image every service runs on. `arcave.*` labels in your compose file narrow that, unit by unit, and pick the backend a volume is captured by. The config's [`capture`](configuration.md#capture) section sets the same choices by kind, for everything no label names.

## The labels

On a **service** (`services.<name>.labels`):

| Label | Value | What it decides |
|---|---|---|
| `arcave.capture` | `true` / `false` | Everything of the service: its writable layers, its image, its binds and anonymous volumes. A named volume follows it only when **every** service mounting the volume says `false`. |
| `arcave.mounts.exclude` | comma-separated mount targets | Those mounts are left out. Reaches a named volume mounted at that target too. |
| `arcave.mounts.include` | comma-separated mount targets | Those mounts are captured, whatever the config or the service's `arcave.capture` says. |
| `arcave.mounts.fullcopy` | comma-separated mount targets | Those mounts are captured by `fullcopy`. |
| `arcave.mounts.cdc` | comma-separated mount targets | Those mounts are captured by `cdc`. |
| `arcave.volume.backend` | `fullcopy` / `cdc` | The default backend of the service's binds and anonymous volumes (not its named volumes). |
| `arcave.writable-layer.capture` | `true` / `false` | The writable layers of the service's containers. |
| `arcave.writable-layer.backend` | `dedupcopy` / `fullcopy` | How those layers are stored; see [`writable_layers`](configuration.md#writable_layers). |
| `arcave.image.capture` | `true` / `false` | The bytes of the image the service's `image:` names. |

On a **named volume** (`volumes.<name>.labels`) or an **anonymous volume** (the long syntax's `volume.labels`):

| Label | Value | What it decides |
|---|---|---|
| `arcave.capture` | `true` / `false` | That volume. |
| `arcave.backend` | `fullcopy` / `cdc` | The backend that volume is captured by. |

A target in a list must be one the service itself mounts — a bind, an anonymous volume or a named volume. Any other `arcave.*` key is refused.

## Which one wins

The most specific label wins:

1. The volume's own label.
2. The service's target lists — `arcave.mounts.exclude`/`include` for whether, `arcave.mounts.fullcopy`/`cdc` for the backend.
3. The service's defaults — `arcave.capture`, `arcave.volume.backend`.
4. The config: [`capture.*`](configuration.md#capture), [`snapshot.backend`](configuration.md#snapshot) and [`writable_layers.backend`](configuration.md#writable_layers).

`arcave.writable-layer.capture` and `arcave.image.capture` sit at step 2 for their own unit: they override the service's `arcave.capture`.

A named volume several services mount is left out if any of them excludes its target, and captured if any of them includes it. If two of them list it for different backends, that is an error.

## Where the labels are read

arcave reads the running compose project, not your compose file: a service's labels come from its first container, a named volume's from the volume as Docker created it. A changed service label takes effect once `docker compose up` has recreated the containers. Docker cannot relabel an existing volume, so for a named volume that already exists, put `arcave.mounts.exclude` on the service mounting it instead.

## Examples

Leave a cache directory out of a service:

```yaml
services:
  app:
    image: myapp:latest
    labels:
      arcave.mounts.exclude: /app/cache
    volumes:
      - ./src:/app/src
      - type: volume
        target: /app/cache
```

Skip the writable layer of a stateless service, whose image already holds everything it needs:

```yaml
services:
  web:
    image: nginx:1.27
    labels:
      arcave.writable-layer.capture: "false"
```

Capture one large volume with `cdc`, so each savepoint stores only what changed, while the rest stay on the default backend:

```yaml
services:
  db:
    image: postgres:16
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata:
    labels:
      arcave.backend: cdc
```

Quote `"true"` and `"false"`: compose labels are strings.

## What a bad label does

A label arcave cannot act on — an unknown `arcave.*` key, a value it does not accept, a target the service does not mount, a target both included and excluded — fails `savepoint create` before anything is stopped:

```text
invalid capture policy label: arcave.capture="no" on service app: want true or false
```

A rollback does not fail on one: its safety snapshot logs a warning and follows the config, and captures everything the rollback is about to overwrite whatever its labels say.

## What was left out

Everything a savepoint did not capture is listed in the record's `not_covered` — a left-out mount under `not_covered.mounts` with `reason: excluded` and the label that said so, a skipped layer under `not_covered.writable_layers`, a skipped image under `not_covered.images`. See [Output](output.md#the-savepoint-record).

A rollback puts back what the savepoint holds, whatever the labels say now:

- **An excluded anonymous volume comes back empty**, because the rollback rebuilds the container and nothing is restored into it. An excluded named volume or bind keeps whatever it holds at the time of the rollback.
- **A container whose layer was not captured** is rebuilt from its service's image, without the changes it made to its own filesystem.
- **A service whose image was not captured** needs the image in the daemon or a registry that serves it. An image no registry holds — one built locally — and the daemon no longer has is refused before anything is stopped.

`savepoint create` warns, per unit, when something is captured otherwise than in the latest savepoint — the sign that a label or the config changed in between.

Each savepoint's recorded spec carries `io.arcave.capture.*` labels stating what arcave decided for every unit and why; they are arcave's own and are never read as policy.
