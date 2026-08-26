---
title: Configuration
description: Every key of .arcave/config.yaml — project, docker, compose, storage, snapshot, backends and log — with its default, its validation rule and the precedence between flags, environment and file.
keywords: [arcave configuration, config.yaml, snapshot backend, cdc, restic, storage, docker compose]
---

# Configuration

arcave is configured by `.arcave/config.yaml` under the project root, written by [`arcave init`](cli/init.md).

It is the one file arcave reads through the operating system rather than through its own store — it is what says where that store points, so it cannot live inside it.

```yaml title="A config with every section present"
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
    backend: cdc
backends:
    cdc:
        restore_mode: incremental
        overwrite: always
        image: restic/restic:0.19.1
        server_address: 127.0.0.1:0
        server_scheme: http
        container_host: host.docker.internal
        network_mode: auto
log:
    level: info
    format: text
```

**An unknown key is an error, not a value quietly ignored.** A typo fails the command rather than falling back to a default you did not choose:

```text
resolve config: unmarshal config: parse config: decoding failed due to the following error(s):

'project' has invalid keys: nam
```

arcave never rewrites this file on its own; that would clobber a path you set deliberately. `arcave init --override` is the one command that replaces it.

## Precedence

Four sources, highest first:

```text
1.  A changed command-line flag
2.  An ARCAVE_<KEY> environment variable
3.  .arcave/config.yaml
4.  The flag's default
```

Only **bound** flags feed the config. `--project-name`, `--config`, `--override`, `--dry-run` and `savepoint create --name` are unbound on purpose: they steer one run rather than describing the project. `savepoint create --name` in particular would otherwise collide with `init --name` on the shared `name` key, conflating a savepoint's name with the project's.

Which keys an environment variable can reach is narrower than it looks — [Environment variables](environment_variables.md) owns that.

## `project`

The storage tenant every store root is scoped by.

| Key | Type | Default | Description |
|---|---|---|---|
| `project.name` | string | the project directory's name, normalised | The tenant segment of the store path. |

Validated against an allowlist: it starts with `[a-z0-9]` and continues with `[a-z0-9_-]`. The directory name is normalised into that charset — `MyApp` becomes `myapp` — and a name that normalises to nothing is reported rather than guessed at.

The compose project is **not** here. It is named per invocation with `-p/--project-name`, because one project directory may hold several compose projects and a persisted name would pin arcave to one of them.

## `docker`

How arcave reaches the daemon. Honoured by both halves of the Docker client, so the SDK and the compose runner can never silently target different daemons.

| Key | Type | Default | Description |
|---|---|---|---|
| `docker.host` | string | `""` | Daemon endpoint, e.g. `unix:///var/run/docker.sock`. Empty means read the environment. |
| `docker.api_version` | string | `""` | Pin the API version. |
| `docker.context` | string | `""` | The Docker context to use. |
| `docker.cert_path` | string | `""` | A directory holding `ca.pem`, `cert.pem` and `key.pem` — the `DOCKER_CERT_PATH` convention. |
| `docker.ca_file` | string | `""` | Override the CA file on its own. |
| `docker.cert_file` | string | `""` | Override the certificate file on its own. |
| `docker.key_file` | string | `""` | Override the key file on its own. |
| `docker.tls_verify` | bool | `false` | Verify the daemon's TLS certificate. |

The three `*_file` keys exist for certificates that do not follow the `cert_path` naming convention.

## `compose`

How patient arcave is with the project, and how loudly compose reports its own progress.

| Key | Flag | Default | Description |
|---|---|---|---|
| `compose.wait_timeout` | `--wait-timeout` | `2m0s` | How long to wait for services to become healthy after a start. |
| `compose.stop_timeout` | `--stop-timeout` | `10s` | How long a container gets to exit on its own before it is killed. |
| `compose.progress` | `--progress` | `log` | Where compose's own progress goes: `log`, `tty` or `quiet`. |

**`wait_timeout` must be positive.** "Wait forever" is not on offer: it is exactly the failure this bound exists to turn into an error, since a crash-looping container with `restart: always` would otherwise hold a savepoint open indefinitely.

```text
compose.wait_timeout must be a positive duration, got -5s
```

**`stop_timeout` matters more here than it does to `docker compose stop`.** arcave captures the volumes the moment the containers are down, so a database killed mid-shutdown is captured mid-shutdown — and the command still reports success. Zero means "unset" and the default applies, so an immediate kill is spelled `1s`, not `0`. A negative value is refused:

```text
compose.stop_timeout must not be negative, got -1s
```

**`progress` selects a channel, not a format.** A log record is rendered by `log.format` like any other.

| Value | Effect |
|---|---|
| `log` | Each compose event becomes an arcave log record — one channel, one format. |
| `tty` | Compose's own redrawing renderer, at the cost of a second format on stderr. |
| `quiet` | Nothing but arcave's own log lines. |

All three keep compose off stdout, which carries the JSON result and nothing else.

```text
unsupported compose.progress "loud" (supported: log, tty, quiet)
```

## `storage`

