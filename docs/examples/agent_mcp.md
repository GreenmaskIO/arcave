---
title: Drive arcave from an AI agent
description: Register the arcave MCP server with an agent host and let an AI agent checkpoint a Docker Compose environment before it acts and roll back when its validation fails.
keywords: [arcave example, MCP server, AI agent, agentic pipeline, Claude Code, savepoint, rollback, Open-Source, Greenmask]
---

# Drive arcave from an AI agent

Give an agent an undo button, so a failed iteration costs a rollback instead of a rebuild.

Runs against the [demo project](index.md#the-project).

## 1. Initialise the project

```shell
docker compose up -d --wait
arcave init
```

The MCP server refuses to start without `.arcave/config.yaml`.

## 2. Register the server

```json title=".mcp.json"
{
  "mcpServers": {
    "arcave": {
      "command": "arcave",
      "args": ["mcp"]
    }
  }
}
```

The host launches `arcave mcp` as a child process and talks JSON-RPC 2.0 over stdio. No port, no HTTP server, nothing to configure on the network.

Start the host **in the project directory**, so `.arcave/config.yaml` resolves the same way it does for the CLI.

## 3. What the agent sees

Ten tools:

| Tool | Purpose |
|---|---|
| `savepoint_create` | Capture the environment |
| `savepoint_list` | Find previous captures, newest first; `sort: "asc"` turns it around |
| `savepoint_remove` | Prune |
| `rollback` | Restore |
| `session_list` | List the [sessions](../sessions.md) |
| `session_create` | Create a session, empty or a copy of another (`from`) |
| `session_remove` | Remove a session and its savepoints |
| `session_current` | Show the session calls work in |
| `session_set` | Make a session the current one |
| `session_unset` | Go back to `baseline` |

The four savepoint tools take `project` explicitly — one server serves many compose projects, and the agent names the one it means per call. They also take an optional `session`, by name or id, for that call only; without it they work in the current session. The session tools take no `project`: a session holds every compose project of the directory.

```json title="A savepoint_create call"
{
  "project": "arcave-demo",
  "name": "attempt-1",
  "description": "before rewriting the orders schema"
}
```

```json title="A rollback call"
{
  "project": "arcave-demo",
  "ref": "attempt-1"
}
```

`ref` takes an id, a name, or the literal `latest`.

To keep the agent's checkpoints apart from yours, give it a session of its own:

```json title="A session_create call"
{
  "name": "agent",
  "from": "baseline"
}
```

```json title="A savepoint_create call in that session"
{
  "project": "arcave-demo",
  "session": "agent",
  "name": "attempt-1"
}
```

Or pin the whole server to it with `"args": ["mcp", "--session", "agent"]`; a pinned server refuses `session_set` and `session_unset`.

## 4. The loop the agent runs

```text
1.  savepoint_create  { project, name: "attempt-N" }
2.  Apply the change — migration, schema rewrite, data transformation
3.  Run the tests
4.  Passed?  continue, and snapshot again before the next change
    Failed?  rollback { project, ref: "attempt-N" } and try another approach
```

Because the environment resets between attempts, attempt 4 is genuinely attempt 4 — not attempt 1 plus the debris of three failures.

## 5. Give slow compose projects more time

```json title="savepoint_create with an explicit bound"
{
  "project": "arcave-demo",
  "name": "attempt-3",
  "wait_timeout": "5m"
}
```

Both tools that restart the project accept `wait_timeout`. Without it they use the project's `compose.wait_timeout`, two minutes by default — and an agent that keeps hitting that on a slow-starting project will read the timeout as a failure and roll back for the wrong reason.

## 6. Watch what it is doing

`arcave mcp` writes nothing to stdout — that stream is the protocol channel. Logs still go to stderr:

```shell
arcave --log-format json mcp 2> arcave-mcp.log
```

```json title="arcave-mcp.log (trimmed)"
{"level":"info","message":"stopping services"}
{"level":"info","volume":"volume-named-arcave-demo_pgdata","backend":"fullcopy","op":"snapshot","message":"volume operation started"}
{"level":"info","volume":"volume-named-arcave-demo_pgdata","backend":"fullcopy","op":"snapshot","message":"volume operation finished"}
{"level":"info","message":"starting services"}
{"level":"info","kind":"Container","name":"arcave-demo-postgres-1","status":"Healthy","message":"compose"}
```

Each record also carries its time, and the volume records `snapshot_id` and `duration`. If the host launches the server itself, it usually surfaces stderr in its own log view.

## 7. Prune at the end of a run

```json title="savepoint_remove"
{
  "project": "arcave-demo",
  "retain_recent": 3
}
```

```json title="Preview first"
{
  "project": "arcave-demo",
  "retain_recent": 3,
  "dry_run": true
}
```

## 8. Make the loop cheap

An agent that snapshots twenty times per run should not pay for twenty full copies:

```yaml title=".arcave/config.yaml"
snapshot:
    backend: cdc
```

Each volume gets a restic repository that outlives any one savepoint, and a capture writes only the chunks that changed. See [Configuration](../reference/configuration.md#snapshot).

## What to tell the agent it cannot do

* **It cannot run two workflows at once.** Every mutating call claims the project lock and fails immediately rather than queuing.
* **It cannot roll back to a point it did not snapshot.** There is no point-in-time recovery — only savepoints that were actually taken.
* **It is not sandboxed.** arcave gives an undo, not isolation. The agent is operating on the real Docker daemon, the real volumes and the real ports.
* **Each call stops the project.** Every `savepoint_create` and `rollback` restarts the services and waits for health checks. This is not a free operation to call in a tight loop.

## Related docs

* [Agent iteration loops](../use_cases/agent_loops.md) — the pattern behind this.
* [arcave mcp](../reference/cli/mcp.md) — the full tool reference.
* [Limitations](../reference/limitations.md) — the constraints in full.
