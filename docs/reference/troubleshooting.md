---
title: Troubleshooting
description: Recovering from an interrupted rollback, a held project lock, a torn-down project, a missing volume, a health-check timeout and cdc container networking.
keywords: [arcave troubleshooting, interrupted rollback, safety snapshot, project lock, health check, cdc, restic]
---

# Troubleshooting

Each entry names the symptom as arcave reports it, what happened, and what to do.

## An interrupted rollback

**Symptom.** Every mutating command refuses to run:

```text
rollback to savepoint 1780335122 was interrupted (restoring)
```

**What happened.** A rollback was killed after it began writing. `.arcave/inflight.json` records the phase it died in, and that marker refuses the next mutating workflow so nothing is built on a half-written environment.

**The remedy.** Return to the safety snapshot that rollback took of the pre-rollback state. It is the one operation the marker lets through.

```shell title="1. Find it"
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

The name carries the id of the savepoint that rollback was applying, so a leftover always identifies the run that abandoned it.

```shell title="2. Apply it, by id"
arcave rollback 1780340557
```

An id is the only reference that reaches a safety snapshot: `latest` and name lookups skip them.

```shell title="3. Once the environment is right, remove it"
arcave savepoint remove 1780340557
```

Not before. Until then it is the last copy of the pre-rollback state.

**What the phase tells you.** The remedy above is the same in every case; the phase says how much was overwritten.

| Phase | What it means |
|---|---|
| `recreating` | Died between the teardown and the containers existing again. The volumes are untouched. |
| `restoring` | Died while writing volumes. The environment is partly overwritten. |
| `restore-done` | Every volume was written back; only the start did not finish. Starting the project and removing `.arcave/inflight.json` is enough, if that is what you want. |
| `inconsistent` | The automatic replay failed too. The safety snapshot is retained and is the last copy of the pre-rollback state. |

## `another arcave command is already running in this project`

Another arcave process holds the lock on `.arcave/lock`. The lock is not a queue — a second run fails rather than waiting.

```shell
ps aux | grep '[a]rcave'
```

Usual causes: a background `arcave mcp` server serving an agent, a CI job that has not finished, a second terminal. The lock is released when the holding process exits, a crash included — it is an operating-system lock, not a file arcave has to clean up. If nothing is running, retry.

## `config is not found: run 'arcave init' if not yet initialized`

Either the project was never initialised, or you are not in its directory: `.arcave/config.yaml` is resolved against the working directory.

```shell
arcave init                                                    # initialise here
arcave --config /path/to/.arcave/config.yaml savepoint list    # or point at it
```

## `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME`

Every command but `init` acts on one compose project, and nothing guesses which.

```shell
arcave -p myapp savepoint list
```

The name is what `docker compose ls` shows in the first column: the compose file's `name:` if it declares one, and the directory name otherwise. Before exporting `ARCAVE_PROJECT_NAME`, read what else [it names](environment_variables.md#arcave_project_name).

## `no containers found for compose project "myapp"`

**What happened.** `docker compose down` was run, or the project was never brought up. Down removes the containers, and with them everything arcave knows about the project.

```shell
docker compose up -d
```

arcave reads the running project from the daemon, not from your compose files. `docker compose stop` is fine — stopped containers still describe the project.

## `docker volume "myapp_pgdata" does not exist (bring the project up once to create it)`

The project declares a named volume the daemon does not hold. arcave treats that as an error rather than a skip: an empty capture would silently replace live data at the next rollback.

```shell
docker compose up -d
```

## `no container for service "app" replica 1 in project "myapp" mounts "/data" (is the project created?)`

An anonymous volume belongs to one replica and can only be reached through its container, and that container is gone.

```shell
docker compose up -d
```

Consider making the mount a named volume: it is addressable directly, survives container removal, and is captured once however many services mount it.

## `capture directory is not empty`

```text
capture directory is not empty: 1780335122 holds 3 object(s) — a savepoint of this same second, or an interrupted run's remains; retry, and check what is there before removing it
```

An earlier run was interrupted after it started writing and before it committed a record — or, an id being a Unix second, a savepoint of this very second already exists. **Retry first**: a second attempt lands on the next second and usually just works.

If the directory persists, its objects are the last copy of whatever that run captured. Look before removing:

```text
<storage.directory.path>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>/1780335122/
```

If there is no `savepoint.json` there, nothing lists what is in it and nothing puts it back.

## `savepoint 1780335122 committed; services failed to start`

**The savepoint exists and is complete.** Only the restart failed, so note the id and look at the project: the services are stopped, or unhealthy, or slower than [`--wait-timeout`](configuration.md#compose). Do not take another savepoint to compensate.

```shell
docker compose ps
arcave --log-level debug savepoint create retry --wait-timeout 5m
```

## Services never become healthy

**Symptom.** A savepoint reports a start failure, or a rollback replays itself, after about two minutes.

`compose.wait_timeout` elapsed before every service reported healthy. On a rollback that triggers the automatic replay, so a healthy-but-slow project is rolled back for the wrong reason.

```shell title="Raise the bound for one run"
arcave rollback latest --wait-timeout 5m
```

```yaml title="Or for the project"
compose:
    wait_timeout: 5m
