---
title: Requirements
description: What arcave needs — Docker 25 or newer, Linux or macOS, a compose project that exists in the Docker daemon — and the long list of things it does not need.
keywords: [arcave requirements, Docker 25, prerequisites, Linux, macOS, docker compose]
---

# Requirements

## Docker 25 or newer

arcave captures each container's writable layer by committing it and exporting it, and it stores the export split by content — which needs the OCI layout `docker save` writes from Docker 25 onward. An older daemon exports a shape whose members carry no digest, and the first capture against one is refused rather than guessed at:

```text
unsupported image archive format: member "…" is neither a blob nor metadata; a daemon older than Docker 25 exports this shape
```

The daemon must also be **reachable**. Every command but `init` opens a connection while it wires itself up, before any workflow starts, so an unreachable daemon fails `savepoint list` exactly as it fails `rollback`:

```text
init dependencies: resolve backends.cdc.network_mode "auto": read docker daemon info: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?
```

Which daemon, and how to reach it, is the [`docker` config section](configuration.md#docker); left empty, it reads `DOCKER_HOST` and friends.

## Linux or macOS

Released binaries:

| OS | Architectures |
|---|---|
| macOS | `amd64`, `arm64` |
| Linux | `amd64`, `arm64`, `armv6`, `armv7`, `ppc64le`, `riscv64`, `s390x` |

**There is no Windows build.** arcave takes its project lock through a system call Windows does not have, so the tool does not build for it at all.

## A compose project that exists in the Docker daemon

arcave reads the **running project** from the daemon by the labels compose stamps on what it creates. It never opens a compose file — a file that has moved, been edited or been generated changes nothing.

The project does have to be there. `docker compose stop` is fine: stopped containers still describe the project. `docker compose down` is not — it removes the containers, and with them everything arcave knows about the project:

```text
no containers found for compose project "myapp": bring the compose project up once, or name another project with -p
```

Named volumes the project declares have to exist in the daemon too. A capture that finds one missing is an error rather than a skip, because an empty capture would silently replace live data at the next rollback:

```text
docker volume "myapp_pgdata" does not exist (bring the project up once to create it)
```

One `docker compose up -d` answers both.

## An initialised project

Every command but `init` needs `.arcave/config.yaml` under the working directory, and a compose project named with `-p/--project-name` or `ARCAVE_PROJECT_NAME`.

## For the `cdc` backend only

[`snapshot.backend: cdc`](configuration.md#snapshot) pulls and runs `restic/restic:0.19.1` in one-shot containers, so the daemon needs to be able to pull it, and those containers need to reach a loopback port on your host. `network_mode: auto` works that out per platform; a remote or rootless daemon needs `network_mode: bridge` and a `container_host` that resolves from inside a container.

`fullcopy`, the default, needs nothing beyond Docker.

## There is nothing else to install

* **Not the `docker` CLI, and not the compose plugin.** arcave speaks to the daemon itself and drives compose itself. A machine with a daemon and the arcave binary is a complete installation.
* **Not a particular filesystem.** No ZFS, no btrfs, no reflink support, no copy-on-write anything. arcave gets cheap repeat savepoints from deduplication and incremental capture, which work on an ordinary directory on an ordinary disk.
* **Not a reconfigured daemon.** No storage driver to choose, no experimental features, no privileges beyond what talking to the daemon already takes.
* **Nothing to write per database.** arcave stops the services and captures the volumes at rest, so a capture needs no engine-specific hook or quiescing script of your own.

## Next step

[Limitations](limitations.md) — what arcave does not do, once it is running.
