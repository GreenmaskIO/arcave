---
title: arcave rollback
description: Return a Docker Compose environment to an arcave savepoint — its volumes, its containers' writable layers and the compose spec it recorded — behind an automatic safety snapshot.
keywords: [arcave rollback, savepoint, safety snapshot, compose spec, volume data]
---

# arcave rollback

Returns the environment to a savepoint.

```text title="Synopsis"
arcave rollback <savepoint-id|name|latest>
```

Exactly one argument is required.

arcave rebuilds the project the savepoint recorded, puts the captured writable layers back, and writes every captured volume back into it. When the project is running, all of it happens behind a safety snapshot arcave replays by itself if anything fails. The whole sequence, its phases and their remedies are in [How arcave works](../../how_it_works.md#what-a-rollback-restores).

**A rollback is destructive.** Every volume it covers is overwritten with the savepoint's data — bind mounts, which are your own host directories, included; a volume a service reaches through `subpath:` goes back whole. Anything written since the savepoint is gone.

## Rolling back a project that is not running

A compose project with no containers — never started on this machine, or taken away with `docker compose down` — can still be rolled back: arcave builds it from the spec the savepoint recorded. There is nothing to stop, so there is no stop, no safety snapshot and no `compose down`; arcave creates the project, writes the volumes and starts it.

Named volumes the project still owns — what `docker compose down` leaves behind — are what [`rollback.leftover_volumes`](../configuration.md#rollback) decides about:

| Value | What happens to a leftover volume the savepoint restores into |
|---|---|
| `overwrite` (default) | Overwritten, with a warning naming it. No safety snapshot is taken, so its old contents are gone. |
| `refuse` | The rollback is refused before anything is created: `compose project is not running but its volumes survive: myapp_pgdata`. |

If such a rollback fails, arcave removes what it created. If it is interrupted, run the **same** rollback again: there is no safety snapshot to return to, and the interrupted-rollback marker lets that one through.

`savepoint create` still needs the project's containers.

## Flags

`rollback` has no flags of its own. The bound it waits on before deciding a start has failed is [`--wait-timeout`](index.md#global-flags), which is a config key and therefore lives on the root command.

```shell
arcave rollback latest --wait-timeout 5m
```

## Selecting the savepoint

| Token | Resolves as |
|---|---|
| `latest` | The savepoint with the newest `created_at`. A reserved word. |
| A canonical UUID | An **id**. Exact, and it never falls back to a name. |
| Anything else | A **name**. |

```shell
arcave rollback latest
arcave rollback 019e843e-1650-7000-8000-000000000000
arcave rollback before-migration
```

An id is the only form that reaches a hidden [safety snapshot](savepoint_list.md#safety-snapshots-are-hidden). A name matching several savepoints is an error listing the candidates, never a guess.

## Output

```json title="stdout"
{
  "rolled_back_to": {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "before-migration",
    "description": "before the v2 schema",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    "stats": [ … ],
    "writable_layers": [ … ],
    "images": [ … ],
    "spec": { "format": "compose-yaml" }
  },
  "restored": [
    {
      "object_id": "volume-named-myapp_pgdata",
      "compose_project": "myapp",
      "type": "named",
      "backend": "fullcopy",
      "named": {
        "volume_name": "myapp_pgdata",
        "used_by": [
          { "service": "db", "target": "/var/lib/postgresql/data" }
        ]
      }
    }
  ],
  "writable_layers": [
    {
      "service": "app",
      "replica": 1,
      "container_name": "myapp-app-1",
      "image_id": "sha256:aaaa…",
      "tag": "arcave/myapp-app-1-myapp-latest:sp-019e843e-1650-7000-8000-000000000000"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `rolled_back_to` | The savepoint record that was applied, whole. |
| `restored` | One entry per volume written back, as the savepoint recorded it. `type` is `named`, `bind` or `anon`, with the matching block. |
| `writable_layers` | One entry per container layer put back: the container it was rebuilt as, the image id, and the `tag` it now answers to in your local daemon. Omitted when the savepoint held no layer. |

Each restored layer is tagged `arcave/<compose-project>-<service>-<replica>-<base image>:sp-<savepoint id>`, so `docker images` reads as one row per replica and one tag per savepoint. The containers run on those tags: a `docker compose up` of your compose file recreates them on its own images and drops the restored layers — roll back again to get them back. [`arcave gc`](gc.md) removes a tag once no container uses it.

## Errors

| Condition | What happens |
|---|---|
| No argument | cobra refuses the call: `rollback` takes exactly one. |
| The id is not there | `finding savepoint "019e843e-1650-7000-8000-000000000000": savepoint not found: 019e843e-1650-7000-8000-000000000000` |
| The name is not there | `finding savepoint "clean": savepoint not found: clean` |
| The name matches several | `finding savepoint "clean": 2 savepoints are named "clean"; roll back by id instead: 019e843e-1650-7000-8000-000000000000, 019e8488-3ac8-7000-8000-000000000000` |
| `latest` or a name, with an empty store | `finding savepoint "latest": no savepoints exist` |
| The savepoint carries no compose spec | `load the savepoint's compose spec: savepoint carries no compose spec: 019e843e-1650-7000-8000-000000000000`. The project is rebuilt from that spec, and savepoints taken before arcave recorded one carry none. |
| The Docker daemon is older than 25 | `prepare: unsupported docker daemon: the daemon reports version 24.0.7; arcave needs Docker 25 or newer` — before anything is stopped. |
| A stored layer the Docker daemon will not take | `restore the savepoint's writable layers: …` — raised **before** anything is stopped. |
| A service would be rebuilt on an image the savepoint does not hold and the daemon lacks | `image is neither in the savepoint nor in the daemon: service app runs on myapp:latest` — before anything is stopped. Pull or build the image, then roll back again. |
| The project is not running, its named volumes survive, and `rollback.leftover_volumes` is `refuse` | `compose project is not running but its volumes survive: myapp_pgdata`. Nothing is created. |
| An interrupted rollback is marked | `require no inflight: rollback to savepoint 019e843e-1650-7000-8000-000000000000 was interrupted (restoring)` — except the rollback that is the remedy, which passes: one to the marker's own safety snapshot, or, after a rollback of a project that was not running, that same rollback again. The log names the command to run. |
| Another arcave run holds the lock | `another arcave command is already running in this project` |
| The safety snapshot fails | `pre-rollback safety snapshot failed, nothing changed: …`. The services are started again. |
| Anything fails after that | `rollback failed: …`, after the safety snapshot has been replayed. You are where you started. |
| The replay fails too | The same error, with the safety snapshot **kept**, its id in the log, and the marker phased `inconsistent`. It is now the last copy of the pre-rollback state — see [Troubleshooting](../troubleshooting.md#an-interrupted-rollback). |

Everything that can fail is done while the project is still up: both backend sets are built, the recorded spec is parsed, and every stored layer is loaded into the daemon before the first container is stopped.

What the rollback puts back is what the savepoint holds, whatever the compose file's [capture labels](../capture_labels.md) say now. A container whose layer the savepoint left out is created fresh from its image, and an anonymous volume it left out comes back empty. The safety snapshot captures everything the rollback is about to overwrite, whatever the labels say.

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

```shell title="Give a slow project more time before arcave calls it a failure"
arcave rollback latest --wait-timeout 5m
```

Worth raising whenever a health check is slower than two minutes: a healthy-but-slow start otherwise reads as a failed rollback and triggers a replay you did not want.

```shell title="Recover from an interrupted rollback"
arcave savepoint list --include-safety-snapshots
arcave rollback 019e8491-04c8-7000-8000-000000000000
```

## Next step

[Troubleshooting](../troubleshooting.md) — what to do when a rollback did not finish.
