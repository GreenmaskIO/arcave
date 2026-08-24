---
title: Index
description: Reference for all arcave CLI commands — init, savepoint create, savepoint list, savepoint remove, rollback and mcp — plus the global flags, output contract and exit codes they share.
keywords: [arcave CLI, command reference, command line, arcave commands, docker compose, Open-Source, Greenmask]
---

# Commands

## Introduction

```shell title="arcave available commands"
arcave \
--project-name=<COMPOSE_PROJECT> \
--config=.arcave/config.yaml \
--log-level=[debug|info|warn] \
--log-format=[text|json] \
[init|savepoint|rollback|mcp]
```

You can use the following commands within arcave:

* [init](init.md) — creates `.arcave/config.yaml` for the current project. The one command that runs before arcave is configured.
* [savepoint create](savepoint_create.md) — captures a consistent snapshot of every stateful volume and every container's writable layer.
* [savepoint list](savepoint_list.md) — lists the project's savepoints, newest first.
* [savepoint remove](savepoint_remove.md) — deletes savepoints by id, or by retention rules.
* [rollback](rollback.md) — restores the environment to a savepoint, selected by id, by name, or by `latest`.
* [mcp](mcp.md) — starts the MCP server, exposing the workflows above as tools for an AI agent.

## Global flags

Every command accepts these. They are declared on the root command, so `arcave savepoint create --wait-timeout 5m` and `arcave --wait-timeout 5m savepoint create` are equivalent.

| Flag | Default | Config key | Description |
|---|---|---|---|
| `-p`, `--project-name` | — | *(none)* | The Docker Compose project to act on, as `docker compose ls` names it. Required by every command that touches a stack. Read from `ARCAVE_PROJECT_NAME` when the flag is absent. |
| `--config` | `.arcave/config.yaml` | *(none)* | Path to the config file, relative to the project root. |
| `--log-level` | `info` | `log.level` | `debug`, `info` or `warn`. |
| `--log-format` | `text` | `log.format` | `text` or `json`. |
| `--wait-timeout` | `2m` | `compose.wait_timeout` | How long to wait for services to become healthy after they are started, e.g. `2m` or `90s`. |
| `--stop-timeout` | `10s` | `compose.stop_timeout` | How long a container gets to exit on its own before it is killed. |
| `--progress` | `log` | `compose.progress` | Where compose's own progress goes: `log`, `tty` or `quiet`. |
| `--version` | — | *(none)* | Print the version, commit and build date. |
| `--help` | — | *(none)* | Show help for the command. |

Flags carrying a **config key** can also be set in `.arcave/config.yaml` or through `ARCAVE_<KEY>` environment variables. The precedence is: **a changed flag** beats **an environment variable** beats **the config file** beats **the default**. See [Configuration](../configuration.md).

`--project-name` and `--config` are deliberately unbound: they steer one run rather than describing the project. In particular, the compose project is not a config key — one directory may hold several stacks, and a persisted name would pin arcave to one of them.

:::note
Durations carry their unit — `90s`, `5m`, `1h30m`, anything Go's `time.ParseDuration` accepts. A bare number is an error, not a guess.
:::

## Naming the stack

Every command except `init` needs to know which compose project to act on:

```shell title="Per invocation"
arcave -p myapp savepoint create before-migration
```

```shell title="Once per shell"
export ARCAVE_PROJECT_NAME=myapp
arcave savepoint create before-migration
```

Without either, the command fails with `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME`.

:::warning
`ARCAVE_PROJECT_NAME` also feeds the `project.name` config key, which names the **storage tenant** rather than the stack. If the two are meant to differ, pass `-p` on the command line instead of exporting the variable. See [Configuration](../configuration.md#environment-variables).
:::

## Output and exit codes

**All results are JSON on stdout. All logs are on stderr.** That split is the contract: a script or an agent parses stdout and never has to pick data out of prose.

```shell title="stdout carries the result, and nothing else"
ID=$(arcave savepoint create nightly | jq -r .id)
```

The two exceptions: `arcave --version` prints a plain version line, and `arcave mcp` writes nothing at all to stdout — that stream is its JSON-RPC channel.

Compose's own per-container events arrive on stderr as ordinary arcave log records, in whatever `--log-format` says:

```text title="--log-format text"
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF compose kind=Container name=myapp-db-1 status=Started
INF compose kind=Container name=myapp-db-1 status=Healthy
```

```json title="--log-format json"
{"level":"info","kind":"Container","name":"myapp-db-1","status":"Healthy","message":"compose"}
```

Steps still in flight — `Waiting`, `Starting`, `Creating`, image-pull progress — are logged at debug level, so `--log-level debug` is how you watch a slow start unfold.

Exit codes:

```text
0  success
1  failure — the error is reported on stderr
```

## Prerequisites shared by every stack command

`savepoint create`, `savepoint list`, `savepoint remove` and `rollback` all require:

* **An initialised project.** Without `.arcave/config.yaml` the command fails with `config is not found: run 'arcave init' if not yet initialized`.
* **A named compose project**, as above.
* **A stack that exists on the daemon.** arcave reads the running stack rather than your compose files. `docker compose stop` is fine — stopped containers still describe the stack. `docker compose down` is not: it removes the containers, and with them everything arcave knows about the project.

Every mutating command additionally claims a non-blocking lock on `.arcave/lock`. A second arcave run against the same project fails immediately with `project is locked` rather than queuing.
