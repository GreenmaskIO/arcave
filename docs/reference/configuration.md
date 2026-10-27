---
title: Configuration
description: Every key of .arcave/config.yaml — project, common, docker, compose, storage (directory, s3, azure, ssh), snapshot, writable_layers, capture, rollback, backends and log — with its default, its validation rule, ${VAR} interpolation and the precedence between flags, environment and file.
keywords: [arcave configuration, config.yaml, snapshot backend, cdc, restic, storage, s3, azure, sftp, docker compose]
---

# Configuration

arcave is configured by `.arcave/config.yaml` under the project root, written by [`arcave init`](cli/init.md).

It is the one file arcave reads through the operating system rather than through its own store — it is what says where that store points, so it cannot live inside it.

```yaml title="A config with every section present"
project:
    name: myapp
common:
    concurrency: 5
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
writable_layers:
    backend: dedupcopy
capture:
    writable_layers: true
    images: true
    volumes:
        named: true
        bind: true
        anonymous: true
rollback:
    leftover_volumes: overwrite
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
resolve config: unmarshal config: invalid config: decoding failed due to the following error(s):

'project' has invalid keys: nam
```

Every config error is reported after that same `resolve config: unmarshal config: invalid config: ` prefix, which the examples below leave out. The `docker` and `log` errors are not config errors and are quoted whole.

arcave never rewrites this file on its own; that would clobber a path you set deliberately. `arcave init --override` is the one command that replaces it.

## Precedence

Four sources, highest first:

```text
1.  A changed command-line flag
2.  An ARCAVE_<KEY> environment variable
3.  .arcave/config.yaml
4.  The flag's default
```

Only **bound** flags feed the config. `--project-name`, `--session`, `--config`, `--override`, `--dry-run`, `init --name` and `savepoint create --name` are unbound on purpose: they steer one run rather than describing the project. `init` binds none of its flags: it writes a template, and reads neither the environment nor the existing file.

Which keys an environment variable can reach is narrower than it looks — [Environment variables](environment_variables.md) owns that.

## `project`

The storage tenant every store root is scoped by.

| Key | Type | Default | Description |
|---|---|---|---|
| `project.name` | string | the project directory's name, normalised | The tenant segment of the store path. |

Validated against an allowlist: it starts with `[a-z0-9]` and continues with `[a-z0-9_-]`. The directory name is normalised into that charset — `MyApp` becomes `myapp` — and a name that normalises to nothing is reported rather than guessed at.

The compose project is **not** here. It is named per invocation with `-p/--project-name`, because one project directory may hold several compose projects and a persisted name would pin arcave to one of them.

## `common`

Settings every workflow shares.

| Key | Type | Default | Description |
|---|---|---|---|
| `common.concurrency` | int | `5` | How many volumes, writable layers or images a capture or a restore works on at once. |

```text
common.concurrency must be at least 1, got -1
```

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

`docker.host` and `docker.context` both name a daemon, so set at most one; a client certificate needs both halves. These are checked when a command connects, not when the config is read:

```text
initialise docker client: docker.host and docker.context are mutually exclusive: "remote" already names a daemon, so remove one of the two
initialise docker client: docker.cert_file is set but docker.key_file is missing: a client certificate needs both, or set docker.cert_path to supply the pair from one directory
```

## `compose`

How patient arcave is with the project, and how loudly compose reports its own progress.

| Key | Flag | Default | Description |
|---|---|---|---|
| `compose.wait_timeout` | `--wait-timeout` | `2m0s` | How long to wait for services to become healthy after a start. |
| `compose.stop_timeout` | `--stop-timeout` | `10s` | The least time a container gets to exit on its own before it is killed; a service's `stop_grace_period` raises it. |
| `compose.progress` | `--progress` | `log` | Where compose's own progress goes: `log`, `tty` or `quiet`. |

**`wait_timeout` must be positive.** "Wait forever" is not on offer: it is exactly the failure this bound exists to turn into an error, since a crash-looping container with `restart: always` would otherwise hold a savepoint open indefinitely.

```text
compose.wait_timeout must be a positive duration, got -5s
```

**`stop_timeout` matters more here than it does to `docker compose stop`.** arcave captures the volumes the moment the containers are down, so a database killed mid-shutdown is captured mid-shutdown — and the command still reports success, with a warning naming the killed container. It is a floor: every stop waits up to the longest `stop_grace_period` the project's services declare, when that is longer. Zero means "unset" and the default applies, so an immediate kill is spelled `1s`, not `0`. A negative value is refused:

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

