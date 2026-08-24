---
title: Troubleshooting
description: Recover from arcave failures — an interrupted rollback, a held project lock, a torn-down stack, missing volumes, health-check timeouts and cdc container networking.
keywords: [arcave troubleshooting, interrupted rollback, project locked, safety snapshot, health check timeout, cdc, restic, Open-Source, Greenmask]
---

# Troubleshooting

## An interrupted rollback

**Symptom.** Every mutating command refuses to run:

```text
rollback to savepoint 1780335122 was interrupted (restoring)
```

**What happened.** A rollback was killed after it started writing. `.arcave/inflight.json` records the phase it died in, and the marker blocks the next mutating workflow so a half-restored environment is not built on.

**The remedy.** Roll back to the safety snapshot that rollback took of your pre-rollback state. That is the one operation the marker lets through.

```shell title="1. Find the safety snapshot"
arcave savepoint list --include-safety-snapshots
```

```json title="stdout"
[
  {
    "id": "1780340557",
    "name": "safety-snapshot-1780335122",
    "description": "",
    "labels": { "arcave-safety-snapshot": "true" },
    "created_at": "2026-06-01T21:22:37.009412Z"
  }
]
```

The name says which savepoint that rollback was restoring, so a leftover snapshot always identifies the run that abandoned it.

```shell title="2. Apply it, by id"
arcave rollback 1780340557
```

An id is the only reference form that reaches a safety snapshot — `latest` and name lookups skip them.

```shell title="3. Once the environment is good, remove it"
arcave savepoint remove 1780340557
```

:::warning
Do not delete the safety snapshot before you have confirmed the environment is right. Until then it is the last copy of your pre-rollback state.
:::

### The three phases

| Phase | Meaning |
|---|---|
| `recreating` | Died between the teardown and the containers existing again. **The volumes are untouched** — the remedy is to rebuild, not to restore. |
| `restoring` | Died while restoring volumes. The environment is partially overwritten. |
| `restore-done` | Volumes were restored, but the stack never came up cleanly. |
| `inconsistent` | The automatic revert itself failed. The safety snapshot is retained and is the last copy of the pre-rollback state. |

arcave does not finish a rollback it died in the middle of. The remedy above is the same in all three cases; the phase tells you how much was overwritten.

## `another arcave command is already running in this project`

**What happened.** Another arcave process holds the non-blocking lock on `.arcave/lock`. The lock is not a queue — a second run fails rather than waiting.

**Check for a live process:**

```shell
ps aux | grep '[a]rcave'
```

Common causes: a background `arcave mcp` server serving an agent, a CI job that has not finished, or a second terminal.

The lock is released when the holding process exits, including on a crash — it is an OS-level file lock, not a lock file arcave has to clean up. If nothing is running, simply retry.

## `config is not found: run 'arcave init' if not yet initialized`

Either the project was never initialised, or you are not in the project directory. arcave resolves `.arcave/config.yaml` relative to the working directory.

```shell
arcave init                                   # initialise here
arcave --config /path/to/.arcave/config.yaml savepoint list   # or point at it
```

## `no compose project named`

Every command except `init` needs to know which compose project to act on.

```shell
arcave -p myapp savepoint list
export ARCAVE_PROJECT_NAME=myapp
```

The name is what `docker compose ls` shows in the first column — which is the compose file's `name:` if it declares one, and the directory name otherwise.

## A command fails saying the stack does not exist

**What happened.** `docker compose down` was run. It removes the containers, and with them everything arcave knows about the project.

```shell
docker compose up -d
```

arcave reads the running stack from the daemon, not from your compose files. `docker compose stop` is fine — stopped containers still describe the stack — but `down` leaves nothing to read.

## A capture fails on a missing named volume

**What happened.** The project declares a named volume the daemon does not have. arcave treats this as an **error**, not a skip: an empty capture would silently wipe live data at the next rollback.

```shell
docker compose up -d          # creates the volumes the project declares
```

## A capture fails on an anonymous volume

An anonymous volume's Docker name is generated per container, so arcave has to ask the daemon through that service's container. If the container was removed, there is no other way to address it.

```shell
docker compose up -d
```

Consider converting the mount to a named volume — it is addressable directly, survives container removal, and is captured once even when several services mount it.

