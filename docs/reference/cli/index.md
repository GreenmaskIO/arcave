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
| [`savepoint remove`](savepoint_remove.md) | Deletes savepoints by id or name, or by retention rules. |
| [`rollback`](rollback.md) | Returns the environment to a savepoint, selected by id, by name, or by `latest`. |
| [`session`](session.md) | Creates, lists, removes and switches sessions — the independent lines of savepoints inside a project: `create`, `list`, `remove`, `set`, `unset`, `current`. |
| [`tools generate-compose`](tools_generate_compose.md) | Writes a docker compose file for a running compose project, from what the daemon reports. |
| [`gc`](gc.md) | Finds and deletes the garbage arcave left in a session's store and in the Docker daemon. |
| [`mcp`](mcp.md) | Serves the workflows above as MCP tools for an agent. |
| [`status`](status.md) | Planned. Registered, and returns `not implemented`. |
| `completion` | Cobra's shell-completion script generator, for bash, zsh, fish and PowerShell. |

Short aliases: `savepoint` also answers to `save` and `sp`, every `list` to `ls`, and every `remove` to `rm` — `arcave sp ls` is `arcave savepoint list`.

## Global flags

| Flag | Default | Config key | Description |
|---|---|---|---|
| `-p`, `--project-name` | — | *(none)* | The compose project to act on, as `docker compose ls` names it. Required by every command but `init`, `mcp`, `status` and the `session` commands; an MCP tool call names its compose project itself. Read from `ARCAVE_PROJECT_NAME` when the flag is absent. |
| `--session` | the current session, else `baseline` | *(none)* | The session to work in, by id or by name. Read from `ARCAVE_SESSION` when the flag is absent; with neither, the current session `arcave session set` chose, else `baseline`. See [`arcave session`](session.md#naming-the-session-a-command-works-in). |
| `--config` | `.arcave/config.yaml` | *(none)* | Path to the config file. |
| `--log-level` | `warn` above the live tree, else `info` | `log.level` | `debug`, `info` or `warn`. |
| `--log-format` | `text` | `log.format` | `text` or `json`. |
| `--wait-timeout` | `2m0s` | `compose.wait_timeout` | How long to wait for services to become healthy after they are started. |
| `--stop-timeout` | `10s` | `compose.stop_timeout` | The least time a container gets to exit on its own before it is killed; a service's `stop_grace_period` raises it. |
| `--progress` | `auto` | *(none)* | How a run shows its progress: `auto` (`tty` on a terminal with text logs, else `log`), `tty` (a live tree on stderr), `log` (log records) or `json` (JSON lines on stdout, the result last). Read from `ARCAVE_PROGRESS` when the flag is absent. See [Output](../output.md#progress). |
| `--format` | `auto` | *(none)* | How the result is printed on stdout: `auto` (text while the live tree is drawn and stdout is a terminal, else JSON), `json` or `text`. `text` is refused beside `--progress json`. Read from `ARCAVE_FORMAT` when the flag is absent. See [Output](../output.md#result-format). |
| `--compose-progress` | `log` | `compose.progress` | Where compose's own progress goes: `log`, `tty` or `quiet`. |
| `-v`, `--version` | — | *(none)* | Print the version, commit and build date. |
| `-h`, `--help` | — | *(none)* | Show help for the command. |

A flag carrying a **config key** can also be set in `.arcave/config.yaml` or through an `ARCAVE_<KEY>` variable; a changed flag beats the variable, which beats the file, which beats the default. See [Configuration](../configuration.md#precedence).

`--project-name`, `--session`, `--progress`, `--format` and `--config` carry no config key on purpose: they steer one run rather than describing the project. In particular the compose project is not a config key — one project directory may hold several compose projects.

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

`savepoint create`, `savepoint list`, `savepoint remove` and `rollback` need three things:

* **An initialised project.** Without `.arcave/config.yaml`: `config is not found: run 'arcave init' if not yet initialized`.
* **A named compose project**, as above.
* **A reachable Docker daemon.** The daemon is contacted while the command wires itself up, before any workflow starts, so an unreachable one fails `savepoint list` exactly as it fails `rollback`.

The `session` commands need only the initialised project: they name no compose project and reach no daemon, `session create --from` included — a fork copies a session's savepoints and deploys nothing. `mcp` needs the initialised project; each tool call names its compose project. `tools generate-compose` needs a named compose project and a reachable daemon, and no initialised project: it opens no store. `gc` needs the initialised project and works on a session: it refuses `-p`. It reaches the daemon only for the `restored-layers` kind.

`savepoint create` additionally needs the project to **exist in the daemon**: arcave reads the running project rather than your compose files. `docker compose stop` is fine — stopped containers still describe the project. `docker compose down` is not:

```text
compose project "myapp": no containers found: bring the compose project up once, or name another project with -p
```

`rollback` does not: a compose project with no containers — never started, or taken down — is built from the savepoint's own spec. See [Rolling back a project that is not running](rollback.md#rolling-back-a-project-that-is-not-running).

Every command that changes something — `savepoint create`, `rollback`, `savepoint remove`, `gc` (`--dry-run` included), `session create`, `session remove`, `session set`, `session unset` — claims a non-blocking lock on `.arcave/lock`. A second one against the same project fails at once rather than queuing:

```text
another arcave command is already running in this project
```

`savepoint list`, `session list`, `session current` and `savepoint remove --dry-run` only read, take no lock, and are never refused.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | Any failure; the cause is on stderr. |
| `130` | Interrupted by Ctrl-C or `SIGTERM`. The first signal lets the command unwind through its own cleanup; a second one exits at once. |

See [Output and exit codes](../output.md#exit-codes).

## Next step

[Output and exit codes](../output.md) — the JSON on stdout, the logs on stderr, and the exit codes.