Where savepoint data is written. A tagged union: `type` names the arm, that arm must be present, and any other arm is dropped. Each arm is one backend of [`github.com/greenmaskio/storages`](https://github.com/greenmaskio/storages).

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.type` | string | `directory` | `directory`, `s3`, `azure` or `ssh`. |

### `storage.directory`

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.directory.path` | string | `.arcave/storage` | A local filesystem path. Relative resolves against the project root; absolute is taken verbatim. |

A **relative** path materialises on first use. An **absolute** path must already exist — that asymmetry keeps a typo from quietly growing a fresh tree somewhere unexpected.

### `storage.s3`

An S3-compatible bucket: AWS, MinIO, and the like.

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.s3.bucket` | string | *(required)* | The bucket. It must exist. |
| `storage.s3.prefix` | string | `""` | A key prefix inside the bucket. |
| `storage.s3.region` | string | `""` | The bucket's region. |
| `storage.s3.endpoint` | string | `""` | A non-AWS endpoint, `http://minio:9000`. Must carry no user info or query string. |
| `storage.s3.force_path_style` | bool | `true` | Path-style addressing, which MinIO needs. |
| `storage.s3.access_key_id` | string | `""` | With `secret_access_key`, a static credential. Both empty: the AWS default chain (environment, profile, instance role). |
| `storage.s3.secret_access_key` | string | `""` | **Secret.** |
| `storage.s3.session_token` | string | `""` | **Secret.** |
| `storage.s3.role_arn`, `session_name` | string | `""` | A role to assume. |
| `storage.s3.storage_class` | string | `STANDARD` | The class every object is written with. |
| `storage.s3.sse`, `kms_key_arn`, `bucket_key_enabled` | | `""` | Server-side encryption: `AES256`, `aws:kms` or `aws:kms:dsse`, and the KMS key. |
| `storage.s3.disable_ssl`, `no_verify_ssl`, `cert_file` | | | TLS: plain HTTP, no certificate check, a CA bundle. |
| `storage.s3.max_retries`, `max_part_size`, `concurrency` | int | `3`, 50 MiB, `0` | Request tuning. |

### `storage.azure`

An Azure Blob Storage container.

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.azure.container` | string | *(required)* | The container. It must exist. |
| `storage.azure.storage_account` | string | *(required)* | The account. |
| `storage.azure.prefix` | string | `""` | A blob prefix inside the container. |
| `storage.azure.access_key` | string | `""` | **Secret.** The shared key. |
| `storage.azure.sas_token` | string | `""` | **Secret.** Used when there is no `access_key`; with neither, `DefaultAzureCredential`. |
| `storage.azure.endpoint` | string | `""` | The service URL, path-style: `http://azurite:10000/devstoreaccount1`. Must carry no user info or query string — a SAS token goes in `sas_token`. |
| `storage.azure.endpoint_suffix` | string | `""` | A sovereign cloud's endpoint suffix. |
| `storage.azure.environment_name` | string | `AzurePublicCloud` | A sovereign cloud by name. |
| `storage.azure.max_retries` | int | `3` | Retries after a failed try; negative for none. |

### `storage.ssh`

A directory on an SFTP server.

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.ssh.host` | string | *(required)* | The server. |
| `storage.ssh.port` | int | `22` | |
| `storage.ssh.user` | string | *(required)* | |
| `storage.ssh.password` | string | `""` | **Secret.** One of `password` or `private_key_path` is required. |
| `storage.ssh.private_key_path` | string | `""` | A private key file. |
| `storage.ssh.private_key_passphrase` | string | `""` | **Secret.** Decrypts an encrypted key. |
| `storage.ssh.prefix` | string | `""` | The remote directory; relative resolves against the user's home. |
| `storage.ssh.connect_timeout` | duration | `30s` | Bounds the TCP dial and the SSH handshake. |

The server's host key is **not** verified — arcave warns `ssh storage does not verify the server's host key` — so use `ssh` on a network you trust.

```text
storage.ssh needs password or private_key_path
storage.ssh.private_key_passphrase needs private_key_path
storage.ssh.connect_timeout must not be negative, got -1s
```

### Layout and reachability

The path or prefix you configure is not the path arcave writes to. Every store is rooted under a static `arcave` segment, and the full layout is in [Storage layout](storage_layout.md):

```text
<path or prefix>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>/
```

That segment is not configurable, and it is what bounds every write and every delete arcave makes: point the path at `/etc` and arcave operates in `/etc/arcave`. Every segment below it is checked against an allowlist, because a list of what a path segment must *not* be is never provably complete.

Every command that opens the store reaches it first — a `HEAD` on the bucket, a property read on the container, an SFTP session — so a wrong endpoint or a refused credential fails before anything is stopped.

```text
storage.type is "s3" but no storage.s3 section is present
storage.s3.bucket is required
storage.s3.endpoint must not carry user info; set the credential fields instead
storage.azure.endpoint must not carry a query string; put a SAS token in storage.azure.sas_token
unsupported storage.type "gcs" (supported: directory, s3, azure, ssh)
```

### Credentials

Keep a secret out of the file by naming its environment variable — see [Interpolation](#interpolation). `arcave init` never writes one: it writes a template of the defaults and `--name`, with the `directory` storage, so a credential reaches the file only when you put it there.

## Interpolation

Any string value may name an environment variable, expanded when the config is read:

| Form | Expands to |
|---|---|
| `${VAR}`, `$VAR` | The variable's value. **Unset is an error**, never an empty string. |
| `${VAR:offset}`, `${VAR:offset:length}` | A substring of the value. Unset is an error. |
| `${VAR:-default}` | The value, or `default` when unset or empty. `${VAR:-}` says "empty" explicitly. |
| `${VAR-default}` | The value, or `default` when unset. |
| `${VAR?message}` | The value; unset fails with `message`. |
| `$$` | A literal `$`. |

```yaml
storage:
  type: s3
  s3:
    bucket: savepoints
    access_key_id: AKIAEXAMPLE
    secret_access_key: ${AWS_SECRET_ACCESS_KEY}
```

An unset variable fails the command:

```text
environment variable AWS_SECRET_ACCESS_KEY is not set (write ${AWS_SECRET_ACCESS_KEY:-} for an empty default, or $$ for a literal $)
```

A value with a `$` that is not a reference — a password like `pa$word` — is written `pa$$word`. That holds wherever the value comes from: an `ARCAVE_*` variable or a flag is interpolated the same way as the file, so `ARCAVE_STORAGE_SSH_PASSWORD='pa$$word'`.

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

This key covers volumes only; writable layers are [`writable_layers`](#writable_layers). A label can pick another backend for one volume — see [Capture labels](capture_labels.md).

```text
unsupported snapshot.backend "restic" (supported: fullcopy, cdc)
```

## `writable_layers`

How a **new** savepoint stores each container's writable layer.

| Key | Type | Default | Description |
|---|---|---|---|
| `writable_layers.backend` | string | `dedupcopy` | `dedupcopy` or `fullcopy`. |

`dedupcopy` splits each committed layer into the blob store every savepoint of the compose project shares, so a base layer every savepoint carries is written once — see [Storage layout](storage_layout.md#shared-blobs). `fullcopy` stores each layer whole, as one `image.tar`. The service label `arcave.writable-layer.backend` overrides it for one service. The images services run on are always stored deduplicated, whatever this key says. Like `snapshot.backend`, it applies to new savepoints only: a rollback reads each layer back the way its record says it was stored.

```text
unsupported writable_layers.backend "zstd" (supported: dedupcopy, fullcopy)
```

## `capture`

Which kinds of unit a savepoint captures when no [capture label](capture_labels.md) says otherwise. Everything is on by default.

| Key | Type | Default | Description |
|---|---|---|---|
| `capture.writable_layers` | bool | `true` | Every container's writable layer. |
| `capture.images` | bool | `true` | The image each service's `image:` names. |
| `capture.volumes.named` | bool | `true` | Named volumes. |
| `capture.volumes.bind` | bool | `true` | Bind mounts. |
| `capture.volumes.anonymous` | bool | `true` | Anonymous volumes. |

These are the defaults a label overrides: `capture.volumes.bind: false` with `arcave.mounts.include: /app/data` on one service captures that one bind and no other. They steer a running compose project without recreating it, which a label cannot. What a savepoint left out is listed in its `not_covered`.

## `rollback`

| Key | Type | Default | Description |
|---|---|---|---|
| `rollback.leftover_volumes` | string | `overwrite` | What a rollback does when the compose project has no containers but still owns named volumes the savepoint restores into: `overwrite` them, with a warning naming each, or `refuse`. |

It matters only for a compose project that is not running — never started, or taken down with `docker compose down`, which leaves named volumes behind. A running project is always overwritten; that is what a rollback is for. `refuse` fails before anything is created:

```text
compose project is not running but its volumes survive: myapp_pgdata
```

```text
unsupported rollback.leftover_volumes "keep" (supported: overwrite, refuse)
```

## `gc`

| Key | Type | Default | Description |
|---|---|---|---|
| `gc.restored_layer_grace` | duration | `1h` | How long ago an unused layer tag must have been tagged before [`arcave gc`](cli/gc.md) counts it as garbage: a rollback in another checkout sharing `project.name` and the daemon may have just loaded it. `0` counts every unused tag. No other kind has a grace: gc judges by the savepoints' status, never by age. |
`--restored-layer-grace` overrides it for one run. What gc looks at is not configured: it looks at every kind of data, and `--kind` narrows one run.

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

Both are checked when a command starts, and an unknown value fails it rather than silencing the log:

```text
set log level: get logger: log level trace: unknown log level
set log level: get logger: log format yaml: unknown log format
```

Logs go to stderr; results go to stdout as JSON. `--log-format json` makes the whole stderr stream machine-readable, which is what a CI job or an agent wants.

## Next step

[Environment variables](environment_variables.md) — which of these keys `ARCAVE_<KEY>` can actually reach.