Where savepoint data is written. A tagged union: `type` names the arm, and that arm must be present.

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.type` | string | `directory` | The storage arm. `directory` is the only one implemented. |
| `storage.directory.path` | string | `.arcave/storage` | A local filesystem path. Relative resolves against the project root; absolute is taken verbatim. |

The path you configure is not the path arcave writes to. Every store is rooted under a static `arcave` segment, and the full layout is in [Storage layout](storage_layout.md):

```text
<storage.directory.path>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>/
```

That segment is not configurable, and it is what bounds every write and every delete arcave makes: point the path at `/etc` and arcave operates in `/etc/arcave`. Every segment below it is checked against an allowlist, because a list of what a path segment must *not* be is never provably complete.

A **relative** path materialises on first use. An **absolute** path must already exist — that asymmetry keeps a typo from quietly growing a fresh tree somewhere unexpected.

```text
storage.type is "s3" but no storage.directory section is present
unsupported storage.type "s3" (supported: directory)
```

## `snapshot`

Which backend captures a **new** savepoint.

| Key | Type | Default | Description |
|---|---|---|---|
| `snapshot.backend` | string | `fullcopy` | `fullcopy` or `cdc`. |

A rollback reads the backend out of the record it is applying, so changing this never orphans an existing savepoint: you can switch to `cdc` today and still roll back to last week's `fullcopy` savepoints.

| | `fullcopy` | `cdc` |
|---|---|---|
| How it captures a volume | One `data.tar` per volume per savepoint, streamed through the daemon | Content-defined chunking into a restic repository that outlives any one savepoint |
| What a second savepoint costs | Full volume size, again | Only the chunks that changed |
| What it needs | Nothing beyond Docker | Pulls and runs `restic/restic` containers, which must reach a loopback port on your host |

Writable layers are deduplicated whichever backend is selected — that is not a property of this key. See [Storage layout](storage_layout.md#shared-blobs).

```text
unsupported snapshot.backend "restic" (supported: fullcopy, cdc)
```

## `backends`

One optional subsection per backend. A project that never uses a backend does not configure it, and an absent section is not an error — arcave resolves the defaults either way, because a rollback restores through the backend its record names.

### `backends.cdc`

| Key | Type | Default | Description |
|---|---|---|---|
| `restore_mode` | string | `incremental` | `incremental` writes over the live data, sending only what differs and deleting what the savepoint does not carry. `wipe` clears the mount and writes it back whole. |
| `overwrite` | string | `always` | Governs an incremental restore only. `always` rewrites every file the savepoint carries; `if-changed` compares content and rewrites only what differs. |
| `image` | string | `restic/restic:0.19.1` | The restic image every one-shot container runs. `--delete`/`--overwrite` need restic 0.17 or newer. |
| `server_address` | string | `127.0.0.1:0` | What arcave's restic server binds. Must be `host:port`. |
| `server_scheme` | string | `http` | The scheme the container fetches the repository over: `http`, or `https` for a TLS proxy in front of it. |
| `container_host` | string | `host.docker.internal` | That server's hostname **as the container sees it**, on the bridge path. Ignored under host networking. |
| `network_mode` | string | `auto` | How a restic container reaches the server: `auto`, `host` or `bridge`. |

With the defaults, a restore writes over the live data and rewrites every file the savepoint carries — `overwrite: if-changed` is what turns that into a comparison.

**Why port `0`.** The kernel picks the port. A fixed one would collide between two arcave processes — a CLI run and an `arcave mcp` server, or two projects — and would be a predictable address on your loopback for anything else on the machine.

**What `network_mode` decides.** A restic container has to reach a server on your host loopback, and how it does that differs per platform.

| Value | Behaviour |
|---|---|
| `auto` | Probes the daemon while the command wires itself up: a native Linux daemon gets host networking, anything else the bridge and `container_host`. The default, because one config then runs everywhere. |
| `host` | Joins the containers to the host's network namespace, so the loopback bind is reachable as `127.0.0.1`. Linux daemons only. |
| `bridge` | Keeps the default network and reaches the host by `container_host`. The way out for a remote or rootless daemon, which `auto` reads as local. |

The server itself speaks plain HTTP. `server_scheme: https` is for putting a TLS proxy in front of it, not for making arcave serve TLS.

On Docker Desktop for macOS the `cdc` backend cannot capture a **host bind mount** — see [Limitations](limitations.md#cdc-and-host-bind-mounts-on-macos).

```text
unsupported backends.cdc.restore_mode "partial" (supported: wipe, incremental)
unsupported backends.cdc.overwrite "never" (supported: if-changed, always)
backends.cdc.server_address "127.0.0.1" is not a host:port address: …
unsupported backends.cdc.network_mode "none" (supported: auto, host, bridge)
```

## `log`

| Key | Flag | Default | Description |
|---|---|---|---|
| `log.level` | `--log-level` | `info` | `debug`, `info` or `warn`. |
| `log.format` | `--log-format` | `text` | `text` or `json`. |

Logs go to stderr; results go to stdout as JSON. `--log-format json` makes the whole stderr stream machine-readable, which is what a CI job or an agent wants.

## Next step

[Environment variables](environment_variables.md) — which of these keys `ARCAVE_<KEY>` can actually reach.
