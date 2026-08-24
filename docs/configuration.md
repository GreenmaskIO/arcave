---
title: Configuration
description: Reference for .arcave/config.yaml — project, docker, compose, storage, snapshot and backend sections — plus flag, environment and file precedence.
keywords: [arcave configuration, config.yaml, snapshot backend, cdc, restic, docker compose, storage, Open-Source, Greenmask]
---

# Configuration

arcave is configured by `.arcave/config.yaml` under the project root, written by [`arcave init`](commands/init.md).

It is the one file arcave reads through the operating system rather than through its own storage backend — it is what says where that backend points, so it cannot live inside it.

```yaml title="A complete config, with every section present"
project:
    name: myapp
docker:
    host: unix:///var/run/docker.sock
compose:
    wait_timeout: 2m0s
    stop_timeout: 10s
    progress: log
storage:
    type: directory
    directory:
        path: .arcave/storage
snapshot:
    backend: fullcopy
backends:
    cdc:
        restore_mode: wipe
        overwrite: if-changed
        image: restic/restic:0.19.1
        server_address: 127.0.0.1:0
        server_scheme: http
        container_host: host.docker.internal
        network_mode: auto
log:
    level: info
    format: text
```

:::warning
**Unknown keys are an error, not a value silently ignored.** A typo in a key name fails the command rather than falling back to a default you did not choose. arcave also never rewrites this file on its own — that would clobber a path you set deliberately.
:::

## Precedence

Four sources, highest first:

```text
1.  A changed command-line flag
2.  An ARCAVE_<KEY> environment variable
3.  .arcave/config.yaml
4.  The flag's default
```

Only **bound** flags feed the config. `--project-name`, `--config`, `--override`, `--dry-run` and `savepoint create --name` are deliberately unbound: they steer one run rather than describing the project.

## Environment variables

Any config key can be set as `ARCAVE_<KEY>`, with dots replaced by underscores.

| Variable | Config key |
|---|---|
| `ARCAVE_LOG_LEVEL` | `log.level` |
| `ARCAVE_LOG_FORMAT` | `log.format` |
| `ARCAVE_COMPOSE_WAIT_TIMEOUT` | `compose.wait_timeout` |
| `ARCAVE_COMPOSE_STOP_TIMEOUT` | `compose.stop_timeout` |
| `ARCAVE_COMPOSE_PROGRESS` | `compose.progress` |
| `ARCAVE_PROJECT_NAME` | `project.name` — **and** the compose project, see below |

