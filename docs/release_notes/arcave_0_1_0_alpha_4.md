---
title: arcave 0.1.0-alpha.4
description: Podman support, a rollback you can undo, rollbacks without a safety snapshot, and compose 5.7.
keywords: [arcave release notes, 0.1.0-alpha.4, changelog, Podman, Open-Source, Greenmask]
---

# arcave 0.1.0-alpha.4

arcave 0.1.0-alpha.4 was released on 2026-11-08. Run arcave on Podman, undo a rollback, and skip the safety snapshot when a rollback has to be fast. arcave now drives compose 5.7.

:::warning

**This is still an alpha.** Commands, flags, output, config keys and the storage format may change between alpha releases with no migration path. Savepoints taken with alpha.3 may not be readable by alpha.4. Do not use it in production yet.

:::

## Breaking changes

- **A rollback keeps its safety snapshot.** Before, a rollback that succeeded deleted it. Now it is the rollback's undo: `arcave rollback <id>`, with the id the result names in `safety_snapshot` (was `safety_snapshot_kept`), puts back the state the rollback replaced. Safety snapshots pile up until you run `arcave gc`, which keeps the newest `gc.safety_snapshots.retain_recent` of each compose project, 1 by default, and deletes the rest; `--kind safety-snapshots` narrows a run to them. On `fullcopy` each one is a full copy of the volumes, so run gc after a rollback or on a schedule. `--progress json` no longer reports a `volume.remove` step.
- **gc removes an unused `arcave/…` layer tag right away.** The one-hour grace is gone, and with it `gc.restored_layer_grace`, `--restored-layer-grace` and the MCP `gc` argument `restored_layer_grace`. A config that still sets the key is refused.
- **`docker.tls_verify` is gone.** Any of `docker.cert_path`, `ca_file`, `cert_file` or `key_file` turns TLS on and verifies the daemon's certificate; `docker.skip_tls_verify` turns it on without verifying. A config that still sets `tls_verify` is refused.

## New

- **Podman.** Rootless and rootful Podman, local or behind TLS: volumes, container files and images are all captured and put back. arcave does not read `CONTAINER_HOST`, so point `DOCKER_HOST` at Podman's socket, as [Connecting to Podman](../reference/requirements.md#connecting-to-podman) shows.
- **Roll back without a safety snapshot.** `arcave rollback --safety-snapshot=false`, `rollback.safety_snapshot: false` in the config or `safety_snapshot: false` in the MCP `rollback` tool skips capturing the live project. It is faster, most of all on large volumes, but nothing can undo the rollback, and after a failure the project stays as it is until you run a rollback again.

## Changed

- **compose 5.7** (was 2.40), reached through the moby client.
- **Dependencies are past 24 published security advisories.** Eleven went with `github.com/docker/docker`, `compose/v2` and the older docker/cli, buildkit, go-archive and distribution; the rest with updates of echo, containerd, klauspost/compress, grpc, moby/sys/user and the OpenTelemetry exporter.

## Fixes

- A rollback no longer recreates a named volume it does not restore. When a volume's driver, options or labels changed since the savepoint, compose asks before recreating it, and arcave said yes to every volume, so the data of one it does not capture was lost. Now such a volume keeps its settings and data, and the rollback warns with its name.
- The container of a compose `pre_start` hook or of a provider is no longer taken for a replica of its service.
- `DOCKER_TLS_VERIFY` and `DOCKER_CERT_PATH` now apply beside a `tcp://` `DOCKER_HOST`. Before, arcave ignored them.
- `savepoint list` and `savepoint remove` no longer contact the daemon under `backends.cdc.transport: tcp` either.

## Good to know before you rely on it

- On Podman, a rollback does not put back what Podman does not store: `sysctls`, a fixed `ipv4_address`, `domainname` and a few more. A project with `links:` does not run on Podman at all.
- A savepoint taken on Docker is not meant to be rolled back on Podman, or the other way round. arcave checks nothing, and such a rollback can fail after the project has stopped.
- A gc on another machine sharing the store doesn't see a rollback interrupted on this one, and may delete the safety snapshot its recovery needs. Recover first.
- **Lose the encryption key and the store is lost.** There is no recovery key.
- The services stop during every savepoint and rollback, for longer when the volumes are large.
- After a rollback, running `docker compose up` by hand recreates the containers from your compose file, and the restored container files are lost. Volume data stays. compose 5.7 does not change that.
- On a store shared by several machines, run `arcave gc` without `--dry-run` only while no other machine works in the same session. In rare cases two machines claiming the store lock at the same moment both believe they hold it, and gc could then delete data another machine's savepoint has just reused.
- SFTP storage does not check the server's host key yet.

The full list is in [Limitations](../reference/limitations.md).

#### Full Changelog: [v0.1.0-alpha.3...v0.1.0-alpha.4](https://github.com/GreenmaskIO/arcave/compare/v0.1.0-alpha.3...v0.1.0-alpha.4)

## Links

- [Documentation](../index.md)
- [Discord](https://discord.com/invite/rKBKvDECfd)
- [Issues](https://github.com/GreenmaskIO/arcave/issues)