```

```shell title="See where it is stuck"
arcave --log-level debug rollback latest
```

In-flight steps — `Waiting`, `Starting`, `Creating`, image-pull progress — are logged at debug level. A service that declares no health check is considered up the moment its container starts, which is faster and worse.

## A database needs recovery after a rollback

`compose.stop_timeout` was probably too short when the savepoint was taken: arcave captures the volumes the moment the containers are down, so a container killed mid-shutdown is captured mid-shutdown — and that capture reported success.

```yaml title="Give it room to close cleanly"
compose:
    stop_timeout: 60s
```

Zero means "unset" and the default applies, so an immediate kill is spelled `1s`, not `0`.

## A rollback is refused: `savepoint carries no compose spec`

The project is rebuilt from the compose spec the savepoint recorded, and savepoints taken before arcave recorded one carry none. Their volume data is still on disk, but there is no supported path that puts it back. Take a fresh savepoint.

## `unsupported image archive format`

```text
unsupported image archive format: member "…" is neither a blob nor metadata; a daemon older than Docker 25 exports this shape
```

The daemon is older than Docker 25. Upgrade it — see [Requirements](requirements.md#docker-25-or-newer).

## `two volumes address the same object id`

Two mounts flatten to the same [object id](storage_layout.md#object-ids): `{app, /var/lib}` and `{app-var, /lib}` both render `volume-service-app-var-lib`. They would share one capture and overwrite each other, so arcave refuses instead. Rename a service, or move a mount path.

## `cdc` fails on a bind mount on macOS

With `snapshot.backend: cdc`, capturing a host bind mount fails with an I/O error on Docker Desktop for macOS: restic opens each source file with `O_NOATIME`, and VirtioFS answers with `EIO`.

* Make the mount a named volume — those live in the VM's own filesystem and are unaffected.
* Or switch Docker Desktop's file sharing to gRPC FUSE.
* Or use `snapshot.backend: fullcopy` for that project.

## `cdc` containers cannot reach arcave's restic server

A restic container has to reach a server bound to your host loopback, and how it does that differs per platform.

```yaml title="Let arcave work it out"
backends:
    cdc:
        network_mode: auto
```

`auto` is the default and gives a native Linux daemon host networking, anything else the bridge and `container_host`. It reads a remote or rootless daemon as local, which is what `bridge` is for:

```yaml title="A remote or rootless daemon"
backends:
    cdc:
        network_mode: bridge
        container_host: docker-host.internal
```

For a remote daemon, `container_host` has to be a name that resolves **from inside the container** to the host running arcave.

## An unknown key fails the command

```text
resolve config: unmarshal config: parse config: decoding failed due to the following error(s):

'project' has invalid keys: nam
```

Unknown keys are an error, not a value quietly ignored. Check the key against [Configuration](configuration.md); arcave never rewrites the file on its own, so fix it by hand, or regenerate it with `arcave init --override` — and set `storage.directory.path` again in the same command if it was pointing somewhere non-default, since savepoints under the old path stop being visible otherwise.

## `initialise compose project store: stat /srv/arcave/myapp: no such file or directory`

An absolute `storage.directory.path` must already exist. Only a relative one materialises on first use — the asymmetry is what keeps a typo from growing a fresh tree somewhere unexpected.

```shell
mkdir -p /srv/arcave/myapp
```

## An environment variable is ignored

`ARCAVE_<KEY>` reaches a key a flag binds, or one your config file already carries. A `backends.cdc` key is the usual case to trip on: until the file declares the section, the variable is neither taken nor reported. See [Environment variables](environment_variables.md#which-keys-a-variable-reaches).

## JSON output is mixed with log lines

It is not — check which stream you are reading. Results go to stdout, logs to stderr.

```shell
arcave savepoint list 2>/dev/null | jq .
ID=$(arcave savepoint create nightly | jq -r .id)
```

Two exceptions: `arcave --version` prints a plain version line, and `arcave mcp` writes nothing to stdout at all.

## Next step

[Limitations](limitations.md) — which of these are constraints rather than faults.
