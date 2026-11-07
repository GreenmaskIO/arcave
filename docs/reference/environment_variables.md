---
title: Environment variables
description: The ARCAVE_ variables arcave reads — every config key has one — the two different things ARCAVE_PROJECT_NAME names, ARCAVE_SESSION, ARCAVE_FORMAT, ARCAVE_PROGRESS and ARCAVE_ENCRYPTION_KEY.
keywords: [arcave environment variables, ARCAVE_PROJECT_NAME, ARCAVE_SESSION, ARCAVE_FORMAT, ARCAVE_PROGRESS, ARCAVE_ENCRYPTION_KEY, config keys, docker compose]
---

# Environment variables

A config key is set in the environment as `ARCAVE_<KEY>`, with dots replaced by underscores: `ARCAVE_LOG_LEVEL` feeds `log.level`. An environment variable beats `.arcave/config.yaml` and loses to a flag you actually passed.

## Which keys a variable reaches

Every key of the config, whether or not the file carries it: arcave binds each one to its `ARCAVE_<KEY>` variable at start, so `ARCAVE_BACKENDS_CDC_RESTORE_MODE=wipe` sets `backends.cdc.restore_mode` in a project whose file has no `backends` section at all. A variable naming no config key is ignored.

```shell
ARCAVE_SNAPSHOT_BACKEND=cdc arcave -p myapp savepoint create nightly
```

No variable reaches the file `arcave init` writes — it is a template of the defaults and `--name`; `ARCAVE_LOG_LEVEL` and `ARCAVE_LOG_FORMAT` steer only its own log.

## `ARCAVE_PROJECT_NAME`

This one variable names two different things.

* **The compose project**, which `savepoint create`, `savepoint list`, `savepoint remove` and `rollback` act on — the same thing `-p/--project-name` names, as `docker compose ls` shows it.
* **The storage tenant** `project.name`, the segment of the store path that keeps two projects sharing one storage location apart.

They are usually the same string, and then this is invisible. When they must differ, pass `-p` on the command line and leave the variable unset:

```shell title="One compose project, one tenant — the ordinary case"
export ARCAVE_PROJECT_NAME=myapp
arcave savepoint create nightly
```

```shell title="Two compose projects under one tenant"
arcave -p myapp-blue savepoint create nightly
arcave -p myapp-green savepoint create nightly
```

Both compose projects store under the tenant `project.name` from the config file, in [store roots](storage_layout.md) that differ in their last segment. Exporting `ARCAVE_PROJECT_NAME=myapp-blue` instead would have moved the tenant too, and the green project's savepoints would have gone somewhere else.

## `ARCAVE_SESSION`

The session a command works in, by id or by name — the same thing `--session` names, and beaten by it. It is not a config key. Unset, a command works in the current session `arcave session set` chose, else in `baseline`. See [`arcave session`](cli/session.md#naming-the-session-a-command-works-in).

```shell title="Work in one session for the rest of the shell"
export ARCAVE_SESSION=feat/add-new-table
arcave -p myapp savepoint create step-1
```

For [`arcave mcp`](cli/mcp.md) it pins the server: every tool call given no `session` works in that session, and `session_set` and `session_unset` are refused.

## `ARCAVE_FORMAT` and `ARCAVE_PROGRESS`

How a run prints its result and shows its progress — the same things `--format` and `--progress` name, and beaten by them. Neither is a config key: they describe one run's output, not the project.

| Variable | Values | Unset |
|---|---|---|
| `ARCAVE_FORMAT` | `auto`, `json`, `text` | `auto`: text while the live tree is drawn and stdout is a terminal, else JSON |
| `ARCAVE_PROGRESS` | `auto`, `tty`, `log`, `json` | `auto`: the live tree on a terminal stderr with `log.format: text`, else log records |

```shell title="JSON results on a terminal, for the rest of the shell"
export ARCAVE_FORMAT=json
```

See [Output and exit codes](output.md).

## `ARCAVE_ENCRYPTION_KEY`

The store key, `encryption.key`: a secret [`arcave init`](cli/init.md) never writes, read from the environment like every key. A `key:` in the file is the fallback. See [`encryption`](configuration.md#encryption).

## Variables that are not arcave's

`DOCKER_HOST` and `DOCKER_CONTEXT` are read by the Docker client when the [`docker` section](configuration.md#docker) leaves the matching key empty, which is the default.

`DOCKER_TLS_VERIFY` and `DOCKER_CERT_PATH` count only beside a `tcp://` `DOCKER_HOST`, or one with no scheme, which the Docker client reads as `tcp://`, with no `docker.host` or `docker.context` set; a Docker context carries its own TLS settings. `DOCKER_TLS_VERIFY`, at any non-empty value, brings in `DOCKER_CERT_PATH`, which acts as `docker.cert_path`, under the same requirement of all three files, unless `docker.cert_path`, `ca_file`, `cert_file` or `key_file` is set. Alone, `DOCKER_CERT_PATH` turns nothing on. Unlike the docker CLI, it has no `~/.docker` default, so `DOCKER_TLS_VERIFY` without it is refused. `DOCKER_TLS` is not read.

Podman's `CONTAINER_HOST` is not read: see [Connecting to Podman](requirements.md#connecting-to-podman).

`ARCAVE_VERSION`, `ARCAVE_INSTALL_DIR` and `ARCAVE_BASE_URL` belong to the install script, not to the CLI.

## Next step

[Output and exit codes](output.md) — what a command writes, and where.