## Services never become healthy

**Symptom.** A savepoint reports a start failure, or a rollback reverts itself, after roughly two minutes.

**What happened.** `compose.wait_timeout` elapsed before every service reported healthy. On a rollback this triggers the automatic revert, so a healthy-but-slow stack gets rolled back for the wrong reason.

```shell title="Raise the bound"
arcave rollback latest --wait-timeout 5m
```

```yaml title="Or fix it for the project"
compose:
    wait_timeout: 5m
```

```shell title="See where it is stuck"
arcave --log-level debug rollback latest
```

In-flight steps — `Waiting`, `Starting`, `Creating`, image-pull progress — are logged at debug level.

A service with **no** health check is considered up the moment its container starts. That is faster, and worse: define health checks on every stateful service.

## A database looks corrupted after a savepoint

**Likely cause.** `compose.stop_timeout` was too short and the container was killed mid-shutdown. arcave captures the volumes the moment the containers are down, so a database killed mid-shutdown is captured mid-shutdown.

```yaml title="Give it room to close cleanly"
compose:
    stop_timeout: 60s
```

Zero means "unset" and the default applies, so an immediate kill is spelled `1s`, not `0`.

## A savepoint create is refused because the directory is not empty

**What happened.** An earlier run was interrupted after it started writing but before it committed a record. Those objects are the last copy of whatever that run captured, so arcave names them rather than deleting them.

Inspect the directory under the store root, and remove it yourself once you are sure it holds nothing you need:

```text
<storage.directory.path>/arcave/projects/<project>/sessions/global/compose/<stack>/<id>/
```

There is no `savepoint.json` there — if there were, `savepoint list` would show it.

## A rollback is refused: the savepoint carries no compose spec

A rollback rebuilds the stack from the compose spec the savepoint recorded. Savepoints taken before arcave recorded specs carry none, and arcave refuses rather than guessing at a stack to rebuild.

Those savepoints still hold their volume data, but there is no supported path to restore them. Take a fresh savepoint.

## `cdc` fails on a bind mount on macOS

**Symptom.** With `snapshot.backend: cdc`, a capture of a host bind mount fails with an I/O error on Docker Desktop for macOS.

**What happened.** restic sets `O_NOATIME` on each source file, and VirtioFS answers with `EIO` on the next read.

Three ways out:

* Convert the bind mount to a named volume — they live in the VM's own filesystem and are unaffected.
* Switch Docker Desktop's file sharing implementation to gRPC FUSE.
* Use `snapshot.backend: fullcopy` for that project.

## `cdc` containers cannot reach arcave's restic server

**What happened.** A restic container has to reach a server bound to your host loopback, and how it does that differs per platform.

```yaml title="Let arcave probe the daemon"
backends:
    cdc:
        network_mode: auto
```

`auto` is the default and gives a native Linux daemon host networking, anything else the bridge plus `container_host`. Override it when the probe guesses wrong:

```yaml title="A remote or rootless daemon"
backends:
    cdc:
        network_mode: bridge
        container_host: host.docker.internal
```

For a remote daemon, `container_host` must be a name that resolves **from inside the container** to the host running arcave.

## JSON output is mixed with log lines

It is not — check which stream you are reading. Results go to **stdout**, logs to **stderr**.

```shell
arcave savepoint list 2>/dev/null | jq .
ID=$(arcave savepoint create nightly | jq -r .id)
```

Two exceptions: `arcave --version` prints a plain version line, and `arcave mcp` writes nothing to stdout at all — that stream is its JSON-RPC channel.

## An unknown key fails the command

Unknown keys in `.arcave/config.yaml` are an error, not a value silently ignored — a typo fails the command rather than falling back to a default you did not choose.

Check the key against the [Configuration reference](configuration.md). arcave never rewrites the file on its own, so fix it by hand, or regenerate it:

```shell
arcave init --override
```

:::warning
`--override` rewrites the file. If your current config points `storage.directory.path` somewhere non-default, set it again — savepoints under the old path stop being visible otherwise.
:::

## Related docs

* [Limitations](limitations.md) — what is a constraint rather than a fault.
* [Configuration](configuration.md) — every key named above.
* [How arcave works](how_it_works.md) — the mechanism behind these failure modes.
