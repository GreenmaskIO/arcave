---
title: MCP tools
description: The twelve tools arcave's MCP server exposes — savepoint_create, savepoint_list, savepoint_remove, rollback, the six session tools, gc and compose_generate — their arguments, their output shapes, and where they differ from the CLI.
keywords: [arcave MCP tools, savepoint_create, rollback, session_create, AI agent, model context protocol]
---

# MCP tools

[`arcave mcp`](cli/mcp.md) exposes twelve tools: four for savepoints, six for sessions, [`gc`](#gc) and [`compose_generate`](#compose_generate). Each savepoint tool takes the compose project it acts on as an argument, so one server addresses every project on the machine.

Every tool runs the workflow the CLI runs, under the same rules: the project is stopped and started again, the project lock is claimed, and a rollback of a running project that fails replays its safety snapshot. What an agent takes, you list; what you take, an agent rolls back to.

## Which session a call works in

Each savepoint tool takes an optional `session`, by name or id, for that call only. Without it, the call works in the session the server was pinned to with `--session` or `ARCAVE_SESSION`, else in the current session (`.arcave/current_session`, shared with your own shell), else in `baseline`. See [`arcave mcp`](cli/mcp.md#sessions).

## `savepoint_create`

Captures a savepoint. The services are stopped for the capture and started again afterwards.

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project, as `docker compose ls` names it. |
| `session` | no | The session to work in for this call only. |
| `name` | no | A short name to refer to the savepoint by. Ids are assigned by arcave. |
| `description` | no | What this savepoint is being taken before. |
| `wait_timeout` | no | How long to wait for services to become healthy on restart, e.g. `2m` or `90s`. Omit for the project's own bound. |

Returns the savepoint record — the shape [Output](output.md#the-savepoint-record) describes, `stats`, `writable_layers`, `images`, `spec` and `not_covered` included.

```json title="structured content"
{
  "id": "019e843e-1650-7000-8000-000000000000",
  "name": "before-migration",
  "description": "before the v2 schema",
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [ … ],
  "writable_layers": [ … ],
  "images": [ … ],
  "spec": { "format": "compose-yaml" },
  "not_covered": { "services_without_volumes": ["worker"] }
}
```

`not_covered` is what the savepoint does **not** hold — skipped mounts with a reason code, services without a captured volume, containers killed at the stop deadline, replica gaps, unresolvable images, and layers or images a [capture label](capture_labels.md) left out. The tool's description tells an agent to report it before relying on the savepoint.

A failure of the form `savepoint 019e843e-1650-7000-8000-000000000000 committed; services failed to start: …` means the savepoint **exists and is safe to roll back to**; only the restart failed. The tool's own description tells an agent this, so it reports the id rather than taking another savepoint.

## `savepoint_list`

Reads the store. Stops nothing, and takes no lock.

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project. |
| `session` | no | The session to work in for this call only. |
| `include_safety_snapshots` | no | Also list the safety snapshots a rollback commits of the live state before it overwrites it. Hidden by default. |
| `sort` | no | `desc` (the default) lists the newest first, `asc` the oldest. Any other value is refused. |

Returns an object, because MCP structured output must be one — the array the CLI prints is the `savepoints` field:

```json title="structured content"
{
  "savepoints": [
    { "id": "019e843e-1650-7000-8000-000000000000", "name": "before-migration", … }
  ]
}
```

An empty store answers `{"savepoints": []}`, never `null`.

## `savepoint_remove`

Deletes savepoints and the data they hold. Permanent.

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project. |
| `session` | no | The session to work in for this call only. |
| `refs` | no | Savepoints to delete, each by id or name. Cannot be combined with the retention fields. |
| `retain_recent` | no | Keep this many newest savepoints and delete the rest. |
| `retain_for` | no | Keep savepoints younger than this duration, e.g. `24h` or `7d`. |
| `before_date` | no | Delete savepoints created before this RFC 3339 timestamp, e.g. `2026-06-01T00:00:00Z`. |
| `dry_run` | no | Report what would be deleted, and delete nothing. |

Either `refs` or the retention fields — not both, and not neither. The refusals carry the same wording the CLI uses: `cannot combine savepoint ids or names with retain-recent/retain-for/before-date`, and `specify savepoint ids or names, or at least one of retain-recent, retain-for or before-date`.

```json title="structured content"
{
  "dry_run": true,
  "removed": [
    { "id": "019e843e-1650-7000-8000-000000000000", "name": "before-migration", … }
  ]
}
```

## `rollback`

Returns the environment to a savepoint. Destructive: every volume it covers is overwritten with the savepoint's data, bind mounts included, and a volume a service reaches through `subpath:` goes back whole. When the project is running, arcave takes a safety snapshot of the live state first and replays it by itself if the rollback fails. A project with no containers is built from the savepoint's spec instead, with no safety snapshot, and [`rollback.leftover_volumes`](configuration.md#rollback) from the config decides about the named volumes it still owns — see [Rolling back a project that is not running](cli/rollback.md#rolling-back-a-project-that-is-not-running).

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project. |
| `session` | no | The session to work in for this call only. |
| `ref` | yes | The savepoint to return to: its id, its name, or the literal `latest`. |
| `wait_timeout` | no | How long to wait for services to become healthy on restart. Omit for the project's own bound. |

Returns what the CLI prints — see [Output](output.md#rollback):

```json title="structured content"
{
  "rolled_back_to": { "id": "019e843e-1650-7000-8000-000000000000", … },
  "restored": [
    { "object_id": "volume-named-myapp_pgdata", "type": "named", … }
  ],
  "writable_layers": [
    { "service": "app", "replica": 1, "container_name": "myapp-app-1", "image_id": "sha256:aaaa…",
      "tag": "arcave/myapp-app-1-myapp-latest:sp-019e843e-1650-7000-8000-000000000000" }
  ]
}
```

`writable_layers` is omitted when the savepoint held no layer.

## `session_list`

Lists the arcave project's sessions. Read-only, and takes no lock.

| Argument | Required | Description |
|---|---|---|
| `all` | no | Also list the session directories without a record: forks still cloning, failed, or left incomplete. |
| `sort` | no | `desc` (the default) lists the newest first, `asc` the oldest. |

```json title="structured content"
{
  "sessions": [
    { "id": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "name": "agent-work", "created_at": "2026-10-10T12:00:00Z",
      "from": { "session": "00000000-0000-0000-0000-000000000001" },
      "status": "ready", "current": true, "savepoint_count": 1, "compose_projects": ["myapp"], "size_bytes": 734003396 },
    { "id": "00000000-0000-0000-0000-000000000001", "name": "baseline",
      "status": "ready", "current": false, "savepoint_count": 2, "compose_projects": ["myapp"], "size_bytes": 734003200 }
  ]
}
```

`current` marks the session a call given no `session` works in — the pinned session on a pinned server. The entries are those of [`session list`](cli/session.md#session-list).

## `session_create`

Creates a session: empty, or with `from` a copy of another session — every compose project and savepoint it holds. A fork touches no container: to run the forked state, call `session_set` (or pass `session`) and then `rollback` to one of its savepoints. It costs the source's `size_bytes`, and holds the project lock for its whole copy.

| Argument | Required | Description |
|---|---|---|
| `name` | no | A name for the session, unique within the arcave project. |
| `from` | no | The session to copy, by name or id. Omit for an empty session. |

Returns the session record:

```json title="structured content"
{ "id": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "name": "agent-work", "created_at": "2026-10-10T12:00:00Z",
  "from": { "session": "00000000-0000-0000-0000-000000000001" } }
```

## `session_remove`

Deletes a session and every savepoint it holds, permanently. A compose project running from it is left as it is. `baseline` cannot be deleted; with `force` it is emptied instead.

| Argument | Required | Description |
|---|---|---|
| `session` | yes | The session to remove, by name or id; a directory without a record by id only. |
| `force` | no | Also empty the default session (baseline), remove the current session — clearing the pointer, so calls work in `baseline` — the session the server is pinned to, or a directory whose fork still looks alive. |

```json title="structured content"
{ "session": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "removed": true, "freed_bytes": 734003396 }
```

## `session_current`

Takes no arguments. Returns the record of the session a call given no `session` works in: the pinned session, else the current session, else `baseline`. Read-only. It fails when the current session no longer exists; `session_unset` or `session_set` repairs that.

## `session_set`

Makes a session the current one: every later call given no `session` works in it — and so do your own arcave commands in the project directory, which share the pointer. Returns the session's record.

| Argument | Required | Description |
|---|---|---|
| `session` | yes | The session to make current, by name or id. |

## `session_unset`

Takes no arguments. Clears the current session, so calls given no `session` work in `baseline` again, and returns `baseline`'s record. It is also the way out when the current session no longer exists.

`session_set` and `session_unset` are refused on a pinned server:

```text
this server is pinned to session "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31" by --session or ARCAVE_SESSION, which outranks the current session: pass session on each call, or restart the server without the pin
```

## `compose_generate`

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project to generate the file of. |
| `absolute_paths` | no | Keep every bind source absolute instead of relative to the compose project's working directory. |

Returns `{"compose_project": …, "services": [ … ], "spec": "<the compose file, as YAML>"}`. Read-only, and works in no session: see [`arcave tools generate-compose`](cli/tools_generate_compose.md). The file carries every environment value the containers run with, secrets included.

## `gc`

| Argument | Required | Description |
|---|---|---|
| `dry_run` | no | `true`, the default, only reports; `false` deletes, as `arcave gc` without `--dry-run` does. |
| `session` | no | The session to look in, by name or by id. The default is the pinned session, or the current one. |
| `restored_layer_grace` | no | How long ago an unused layer tag must have been tagged before it counts as garbage, written as a duration such as `1h`. The default is `gc.restored_layer_grace`. |
| `kinds` | no | The kinds of data to look at: `savepoint-data`, `deduplicated-volumes`, `image-layers`, `upstream-images`, `restored-layers`. The default is every kind. |

Returns the report [`arcave gc`](cli/gc.md) prints. Unless `dry_run` is `false` the tool deletes nothing; it is marked destructive either way. A partial gc is still returned as a report: what could not be judged is marked `aborted` or `unjudged`, or carries an `error`, and what failed to delete is under `deletion.failed`. A gc that lost its session lock midway through deleting is returned as a tool error that still carries the report, with what was deleted before the loss.

## Where the tools differ from the CLI

* **Lists come wrapped in an object.** `savepoint_list` answers `{"savepoints": […]}` and `session_list` `{"sessions": […]}`, where the CLI prints the bare array.
* **A lock error says what to wait for.** A call refused because another run holds the project lock reads `another arcave command is already running in this project: a session fork holds the lock for its whole copy, as does a savepoint or rollback for its run; retry when it finishes`.
* **Errors keep the CLI's flag names.** An unparseable `before_date` is reported as `invalid --before-date "2026-06-01": …`, and `retain_for` as `invalid --retain-for …`.

`wait_timeout` takes a unit-bearing duration in both tools that accept it. A bare number is refused:

```text
wait_timeout "120": want a unit-bearing value like 2m, 90s or 1h30m
```

## Next step

[Requirements](requirements.md) — what has to be true of the machine before any of this runs.
