---
title: Troubleshooting
description: Recovering from an interrupted rollback, a held project lock, a torn-down project, a missing volume, a refused label or config, a stale current session, a health-check timeout and cdc container networking.
keywords: [arcave troubleshooting, interrupted rollback, safety snapshot, project lock, health check, cdc, restic]
---

# Troubleshooting

Each entry names the symptom as arcave reports it, what happened, and what to do.

## An interrupted rollback

**Symptom.** Every command that changes something refuses to run (`rollback` prefixes it with `require no inflight: `):

```text
rollback to savepoint 019e843e-1650-7000-8000-000000000000 was interrupted (restoring)
```

**What happened.** A rollback was killed after it began writing. `.arcave/inflight.json` records the phase it died in, and that marker refuses the next mutating workflow so nothing is built on a half-written environment.

**The remedy.** Return to the safety snapshot that rollback took of the pre-rollback state. It is the one operation the marker lets through, and only in the session and compose project the marker names. An error log line on stderr, just before the error, prints the full command, `--session` and `-p` included:

```text
… volumes may mix states; recover with `arcave rollback --session 00000000-0000-0000-0000-000000000001 -p myapp 019e8491-04c8-7000-8000-000000000000`
```

Pass the same two flags to the commands below.

```shell title="1. Find it"
arcave savepoint list --include-safety-snapshots
```

```json title="stdout"
[
  {
    "id": "019e8491-04c8-7000-8000-000000000000",
    "name": "safety-snapshot-019e843e-1650-7000-8000-000000000000",
    "description": "",
    "labels": { "arcave-safety-snapshot": "true" },
    "created_at": "2026-06-01T21:22:37.009412Z"
  }
]
```

The name carries the id of the savepoint that rollback was applying, so a leftover always identifies the run that abandoned it.

```shell title="2. Apply it, by id"
arcave rollback 019e8491-04c8-7000-8000-000000000000
```

An id is the only reference that reaches a safety snapshot: `latest` and name lookups skip them.

```shell title="3. Once the environment is right, remove it"
arcave savepoint remove 019e8491-04c8-7000-8000-000000000000
```

Not before. Until then it is the last copy of the pre-rollback state.

**What the phase tells you.** The remedy above is the same in every case; the phase says how much was overwritten.

| Phase | What it means |
|---|---|
| `recreating` | Died between the teardown and the containers existing again. The volumes are untouched. |
| `restoring` | Died while writing volumes. The environment is partly overwritten. |
| `restore-done` | Every volume was written back; only the start did not finish. Starting the project and removing `.arcave/inflight.json` is enough, if that is what you want. |
| `inconsistent` | The automatic replay failed too. The safety snapshot is retained and is the last copy of the pre-rollback state. |

**A rollback of a project that had no containers** — never started, or taken down — takes no safety snapshot, so there is nothing to return to. The hint says so, and names the savepoint the rollback was applying:

```text
… a rollback into a compose project that was not running was interrupted; volumes may be half-restored; recover with `arcave rollback --session 00000000-0000-0000-0000-000000000001 -p myapp 019e843e-1650-7000-8000-000000000000`
```

Run that same rollback again; the marker lets it through, and it finishes what the first one started.

## `another arcave command is already running in this project`

Another arcave process holds the lock on `.arcave/lock`. Only commands that change something take it — `savepoint list`, `session list` and `session current` always run. The lock is not a queue — a second run fails rather than waiting.

```shell
ps aux | grep '[a]rcave'
```

Usual causes: a background `arcave mcp` server serving an agent, a CI job that has not finished, a fork (`session create --from`) still copying, a second terminal. The lock is released when the holding process exits, a crash included — it is an operating-system lock, not a file arcave has to clean up. If nothing is running, retry.

## `config is not found: run 'arcave init' if not yet initialized`

Either the project was never initialised, or you are not in its directory: `.arcave/config.yaml` is resolved against the working directory.

```shell
arcave init                                                    # initialise here
arcave --config /path/to/.arcave/config.yaml savepoint list    # or point at it
```

## `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME`

The `savepoint` commands and `rollback` act on one compose project, and nothing guesses which.

```shell
arcave -p myapp savepoint list
```

The name is what `docker compose ls` shows in the first column: the compose file's `name:` if it declares one, and the directory name otherwise. Before exporting `ARCAVE_PROJECT_NAME`, read what else [it names](environment_variables.md#arcave_project_name).

## `compose project "myapp": no containers found`

```text
compose project "myapp": no containers found: bring the compose project up once, or name another project with -p
```

**What happened.** `savepoint create` found no containers: `docker compose down` was run, or the project was never brought up. Down removes the containers, and with them everything arcave knows about the project.

```shell
docker compose up -d
```

