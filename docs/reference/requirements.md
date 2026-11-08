---
title: Requirements
description: What arcave needs — Docker 25 or newer or Podman, Linux or macOS, a compose project the daemon knows — and the long list of things it does not need.
keywords: [arcave requirements, Docker 25, Podman, prerequisites, Linux, macOS, docker compose]
---

# Requirements

## Docker 25 or newer, or Podman

arcave captures each container's writable layer by committing it and exporting it, and it stores the export split by content — which needs the OCI layout `docker save` writes from Docker 25 onward. An older daemon exports a shape whose members carry no digest, so `savepoint create` and `rollback` refuse it before they stop or load anything:

```text
unsupported docker daemon: the daemon reports version 24.0.7; arcave needs Docker 25 or newer
```

`rollback` reports it after `prepare: `. `savepoint list`, `savepoint remove` and the `session` commands touch no image and keep working. A daemon whose version cannot be read is let through with a warning, and the capture itself still refuses an archive of the old shape:

```text
unsupported image archive format: member "…" is neither a blob nor metadata; a daemon older than Docker 25 exports this shape
```

Podman, which names itself in `docker version`, is not held to Docker's floor: arcave commits and exports through Podman's own API, which writes the OCI layout, and reads a volume through a `tar` of its own, which keeps the owners Podman's archive API would rewrite to the container's user. Volumes, writable layers and images are all captured. A Podman daemon that reports no version is refused, since requests to its own API name it. A Podman daemon behind TLS is reached as a Docker one is: see [Podman behind TLS](#podman-behind-tls).

The daemon must also be **reachable** for `savepoint create` and `rollback`, which ask it which engine it is before anything else. `init`, `savepoint list`, `savepoint remove` and the `session` commands reach no daemon:

```text
init dependencies: read the daemon version: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?
```

Which daemon, and how to reach it, is the [`docker` config section](configuration.md#docker); left empty, it reads `DOCKER_HOST` and friends.

### Connecting to Podman

arcave reaches Podman through its Docker-compatible socket, named by `DOCKER_HOST` or `docker.host`. It does not read `CONTAINER_HOST` or `podman system connection`, so with neither set it looks for Docker's socket and fails as above.

On Linux, rootless:

```bash
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
```

On Linux, rootful, the socket is `unix:///run/podman/podman.sock`, enabled with `sudo systemctl enable --now podman.socket`.

On macOS, `podman machine start` prints the `DOCKER_HOST` to export when it does not serve Docker's default socket.

Without systemd, start the service with no idle timeout, since one that exits while idle can cut a rollback short:

```bash
podman system service --time 0
```

#### Podman behind TLS

Podman's API service terminates TLS itself; `--tls-client-ca` also requires a client certificate:

```bash
podman system service --time 0 --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca ca.pem tcp://0.0.0.0:8443
```

Point arcave at it as at a Docker daemon behind TLS: the [`docker` section](configuration.md#docker), a Docker context, or `DOCKER_HOST` with `DOCKER_TLS_VERIFY` and `DOCKER_CERT_PATH`. Podman's own API is reached over the same connection and certificates. Name the daemon `tcp://`: a context's `http://` or `https://` host is refused, since the Docker client cannot attach to a container over one.

arcave is tested on projects brought up by `docker compose` through this socket. `podman compose` is a wrapper that runs `docker-compose` when it is installed and `podman-compose` otherwise; `podman-compose` itself has not been tested, and neither has `DOCKER_HOST=ssh://` to a Podman host.

## Linux or macOS

Released binaries:

| OS | Architectures |
|---|---|
| macOS | `amd64`, `arm64` |
| Linux | `amd64`, `arm64`, `armv6`, `armv7`, `ppc64le`, `riscv64`, `s390x` |

**There is no Windows build.** arcave takes its project lock through a system call Windows does not have, so the tool does not build for it at all.

## A compose project the Docker daemon knows

arcave reads the **running project** from the daemon by the labels compose stamps on what it creates. It never opens a compose file — a file that has moved, been edited or been generated changes nothing.

`savepoint create` needs the project's containers. `docker compose stop` is fine: stopped containers still describe the project. `docker compose down` is not — it removes the containers, and with them everything arcave knows about the project:

```text
compose project "myapp": no containers found: bring the compose project up once, or name another project with -p
```

`rollback` does not need them: the savepoint carries the spec it rebuilds the project from, so a project never started, or taken down, is rolled back too. Named volumes such a project left behind are overwritten, or refused under [`rollback.leftover_volumes: refuse`](configuration.md#rollback).

Named volumes the project declares have to exist in the daemon too. A capture that finds one missing is an error rather than a skip, because an empty capture would silently replace live data at the next rollback:

```text
docker volume "myapp_pgdata" does not exist (bring the project up once to create it)
```

One `docker compose up -d` answers both for `savepoint create`.

## An initialised project

Every command but `init` needs `.arcave/config.yaml` under the working directory. The `savepoint` commands and `rollback` also need a compose project named with `-p/--project-name` or `ARCAVE_PROJECT_NAME`; the `session` commands and `mcp` do not.

## For the `cdc` backend only

[`snapshot.backend: cdc`](configuration.md#snapshot) pulls and runs `restic/restic:0.19.1` in one-shot containers, so the daemon needs to be able to pull it. By default arcave reaches those containers' repositories over the Docker API itself, so nothing else is needed — a remote `DOCKER_HOST` included. That default is verified on Docker Engine and Docker Desktop; for other engines see the [support matrix](#container-engines). `backends.cdc.transport: tcp` is the alternative wherever the default does not run.

A custom `backends.cdc.image` needs a shell under the default transport — `/bin/sh`, `mkfifo` and `cat`; the stock image has them. See [`backends.cdc`](configuration.md#backendscdc).

Under `transport: tcp` the containers need to reach a loopback port on your host instead. `network_mode: auto` works that out per platform; a remote or rootless daemon needs `network_mode: bridge` and a `container_host` that resolves from inside a container.

`fullcopy`, the default, needs nothing beyond Docker.

## Container engines

Tested on macOS arm64, 2026-11-02, with arcave's integration suites and a smoke of the built binary
(savepoint, change, rollback, compare, gc) over each transport.

| Engine | Version | `fullcopy` | `cdc`, `stdio` | `cdc`, `tcp` | Notes |
|---|---|---|---|---|---|
| Docker Desktop | Engine 29.8.2 | ✅ | ✅ | ✅ | |
| Docker Engine on Linux | 29.8.2, in Docker Desktop's VM | ✅ | ✅ | ✅ | `network_mode: auto` picks the host network |
| Docker Engine, rootless | 29.6.2, Ubuntu 26.04 in Lima | ✅ | ✅ | ✅ | |
| Colima | 0.10.3, dockerd 29.5.2 | ✅ | ✅ | ✅ | |
| OrbStack | 2.2.5, Engine 29.8.2 | ✅ | ✅ | ✅ | |
| Podman, rootless and rootful | 6.1.4 | ✅ | ✅ | ✅ | retested 2026-11-03 on `podman machine`, behind TLS and mutual TLS 2026-11-06, older versions not tested; what Podman does not store — `sysctls`, a fixed address, `domainname` — is not put back: see [Limitations](limitations.md) |

Speed of a first backup and a full restore of 1 GiB, in seconds — `stdio` against `tcp`. `stdio` is bound by
how fast the engine streams a `docker exec`, `tcp` by its network. The Podman rows measure the transport only:

| Engine | backup, `stdio` / `tcp` | restore, `stdio` / `tcp` |
|---|---|---|
| Docker Desktop | 5.5 / 2.5 | 7.2 / 1.8 |
| Docker Engine on Linux | 8.1 / 3.1 | 8.5 / 1.6 |
| Docker Engine, rootless | 5.0 / 5.6 | 5.3 / 8.0 |
| Colima | 5.0 / 3.7 | 4.5 / 4.6 |
| OrbStack | 3.5 / 3.0 | 3.2 / 1.5 |
| Podman, rootless | 6.4 / 4.7 | 10.0 / 9.2 |
| Podman, rootful | 6.1 / 3.8 | 11.2 / 9.5 |

## There is nothing else to install

* **Not the `docker` CLI, and not the compose plugin.** arcave speaks to the daemon itself and drives compose itself. A machine with a daemon and the arcave binary is a complete installation. arcave carries compose 5.7.
* **Not a particular filesystem.** No ZFS, no btrfs, no reflink support, no copy-on-write anything. arcave gets cheap repeat savepoints from deduplication and incremental capture, which work on an ordinary directory on an ordinary disk.
* **Not a reconfigured daemon.** No storage driver to choose, no experimental features, no privileges beyond what talking to the daemon already takes.
* **Nothing to write per database.** arcave stops the services and captures the volumes at rest, so a capture needs no engine-specific hook or quiescing script of your own.

## Next step

[Limitations](limitations.md) — what arcave does not do, once it is running.
