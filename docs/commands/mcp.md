---
title: mcp
description: Start the arcave MCP server over stdio, exposing savepoint_create, savepoint_list, savepoint_remove and rollback as native tools for an AI agent.
keywords: [arcave mcp, MCP server, AI agent, agentic pipeline, model context protocol, stdio, docker compose, Open-Source, Greenmask]
---

# arcave mcp

Starts the arcave MCP server, exposing the savepoint workflows as native tools for an AI agent.

## Usage

```text
Usage: arcave mcp
```

Takes no arguments and no flags of its own.

:::note
`arcave mcp` does not take `-p/--project-name`. One server serves many stacks, and each tool call names the project it means. Every other stack command takes it because it acts on exactly one.
:::

## Transport

JSON-RPC 2.0 over **stdio**. The agent host launches `arcave mcp` as a child process and talks to it over the standard streams. No HTTP server, no port, no network configuration.

```text
Agent host process
      │
      │  stdin / stdout  (JSON-RPC 2.0)
      ▼
 arcave mcp process
      │
      │  direct Go calls — no subprocess, no output parsing
      ▼
 the same workflows the CLI runs
```

The tool handlers do not shell out to the `arcave` binary. Both adapters call the same service, so there is one implementation of savepoint and rollback, not two.

:::warning
`arcave mcp` writes **nothing** to stdout — that stream is the JSON-RPC channel. Logs still go to stderr, in whatever `--log-format` says, so the host can surface them without corrupting the protocol.
:::

## Tools

### savepoint_create

Creates a savepoint: a consistent snapshot of every stateful volume in the project.

| Field | Required | Description |
|---|---|---|
| `project` | yes | The Docker Compose project to operate on, as `docker compose ls` names it. |
| `name` | no | A short name for the savepoint, used to refer to it later. Ids are assigned by arcave. |
| `description` | no | What this savepoint captures, e.g. the change it is being taken before. |
| `wait_timeout` | no | How long to wait for services to become healthy on restart, e.g. `2m` or `90s`. Omit for the project's default. |

### savepoint_list

Lists the project's savepoints, newest first.

| Field | Required | Description |
|---|---|---|
| `project` | yes | The compose project to operate on. |
| `include_safety_snapshots` | no | Also list the safety snapshots a rollback commits for its own undo. Hidden by default. |

### savepoint_remove

Deletes savepoints and the snapshot data they hold.

| Field | Required | Description |
|---|---|---|
| `project` | yes | The compose project to operate on. |
| `ids` | no | Savepoint ids to delete. Cannot be combined with the retention fields. |
| `retain_recent` | no | Keep this many newest savepoints and delete the rest. |
| `retain_for` | no | Keep savepoints younger than this duration, e.g. `24h` or `7d`. |
| `before_date` | no | Delete savepoints created before this date (`YYYY-MM-DD`). |
| `dry_run` | no | Report what would be deleted without deleting anything. |

Either `ids` **or** the retention fields — not both, and not neither. See [savepoint remove](savepoint_remove.md#the-two-selection-modes).

### rollback

Restores the environment to a savepoint.

| Field | Required | Description |
|---|---|---|
| `project` | yes | The compose project to operate on. |
| `ref` | yes | The savepoint to restore: its id, its name, or the literal `latest`. |
| `wait_timeout` | no | How long to wait for services to become healthy on restart. Omit for the project's default. |

## Registration

```json title=".mcp.json / claude_desktop_config.json"
{
  "mcpServers": {
    "arcave": {
      "command": "arcave",
      "args": ["mcp"]
    }
  }
}
```

The host should start the server **in the project directory**, so `.arcave/config.yaml` resolves relative to the working directory exactly as it does for the CLI.

## Behavior

`arcave mcp` requires an initialised project — it fails at startup with `config is not found: run 'arcave init' if not yet initialized` otherwise.

It builds no dependencies at startup. Each tool call wires the storage and Docker clients for the project it names, and releases them when the call returns. That is what lets one server address several stacks.

Every tool call runs the same workflow the CLI runs, with the same guarantees: the stack is stopped and restarted, the project lock is claimed, an interrupted rollback blocks mutating calls, and a failed rollback replays its safety snapshot automatically.

## Errors

| Condition | What happens |
|---|---|
| No `.arcave/config.yaml` | The server refuses to start. |
| A tool call names an unknown project | The call fails; the server stays up. |
| Another arcave run holds the project lock | The call fails immediately rather than queuing. |
| An interrupted rollback is marked | Mutating calls are refused until it is resolved. |

## Examples

### Register with Claude Code and use it

```shell
arcave init
```

```json title=".mcp.json"
{
  "mcpServers": {
    "arcave": { "command": "arcave", "args": ["mcp"] }
  }
}
```

The agent can then checkpoint before acting and roll back when its own validation fails, without shelling out.

### Run it manually to inspect the tool list

```shell
arcave mcp
```

It will sit waiting for JSON-RPC on stdin. This is only useful for debugging a host integration — normally the host launches it.

### Watch what an agent is doing

```shell
arcave --log-format json mcp 2> arcave-mcp.log
```

Logs go to stderr, so redirecting it captures the agent's whole sequence of operations without touching the protocol channel.

## Related docs

* [Agent iteration loops](../use_cases/agent_loops.md) — the workflow this exists for.
* [savepoint create](savepoint_create.md) · [rollback](rollback.md) — the same operations from the CLI.
* [Limitations](../limitations.md) — what to tell an agent it cannot do.
