---
title: MCP tools
description: The four tools arcave's MCP server exposes — savepoint_create, savepoint_list, savepoint_remove and rollback — their arguments, their output shapes, and where they differ from the CLI.
keywords: [arcave MCP tools, savepoint_create, rollback, AI agent, model context protocol]
---

# MCP tools

[`arcave mcp`](cli/mcp.md) exposes four tools. Each takes the compose project it acts on as an argument, so one server addresses every project on the machine.

Every tool runs the workflow the CLI runs, under the same rules: the project is stopped and started again, the project lock is claimed, and a rollback that fails replays its safety snapshot. What an agent takes, you list; what you take, an agent rolls back to.

## `savepoint_create`

Captures a savepoint. The services are stopped for the capture and started again afterwards.

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project, as `docker compose ls` names it. |
| `name` | no | A short name to refer to the savepoint by. Ids are assigned by arcave. |
| `description` | no | What this savepoint is being taken before. |
| `wait_timeout` | no | How long to wait for services to become healthy on restart, e.g. `2m` or `90s`. Omit for the project's own bound. |

Returns the savepoint record — the shape [Output](output.md#the-savepoint-record) describes, `stats`, `images` and `spec` included.

```json title="structured content"
{
  "id": "1780335122",
  "name": "before-migration",
  "description": "before the v2 schema",
  "created_at": "2026-06-01T19:41:12.123456789Z",
  "stats": [ … ],
  "images": [ … ],
  "spec": { "format": "compose-yaml" }
}
```

A failure of the form `savepoint 1780335122 committed; services failed to start: …` means the savepoint **exists and is safe to roll back to**; only the restart failed. The tool's own description tells an agent this, so it reports the id rather than taking another savepoint.

## `savepoint_list`

Reads the store. Stops nothing, and takes no lock.

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project. |
| `include_safety_snapshots` | no | Also list the safety snapshots a rollback commits of the live state before it overwrites it. Hidden by default. |

Returns an object, because MCP structured output must be one — the array the CLI prints is the `savepoints` field:

```json title="structured content"
{
  "savepoints": [
    { "id": "1780335122", "name": "before-migration", … }
  ]
}
```

An empty store answers `{"savepoints": []}`, never `null`.

## `savepoint_remove`

Deletes savepoints and the data they hold. Permanent.

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project. |
| `ids` | no | Savepoint ids to delete. Cannot be combined with the retention fields. |
| `retain_recent` | no | Keep this many newest savepoints and delete the rest. |
| `retain_for` | no | Keep savepoints younger than this duration, e.g. `24h` or `7d`. |
| `before_date` | no | Delete savepoints created before this timestamp. |
| `dry_run` | no | Report what would be deleted, and delete nothing. |

Either `ids` or the retention fields — not both, and not neither. The refusals carry the same wording the CLI uses: `cannot combine savepoint ids with retain-recent/retain-for/before-date`, and `specify savepoint ids, or at least one of retain-recent, retain-for or before-date`.

```json title="structured content"
{
  "dry_run": true,
  "removed": [
    { "id": "1780335122", "name": "before-migration", … }
  ]
}
```

## `rollback`

Returns the environment to a savepoint. Destructive: every volume it covers is wiped and written back, bind mounts included, and a volume a service reaches through `subpath:` goes back whole. arcave takes a safety snapshot of the live state first and replays it by itself if the rollback fails.

| Argument | Required | Description |
|---|---|---|
| `project` | yes | The compose project. |
| `ref` | yes | The savepoint to return to: its id, its name, or the literal `latest`. |
| `wait_timeout` | no | How long to wait for services to become healthy on restart. Omit for the project's own bound. |

```json title="structured content"
{
  "rolled_back_to": { "id": "1780335122", … },
  "restored": [
    { "object_id": "volume-named-myapp_pgdata", "type": "named", … }
  ]
}
```

## Where the tools differ from the CLI

Three differences an agent's caller should know about.

* **`before_date` is described as `YYYY-MM-DD` and parsed as RFC3339Nano.** `2026-06-01` is refused with `invalid --before-date "2026-06-01": …`; pass `2026-06-01T00:00:00Z`.
* **`rollback` returns no `images` field.** The CLI prints one entry per writable layer put back, with the local reference it now answers to; the tool's result stops at `rolled_back_to` and `restored`. The layers are restored either way.
* **`savepoint_list` is described as newest first and answers oldest first**, exactly as the CLI does. Sort on `created_at` rather than taking the first element.

`wait_timeout` takes a unit-bearing duration in both tools that accept it. A bare number is refused:

```text
wait_timeout "120": want a unit-bearing value like 2m, 90s or 1h30m
```

## Next step

[Requirements](requirements.md) — what has to be true of the machine before any of this runs.