`ARCAVE_INSTALL_DIR` and `ARCAVE_BASE_URL` belong to the [install script](installation.md#via-install-script), not to the CLI.

:::warning
**`ARCAVE_PROJECT_NAME` is read twice.** arcave reads it directly as the **compose project** to act on, which is what the [Getting started](getting_started.md#3-name-the-stack-once) page uses it for. It *also* matches the `project.name` config key, which names the **storage tenant** — so exporting it overrides that too.

They are usually meant to be the same string, and then this is invisible. If they must differ, pass `-p/--project-name` on the command line and leave the variable unset.
:::

## `project` section

The storage tenant every store root is scoped by.

| Key | Type | Default | Description |
|---|---|---|---|
| `project.name` | string | the project directory name | The tenant segment of the store path. Validated against an allowlist. |

The compose stack is **not** here. It is named per invocation with `-p/--project-name`, because one project directory may hold several stacks and a persisted name would pin arcave to one of them. See [Concepts](concepts.md#project-versus-stack).

If the directory name normalises to nothing, arcave asks you to set `project.name` explicitly rather than guessing.

## `docker` section

How arcave reaches the daemon. Honoured by both halves of the Docker client, so the SDK and the compose runner can never silently target different daemons.

| Key | Type | Default | Description |
|---|---|---|---|
| `docker.host` | string | `""` | Daemon endpoint, e.g. `unix:///var/run/docker.sock`. Empty means read the environment. |
| `docker.api_version` | string | `""` | Pin the API version. |
| `docker.context` | string | `""` | Docker context to use. |
| `docker.cert_path` | string | `""` | Directory holding `ca.pem`, `cert.pem`, `key.pem` — the `DOCKER_CERT_PATH` convention. |
| `docker.ca_file` | string | `""` | Override the CA file individually. |
| `docker.cert_file` | string | `""` | Override the certificate file individually. |
| `docker.key_file` | string | `""` | Override the key file individually. |
| `docker.tls_verify` | bool | `false` | Verify the daemon's TLS certificate. |

The three `*_file` keys exist for certificates that do not follow the `cert_path` naming convention.

## `compose` section

How patient arcave is with the stack, and how loudly compose reports its own progress.

| Key | Flag | Default | Description |
|---|---|---|---|
| `compose.wait_timeout` | `--wait-timeout` | `2m` | How long to wait for services to become healthy after a start. |
| `compose.stop_timeout` | `--stop-timeout` | `10s` | How long a container gets to exit on its own before SIGKILL. |
| `compose.progress` | `--progress` | `log` | Where compose's progress goes: `log`, `tty` or `quiet`. |

**`wait_timeout` must be positive.** "Wait forever" is not on offer — it is exactly the failure mode this bound exists to turn into an error, and a crash-looping container with `restart: always` would otherwise block a savepoint indefinitely.

**`stop_timeout` matters more here than for plain `docker compose stop`.** arcave captures the volumes the moment the containers are down, so a database killed mid-shutdown is captured mid-shutdown. Zero means "unset" and the default applies, so an immediate kill is spelled `1s`, not `0`.

**`progress` selects a channel, not a format.** A log record is rendered by `log.format` like every other:

| Value | Effect |
|---|---|
| `log` | Each compose event becomes an arcave log record — one output channel, one format. |
| `tty` | Compose's own redrawing renderer, at the cost of a second format on stderr. |
| `quiet` | Nothing but arcave's own log lines. |

All three keep compose off stdout, which carries nothing but arcave's JSON result.

## `storage` section

Where savepoint data is written. A **tagged union**: `type` names the arm, and exactly that arm must be present.

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.type` | string | `directory` | The storage arm. `directory` is the only one implemented. |
| `storage.directory.path` | string | `.arcave/storage` | Local filesystem path. Relative paths resolve against the project root; absolute paths are honoured verbatim. |

The path you configure is not the path arcave writes to. Every store is rooted under a static `arcave` segment:

```text title="The real store root"
<storage.directory.path>/arcave/projects/<project>/sessions/global/compose/<stack>/
```

That segment is not configurable, and it is the whole safety story: point the path at `/etc` and arcave operates in `/etc/arcave`, never in `/etc`. Every segment below it is validated against an allowlist, because a list of what a path segment must *not* be is never provably complete.

:::warning
A savepoint store may not be `/`, a shallow system directory, `$HOME`, or any ancestor of the project.
:::

A **relative** path materialises on first use. An **absolute** path must already exist — that asymmetry keeps a typo from quietly growing a fresh tree somewhere unexpected.

## `snapshot` section

Which backend owns the read-and-write pipeline for a **new** savepoint.

| Key | Type | Default | Description |
|---|---|---|---|
| `snapshot.backend` | string | `fullcopy` | `fullcopy` or `cdc`. |

A rollback reads the backend out of the record it is restoring, so changing this never orphans an existing savepoint. You can switch to `cdc` today and still roll back to last week's `fullcopy` savepoints.

### Snapshot backends

| | `fullcopy` | `cdc` |
|---|---|---|
| How it captures | One `data.tar` per volume per savepoint, streamed through the Docker engine | Content-defined chunking into a per-volume restic repository |
| Cost per savepoint | Full size, every time | Only the chunks that changed |
| Dependencies | None beyond Docker | Pulls and runs `restic/restic` containers; needs a loopback port reachable from them |
| Good for | Occasional checkpoints, small volumes | Tight loops, large volumes, many savepoints |

The `cdc` repositories live inside the same session root, at `<stack root>/restic/<object-id>` — one repository per volume, beside the savepoint directories. That keeps a session self-contained and everything in it dying together, at the price of a repository that cannot be shared across sessions.

## `backends` section

One optional subsection per snapshot backend. A project that never uses a backend does not have to configure it, and an absent section is not an error.

### `backends.cdc`

| Key | Type | Default | Description |
|---|---|---|---|
| `restore_mode` | string | `wipe` | `wipe` clears the mount and restores it whole. `incremental` restores over the live data, writing only what differs and deleting what the snapshot does not carry. |
| `overwrite` | string | `if-changed` | Governs an **incremental** restore only. `if-changed` compares content and rewrites only what differs; `always` rewrites every file the snapshot carries. |
| `image` | string | `restic/restic:0.19.1` | The restic image every one-shot container runs. Needs restic ≥ 0.17 for `--delete`/`--overwrite`. |
| `server_address` | string | `127.0.0.1:0` | What arcave's restic server binds. Must be `host:port`. |
| `server_scheme` | string | `http` | The scheme the container fetches the repository over: `http`, or `https` for a TLS proxy in front. |
| `container_host` | string | `host.docker.internal` | The server's hostname **as the container sees it**, on the bridge path. Ignored under host networking. |
| `network_mode` | string | `auto` | How a restic container reaches the server: `auto`, `host` or `bridge`. |

**Why port `0`.** The kernel picks the port. A fixed one would collide between two arcave processes — a CLI run and an `arcave mcp` server, or two projects — and would give an attacker a predictable target.

**What `network_mode` decides.** A restic container has to reach a server on your host loopback, and how it does that differs per platform:

| Value | Behaviour |
|---|---|
| `auto` | Probes the daemon at wiring time: a native Linux daemon gets host networking, anything else gets the bridge and `container_host`. The default, because the same config then runs everywhere. |
| `host` | Joins the containers to the host's network namespace, so the loopback bind is reachable as `127.0.0.1`. Linux daemons only. |
| `bridge` | Keeps the default network and reaches the host by `container_host`. The escape hatch for remote daemons and rootless setups. |

The server itself speaks plain HTTP. `server_scheme: https` is for putting a TLS proxy in front of it, not for making arcave serve TLS.

:::warning
On Docker Desktop for macOS, the `cdc` backend cannot capture a **host bind mount**. restic sets `O_NOATIME` on each source file, which VirtioFS answers with `EIO`. Named volumes live in the VM's own filesystem and are unaffected. See [Limitations](limitations.md#cdc-and-host-bind-mounts-on-macos).
:::

## `log` section

| Key | Flag | Default | Description |
|---|---|---|---|
| `log.level` | `--log-level` | `info` | `debug`, `info` or `warn`. |
| `log.format` | `--log-format` | `text` | `text` or `json`. |

Logs go to **stderr**. Results go to stdout as JSON. `--log-format json` makes the whole stderr stream machine-readable, which is what a CI job or an agent wants.

## Related docs

* [arcave init](commands/init.md) — writes this file.
* [Commands](commands/index.md#global-flags) — the flags that override these keys.
* [Concepts](concepts.md) — project versus stack, sessions, stores and backends.
* [Limitations](limitations.md) — the constraints these settings cannot lift.
