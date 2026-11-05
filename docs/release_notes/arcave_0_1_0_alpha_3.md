---
title: arcave 0.1.0-alpha.3
description: Client-side store encryption, cdc over docker exec by default, and zstd-compressed fullcopy volume archives.
keywords: [arcave release notes, 0.1.0-alpha.3, changelog, Open-Source, Greenmask]
---

# arcave 0.1.0-alpha.3

arcave 0.1.0-alpha.3 was released on 2026-11-05. Encrypt the store on your own machine, run `cdc` without opening a port, and store smaller `fullcopy` savepoints by default.

:::warning

**This is still an alpha.** Commands, flags, output, config keys and the storage format may change between alpha releases with no migration path. Savepoints taken with alpha.2 may not be readable by alpha.3. Do not use it in production yet.

:::

## Breaking changes

- **`cdc` now reaches its repositories over `docker exec` by default.** The new key `backends.cdc.transport` is `stdio` unless you set it, so existing configs switch: no port opens, and a remote `DOCKER_HOST` works. It is usually slower than before: on Docker Desktop a full restore of 1 GiB took 7.2 s against 1.8 s. To go back, set `transport: tcp`. Under `stdio`, `server_address`, `server_scheme`, `container_host` and `network_mode` are ignored with a warning, and a custom `backends.cdc.image` must ship `/bin/sh`, `mkfifo` and `cat`.
- **Every config key now reads its `ARCAVE_<KEY>` variable.** Before, a variable only worked for a key the file already carried or a flag bound. Now `ARCAVE_BACKENDS_CDC_RESTORE_MODE=wipe` works with no `backends` section in the file. A stale variable you still have exported can now change what a command does, and an invalid one fails it. `arcave init` still reads none of them into the file it writes.
- **`fullcopy` compresses volume archives with zstd by default.** Each one is stored as `data.tar.zst` instead of `data.tar`, and its `stats` entry gains `"payload": {"compression": "zstd"}`. `size_bytes` is still the uncompressed tar, and `stored_bytes` is what reached the store. Anything that reads archives straight from the store needs to decompress them, or set `backends.fullcopy.compression: none`.

## New

- **Encrypt the store.** With `encryption.enabled: true`, every object arcave stores is encrypted on your machine to an [age](https://age-encryption.org) key: records, compose specs, volume data, container files, images, `cdc` repositories and session locks. arcave keeps no copy of the key. The store holds only its public half, so a host with no key or the wrong one is refused before anything stops.

  ```shell
  age-keygen                                      # prints AGE-SECRET-KEY-1…
  export ARCAVE_ENCRYPTION_KEY=AGE-SECRET-KEY-1…
  ```

  ```yaml
  # .arcave/config.yaml
  encryption:
    enabled: true
  ```

  Start an encrypted store at an empty location: arcave encrypts nothing in place, and switching it on over existing data leaves that data unreadable.

- **Smaller `fullcopy` savepoints.** A fresh `postgres:16` volume went from 40.8 MB to about 2.5 MB. A rollback reads each archive's format from its savepoint, so changing `backends.fullcopy.compression` only affects new savepoints.

## Fixes

- `savepoint list` and `savepoint remove` no longer contact Docker under the default `stdio` transport, whichever `snapshot.backend` you use, so they work while the daemon is down. Under `backends.cdc.transport: tcp` with `network_mode: auto` they still do.

## Good to know before you rely on it

- **Lose the encryption key and the store is lost.** There is no recovery key.
- Encryption hides contents, not names and sizes: savepoint ids, volume names and compose project names stay readable in the store. Anyone who can write to the store can still delete objects. Volumes and images in Docker on the host stay unencrypted.
- **Podman is not supported yet.** arcave lets it through with a warning, and a default `savepoint create` fails there. Don't get past that by turning off `capture.writable_layers` and `capture.images`: a `fullcopy` rollback then brings volumes back empty while reporting success, and with either backend a rollback to a savepoint taken after an earlier rollback fails, removes the project's containers and leaves an `inconsistent` marker that refuses further commands.
- The services stop during every savepoint and rollback, for longer when the volumes are large.
- You can only go back to a savepoint you took. There is no point-in-time recovery.
- If you leave a service's container files out of savepoints, a rollback rebuilds it from its `image:`, which must still be in Docker or a registry. The image copy stored in the savepoint isn't used yet.
- Give every stateful service a health check. Without one, arcave treats a service as up as soon as its container starts.
- After a rollback, running `docker compose up` by hand recreates the containers from your compose file, and the restored container files are lost. Volume data stays.
- Some space takes two `arcave gc` runs to come back: an unused image's layers go on the run after the image.
- On a store shared by several machines, run `arcave gc` without `--dry-run` only while no other machine works in the same session. In rare cases two machines claiming the store lock at the same moment both believe they hold it, and gc could then delete data another machine's savepoint has just reused.
- gc leaves alone an `arcave/…` tag of a compose project no session holds any more, or one an earlier arcave version made. List them with `docker image ls --filter 'reference=arcave/*'` and remove those by hand.
- On Docker Desktop for macOS with VirtioFS file sharing, `cdc` can fail on bind mounts. Use named volumes, gRPC FUSE, or `fullcopy` for that project.
- SFTP storage does not check the server's host key yet.

The full list is in [Limitations](../reference/limitations.md).

#### Full Changelog: [v0.1.0-alpha.2...v0.1.0-alpha.3](https://github.com/GreenmaskIO/arcave/compare/v0.1.0-alpha.2...v0.1.0-alpha.3)

## Links

- [Documentation](../index.md)
- [Discord](https://discord.com/invite/rKBKvDECfd)
- [Issues](https://github.com/GreenmaskIO/arcave/issues)
