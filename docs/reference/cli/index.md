---
title: CLI reference
description: The arcave command tree, the global flags every command shares, how the compose project is named, and what each command needs before it runs.
keywords: [arcave CLI, command reference, global flags, docker compose, savepoint]
---

# CLI reference

```text title="The shape of every invocation"
arcave [global flags] <command> [command flags] [arguments]
```

Global flags are declared on the root command, so `arcave savepoint create --wait-timeout 5m` and `arcave --wait-timeout 5m savepoint create` are the same run.

## Commands

| Command | What it does |
|---|---|
| [`init`](init.md) | Writes `.arcave/config.yaml`. The one command that runs before arcave is configured. |
| [`savepoint create`](savepoint_create.md) | Captures every stateful volume, every container's writable layer and the project's compose spec. |
| [`savepoint list`](savepoint_list.md) | Prints the project's committed savepoints. |
| [`savepoint remove`](savepoint_remove.md) | Deletes savepoints by id, or by retention rules. |
| [`rollback`](rollback.md) | Returns the environment to a savepoint, selected by id, by name, or by `latest`. |
| [`session`](session.md) | Creates, lists and deletes sessions — the independent lines of savepoints inside a project. |
| [`mcp`](mcp.md) | Serves the workflows above as MCP tools for an agent. |
| [`status`](status.md) | Planned. Registered, and returns `not implemented`. |
| `completion` | Cobra's shell-completion script generator, for bash, zsh, fish and PowerShell. |

## Global flags

| Flag | Default | Config key | Description |
|---|---|---|---|
| `-p`, `--project-name` | — | *(none)* | The compose project to act on, as `docker compose ls` names it. Required by every command but `init`. Read from `ARCAVE_PROJECT_NAME` when the flag is absent. |
| `--session` | `baseline` | *(none)* | The session to work in, by id or by name. Read from `ARCAVE_SESSION` when the flag is absent. See [`arcave session`](session.md). |
| `--config` | `.arcave/config.yaml` | *(none)* | Path to the config file. |
| `--log-level` | `info` | `log.level` | `debug`, `info` or `warn`. |
| `--log-format` | `text` | `log.format` | `text` or `json`. |
| `--wait-timeout` | `2m0s` | `compose.wait_timeout` | How long to wait for services to become healthy after they are started. |
| `--stop-timeout` | `10s` | `compose.stop_timeout` | How long a container gets to exit on its own before it is killed. |
| `--progress` | `log` | `compose.progress` | Where compose's own progress goes: `log`, `tty` or `quiet`. |
| `-v`, `--version` | — | *(none)* | Print the version, commit and build date. |
| `-h`, `--help` | — | *(none)* | Show help for the command. |

A flag carrying a **config key** can also be set in `.arcave/config.yaml` or through an `ARCAVE_<KEY>` variable; a changed flag beats the variable, which beats the file, which beats the default. See [Configuration](../configuration.md#precedence).

`--project-name` and `--config` carry no config key on purpose: they steer one run rather than describing the project. In particular the compose project is not a config key — one project directory may hold several compose projects.

Durations carry their unit — `90s`, `5m`, `1h30m`. A bare number is an error, not a guess.

```text title="arcave --version"
arcave version 0.1.0 (commit 428850e…, built 2026-08-25T09:14:03Z)
```

## Naming the project

```shell title="Per invocation"
arcave -p myapp savepoint create before-migration
```

```shell title="Once per shell"
export ARCAVE_PROJECT_NAME=myapp
arcave savepoint create before-migration
```

Without either:

```text
no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME
```

`ARCAVE_PROJECT_NAME` names two different things at once. [Environment variables](../environment_variables.md#arcave_project_name) owns that, and is the page to read before exporting it.

## What a command needs

Every command but `init` needs three things:

* **An initialised project.** Without `.arcave/config.yaml`: `config is not found: run 'arcave init' if not yet initialized`.
* **A named compose project**, as above.
* **A reachable Docker daemon.** The daemon is contacted while the command wires itself up, before any workflow starts, so an unreachable one fails `savepoint list` exactly as it fails `rollback`.

`savepoint create` and `rollback` additionally need the project to **exist in the daemon**: arcave reads the running project rather than your compose files. `docker compose stop` is fine — stopped containers still describe the project. `docker compose down` is not:

```text
no containers found for compose project "myapp": bring the stack up once, or name another project with -p
```

Every mutating command claims a non-blocking lock on `.arcave/lock`. A second run against the same project fails at once rather than queuing:

```text
another arcave command is already running in this project
```

`savepoint remove --dry-run` is the exception: it only reads, and takes no lock.

## Next step

[Output and exit codes](../output.md) — the JSON on stdout, the logs on stderr, and the two exit codes.
