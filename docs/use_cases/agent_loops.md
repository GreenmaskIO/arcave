---
title: Agent iteration loops
description: Give an AI coding agent a checkpoint per attempt — snapshot before it acts, roll back when validation fails — using the arcave CLI or the built-in MCP server.
keywords: [AI agent, agentic pipeline, development cycle, agent sandbox, savepoint, rollback, MCP server, docker compose, Open-Source, Greenmask]
---

# Agent iteration loops

An agent iterates on code the way you do: change something, run the tests, look at the result, try again. The difference is that it does it dozens of times, unattended, and it does not remember what the database looked like three attempts ago.

Code has version control for exactly this. State does not. An agent that ran a migration, seeded a table, or dropped a column has changed something no `git checkout` will undo — so by attempt four it is no longer iterating on the problem, it is iterating on a corrupted environment.

Savepoints close that gap. Snapshot before the attempt, roll back after a failed one, and every iteration starts from the same known state.

Make that known state realistic, and never production itself: give the agent a masked copy of production data — see [Prod-like data](prod_like_data.md).

## The loop

```text title="One iteration"
1.  Create a savepoint
2.  Apply the change — code, schema, migration, data
3.  Run tests and validation
4.  Passed?  keep going, and snapshot again before the next change
    Failed?  roll back and try a different approach
```

```shell title="The same loop, in shell"
SP=$(arcave savepoint create "attempt-${N}" | jq -r .id)

if ! { ./run-migration.sh && ./run-tests.sh; }; then
  arcave rollback "$SP"
fi
```

Because every command prints JSON on stdout and logs on stderr, `jq -r .id` is enough — there is no output to scrape.

## Why this is a checkpoint, not a backup

An agent run has a shape: it goes on for a while, produces many intermediate states, and almost all of them are throwaway. Savepoints match that shape.

* **Cheap to create**, with the [`cdc` backend](../reference/configuration.md#snapshot) — only the chunks that changed since the last capture are written. A loop that snapshots twenty times does not cost twenty full copies.
* **Cheap to discard** — `arcave savepoint remove --retain-recent 5` at the end of a run, or `--retain-for 24h` on a schedule.
* **Addressable by name** — `attempt-3`, `before-schema-rewrite`, `known-good`. The agent chooses names that mean something to its own reasoning.

## Giving the agent the tools directly

Rather than teaching an agent to shell out and parse output, run the MCP server:

```shell
arcave mcp
```

It speaks JSON-RPC 2.0 over stdio — the agent host launches it as a child process, no port and no network configuration. Ten tools appear in the agent's tool list. Four work on savepoints:

| Tool | What it does |
|---|---|
| `savepoint_create` | Capture the environment. Takes `project`, and optionally `session`, `name`, `description`, `wait_timeout`. |
| `savepoint_list` | List savepoints, newest first. Takes `project`, optionally `session`, `include_safety_snapshots`, `sort` (`desc` or `asc`). |
| `savepoint_remove` | Delete by `ids`, or by `retain_recent` / `retain_for` / `before_date`. Takes `project`, optionally `session`. Supports `dry_run`. |
| `rollback` | Restore to `ref` — an id, a name, or `latest`. Takes `project`, optionally `session`, `wait_timeout`. |

Every savepoint tool takes `project` explicitly: one server serves many compose projects, and the agent names the one it means per call. `session` picks the [session](../sessions.md) for that call only; without it the tool works in the current one.

The other six manage sessions: `session_list`, `session_create`, `session_remove`, `session_current`, `session_set` and `session_unset`. An agent can fork its own session from `baseline` with `session_create` and keep its checkpoints there, apart from yours. `arcave mcp --session <name>` pins the server to one session instead; a pinned server refuses `session_set` and `session_unset`.

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

The host should start it in the project directory, so `.arcave/config.yaml` resolves the same way it does for the CLI.

:::note
`arcave mcp` writes nothing to stdout — that is its JSON-RPC channel. Logs still go to stderr, so the host can surface them without corrupting the protocol.
:::

## Two guarantees worth telling the agent about

**A failed rollback is not a new state.** Before overwriting anything, arcave snapshots the live environment and replays it automatically if the restore fails or the services come up unhealthy. The agent's error path leads back to where it started, not somewhere unexplored.

**A crashed run is never half-committed.** The savepoint record is written last and deleted first, so a killed process leaves either a complete savepoint or nothing that `savepoint_list` will ever return. The agent cannot roll back to a half-captured state, because a half-captured state is not addressable.

## Bounding the loop

Two settings matter when an agent is driving:

```shell
arcave savepoint create attempt-7 --wait-timeout 5m
```

[`--wait-timeout`](../reference/configuration.md#compose) is how long arcave waits for health checks after restarting. An agent that keeps hitting the default two minutes on a slow-starting project will read the timeout as a failure and roll back for the wrong reason. Both MCP tools that restart the project accept `wait_timeout` per call for the same reason.

```shell
arcave --log-format json savepoint create attempt-7
```

`--log-format json` turns the whole stderr stream into records, so an agent parses progress instead of picking prose out of it.

## What this is not

arcave gives an agent an **undo**, not a **sandbox**. The agent is still operating on your real Docker daemon, your real volumes and your real ports. A savepoint protects the data; it does not isolate the blast radius. If the agent needs isolation rather than reversibility, it needs a separate environment, and arcave is what makes that environment cheap to reset — not what makes it separate.

The loop also serialises. Every workflow stops the project and holds the project lock, so one agent iterates at a time per project. Two agents on one compose project is not a supported shape — see [Limitations](../reference/limitations.md#one-writer-per-project).

## Related docs

* [Prod-like data](prod_like_data.md) — masked, realistic data for the agent to work on.
* [arcave mcp](../reference/cli/mcp.md) — the command reference.
* [Environment branching](environment_branching.md) — when the checkpoints outlive the run.
* [Limitations](../reference/limitations.md) — the downtime, and what a savepoint does not cover.
