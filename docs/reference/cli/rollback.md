---
title: arcave rollback
description: Return a Docker Compose environment to an arcave savepoint — its volumes, its containers' writable layers and the stack topology it recorded — behind an automatic safety snapshot.
keywords: [arcave rollback, savepoint, safety snapshot, compose spec, volume data]
---

# arcave rollback

Returns the environment to a savepoint.

```text title="Synopsis"
arcave rollback <savepoint-id|name|latest>
```

Exactly one argument is required.

arcave rebuilds the stack the savepoint recorded, puts the captured writable layers back, and writes every captured volume back into it, behind a safety snapshot it replays by itself if anything fails. The whole sequence, its phases and their remedies are [`concepts/rollback_and_safety.md`](../../concepts/rollback_and_safety.md).

**A rollback is destructive.** Every volume it covers is wiped before it is written back — bind mounts, which are your own host directories, included; a volume a service reaches through `subpath:` goes back whole. Anything written since the savepoint is gone.

## Flags

`rollback` has no flags of its own. The bound it waits on before deciding a start has failed is [`--wait-timeout`](index.md#global-flags), which is a config key and therefore lives on the root command.

```shell
arcave rollback latest --wait-timeout 5m
```

## Selecting the savepoint

| Token | Resolves as |
|---|---|
| `latest` | The savepoint with the newest `created_at`. A reserved word. |
| A run of decimal digits | An **id**. Exact, and it never falls back to a name. |
| Anything else | A **name**. |

```shell
arcave rollback latest
arcave rollback 1780335122
arcave rollback before-migration
```

An id is the only form that reaches a hidden [safety snapshot](savepoint_list.md#safety-snapshots-are-hidden). A name matching several savepoints is an error listing the candidates, never a guess.

## Output

```json title="stdout"
{
  "rolled_back_to": {
    "id": "1780335122",
    "name": "before-migration",
    "description": "before the v2 schema",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    "stats": [ … ],
    "images": [ … ],
    "spec": { "format": "compose-yaml" }
  },
  "restored": [
    {
      "object_id": "volume-named-myapp_pgdata",
      "compose_project": "myapp",
      "type": "named",
      "backend": "copy",
      "named": {
        "volume_name": "myapp_pgdata",
        "used_by": [
          { "service": "db", "target": "/var/lib/postgresql/data" }
        ]
      }
    }
  ],
  "images": [
    {
      "service": "app",
      "replica": 1,
      "image_id": "sha256:aaaa…",
      "image_ref": "arcave/myapp-app-1-myapp-latest:sp-1780335122"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `rolled_back_to` | The savepoint record that was applied, whole. |
| `restored` | One entry per volume written back, as the savepoint recorded it. `type` is `named`, `bind` or `anon`, with the matching block. |
| `images` | One entry per writable layer loaded back, with the local reference it now answers to. |

Each restored layer is tagged `arcave/<stack>-<service>-<replica>-<base image>:sp-<savepoint id>`, so `docker images` reads as one row per replica and one tag per savepoint. Those tags are not collected — see [Limitations](../limitations.md#nothing-collects-what-a-rollback-leaves-in-the-daemon).

## Errors

| Condition | What happens |
|---|---|
| No argument | cobra refuses the call: `rollback` takes exactly one. |
| The id is not there | `finding savepoint "1780335122": savepoint not found: 1780335122` |
| The name is not there | `finding savepoint "clean": savepoint not found: clean` |
| The name matches several | `finding savepoint "clean": 2 savepoints are named "clean"; roll back by id instead: 1780335122, 1780339981` |
| `latest` with an empty store | `finding savepoint "latest": no savepoints exist` |
| The savepoint carries no compose spec | `load the savepoint's compose spec: savepoint carries no compose spec: 1780335122`. The stack is rebuilt from that spec, and savepoints taken before arcave recorded one carry none. |
| A stored layer the daemon will not take | `restore the savepoint's writable layers: …` — raised **before** anything is stopped. |
| An interrupted rollback is marked | `require no inflight: rollback to savepoint 1780335122 was interrupted (restoring)` — except a rollback to that marker's own safety snapshot, which is the remedy and passes. |
| Another arcave run holds the lock | `another arcave command is already running in this project` |
| The safety snapshot fails | `pre-rollback safety snapshot failed, nothing changed: …`. The services are started again. |
| Anything fails after that | `rollback failed: …`, after the safety snapshot has been replayed. You are where you started. |
| The replay fails too | The same error, with the safety snapshot **kept**, its id in the log, and the marker phased `inconsistent`. It is now the last copy of the pre-rollback state — see [Troubleshooting](../troubleshooting.md#an-interrupted-rollback). |

Everything that can fail is done while the stack is still up: both backend sets are built, the recorded spec is parsed, and every stored layer is loaded into the daemon before the first container is stopped.

## Examples

```shell title="Back to the last savepoint you took"
arcave rollback latest
```

```shell title="Return to a named baseline"
arcave rollback clean
```

```shell title="Guard a migration in a script"
./migrate up || arcave rollback before-migration
```

```shell title="Give a slow stack more time before arcave calls it a failure"
arcave rollback latest --wait-timeout 5m
```

Worth raising whenever a health check is slower than two minutes: a healthy-but-slow start otherwise reads as a failed rollback and triggers a replay you did not want.

```shell title="Recover from an interrupted rollback"
arcave savepoint list --include-safety-snapshots
arcave rollback 1780340557
```

## Next step

[Troubleshooting](../troubleshooting.md) — what to do when a rollback did not finish.