arcave reads the running project from the daemon, not from your compose files. `docker compose stop` is fine — stopped containers still describe the project. A `rollback` needs no containers: it rebuilds the project from the savepoint.

## `compose project is not running but its volumes survive`

```text
compose project is not running but its volumes survive: myapp_pgdata
```

A rollback into a project with no containers found named volumes the savepoint would restore into — usually left behind by `docker compose down` — and [`rollback.leftover_volumes`](configuration.md#rollback) is `refuse`. Nothing was created. Remove the volumes if you do not need them (`docker volume rm myapp_pgdata`), or set `leftover_volumes: overwrite` to have the rollback write over them.

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
capture directory is not empty: 019e843e-1650-7000-8000-000000000000 holds 3 object(s), an interrupted run's remains; check what is there before removing it
```

An earlier run was interrupted after it started writing and before it committed a record. Its objects are the last copy of whatever that run captured. Look before removing:

```text
<storage.directory.path>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>/019e843e-1650-7000-8000-000000000000/
```

If there is no `savepoint.json` there, nothing lists what is in it and nothing puts it back. Its `heartbeat.json` says whether the run that wrote it failed, and [`arcave gc --dry-run`](cli/gc.md) names such a directory once its run is dead.

## `savepoint 019e843e-1650-7000-8000-000000000000 committed; services failed to start`

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

The grace period was probably too short when the savepoint was taken: arcave captures the volumes the moment the containers are down, so a container killed mid-shutdown is captured mid-shutdown — and that capture reported success, with a warning on stderr: `container <name> did not exit within <bound> and was killed`. The savepoint names it in `not_covered.killed_containers`.

The bound is `compose.stop_timeout`, raised by the longest `stop_grace_period` a service declares. Declare what the database needs on the service itself, or raise the floor for every service:

```yaml title="Give it room to close cleanly"
services:
  db:
    stop_grace_period: 60s
```

```yaml
compose:
    stop_timeout: 60s
```

Zero means "unset" and the default applies, so an immediate kill is spelled `1s`, not `0`.

## A rollback is refused: `savepoint carries no compose spec`

The project is rebuilt from the compose spec the savepoint recorded, and savepoints taken before arcave recorded one carry none. Their volume data is still on disk, but there is no supported path that puts it back. Take a fresh savepoint.

## `unsupported docker daemon` or `unsupported image archive format`

```text
unsupported docker daemon: the daemon reports version 24.0.7; arcave needs Docker 25 or newer
```

`savepoint create` and `rollback` refuse the daemon before anything is stopped or loaded. The second form comes from the capture itself, on a daemon whose version arcave could not read:

```text
unsupported image archive format: member "…" is neither a blob nor metadata; a daemon older than Docker 25 exports this shape
```

Either way the daemon is older than Docker 25. Upgrade it — see [Requirements](requirements.md#docker-25-or-newer).

## `invalid capture policy label`

```text
invalid capture policy label: arcave.capture="no" on service app: want true or false
```

An `arcave.*` label in the compose project is one arcave cannot act on: an unknown key, a value it does not accept, or a mount target the service does not mount. `savepoint create` refuses it before anything is stopped. Fix the label against [Capture labels](capture_labels.md) and recreate the containers with `docker compose up -d`, since arcave reads the labels off the running containers.

## `savepoint name is already taken`

```text
savepoint name is already taken: "pre-migration" names savepoint 019e843e-1650-7000-8000-000000000000
```

Names are unique within a compose project and session. Pick another name, or remove the old savepoint first. In a loop, put something unique in the name, such as a run id.

## `the current session no longer exists`

```text
init dependencies: resolve session: the current session no longer exists: 019e9a10-7c2e-7000-8000-000000000000, named by .arcave/current_session; run 'arcave session unset' or 'arcave session set <session>'
```

`.arcave/current_session` names a session that was deleted, from another machine or by hand in the store. arcave refuses rather than quietly falling back to `baseline`. Run `arcave session unset` to return to `baseline`, or `arcave session set <session>` to pick another.

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
resolve config: unmarshal config: invalid config: decoding failed due to the following error(s):

'project' has invalid keys: nam
```

Unknown keys are an error, not a value quietly ignored. Check the key against [Configuration](configuration.md); arcave never rewrites the file on its own, so fix it by hand. `arcave init --override` regenerates it from the defaults, but it takes no storage settings: if your storage pointed somewhere non-default, edit it back into the file afterwards, since savepoints under the old path stop being visible otherwise.

## `docker.host and docker.context are mutually exclusive`

```text
init dependencies: initialise docker client: docker.host and docker.context are mutually exclusive: "remote" already names a daemon, so remove one of the two
```

Both keys name a daemon. Keep the one you mean and delete the other from the [`docker`](configuration.md#docker) section.

## `init dependencies: initialise store root: open directory storage: stat /srv/arcave/myapp: no such file or directory`

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
