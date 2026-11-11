---
title: arcave rollback
description: Return a Docker Compose environment to an arcave savepoint — its volumes, its containers' writable layers and the compose spec it recorded — behind an automatic safety snapshot.
keywords: [arcave rollback, savepoint, safety snapshot, compose spec, volume data]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# arcave rollback

Returns the environment to a savepoint.

```text title="Synopsis"
arcave rollback <savepoint-id|name|latest>
```

Exactly one argument is required.

arcave rebuilds the project the savepoint recorded, puts the captured writable layers back, and writes every captured volume back into it. When the project is running, all of it happens behind a safety snapshot arcave replays by itself if anything fails, and which stays afterwards: rolling back to its id, which the result names, undoes the rollback. The whole sequence, its phases and their remedies are in [How arcave works](../../how_it_works.md#what-a-rollback-restores).

**A rollback is destructive.** Every volume it covers is overwritten with the savepoint's data — bind mounts, which are your own host directories, included; a volume a service reaches through `subpath:` goes back whole. Anything written since the savepoint is gone.

## Rolling back a project that is not running

A compose project with no containers — never started on this machine, or taken away with `docker compose down` — can still be rolled back: arcave builds it from the spec the savepoint recorded. There is nothing to stop, so there is no stop, no safety snapshot and no `compose down`; arcave creates the project, writes the volumes and starts it.

Named volumes the project still owns — what `docker compose down` leaves behind — are what [`rollback.leftover_volumes`](../configuration.md#rollback) decides about:

| Value | What happens to a leftover volume the savepoint restores into |
|---|---|
| `overwrite` (default) | Overwritten, named in the result's warnings and `leftover_volumes`. No safety snapshot is taken, so its old contents are gone. |
| `refuse` | The rollback is refused before anything is created: `compose project is not running but its volumes survive: myapp_pgdata`. |

If such a rollback fails, arcave removes what it created. If it is interrupted, run the **same** rollback again: there is no safety snapshot to return to, and the interrupted-rollback marker lets that one through.

`savepoint create` still needs the project's containers.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--no-safety-snapshot` | | Capture neither the live volumes nor the writable layers: faster, but what they held is gone for good, nothing can undo the rollback, and after a failure the project stays as it is until you run the same rollback again. |
| `--safety-snapshot` | | Capture the live volumes and writable layers first. |

Without either flag, [`rollback.safety_snapshot`](../configuration.md#rollback) decides, and takes a safety snapshot unless set to `false`. Either flag overrides it for one run; they cannot be combined, and neither takes a value.

The bound it waits on before deciding a start has failed is [`--wait-timeout`](index.md#global-flags), which is a config key and therefore lives on the root command.

```shell
arcave rollback latest --wait-timeout 5m
```

## Safety snapshots pile up until you run gc

Every rollback of a running project leaves a safety snapshot. [`arcave gc`](gc.md) retires them, past the newest [`gc.safety_snapshots.retain_recent`](../configuration.md#gc) of each compose project, and **it runs only when you run it**: periodically, or right after the rollback.

```shell
arcave rollback latest && arcave gc
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

On a terminal, the final tree of the steps on stderr and a summary on stdout; in a script, or with `--format json`, JSON on stdout.

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ rollback before-migration                                       8.6s
├── ✔ project.introspect                                          0.0s
├── ✔ rollback.prepare                                            1.3s
├── ✔ rollback.safety-snapshot                                    2.7s
│   └── ✔ savepoint.capture 019e8491-04c8-7000-8000-000000000000  2.4s
│         ! not covered: 1 service(s) without volumes
├── ✔ rollback.apply                                              3.5s
└── ✔ rollback.cleanup                                            0.0s

Rolled back to before-migration · 019e843e-1650-7000-8000-000000000000 · 2026-06-01 19:41:12
  volumes   1
  layers    2
  safety    taken

Warnings (1)
  ! the containers run on the savepoint's writable layers
    → `docker compose up` of your compose file recreates them on its images and drops those layers

  undo it:  arcave -p myapp rollback 019e8491-04c8-7000-8000-000000000000
```

</TabItem>
<TabItem value="json" label="JSON">

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
    "spec": { "format": "compose-yaml" },
    "not_covered": { … }
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
      "image_id": "sha256:f68cd146c87c7e1e7606d02065a872eed3eafafa97ca55b8836c2785445b72da",
      "tag": "arcave/myapp-app-1-myapp-latest:sp-019e843e-1650-7000-8000-000000000000"
    },
    {
      "service": "db",
      "replica": 1,
      "container_name": "myapp-db-1",
      "image_id": "sha256:6b3f960b737dff8f5ac90b9538610413cafe14be340f3635dc4cbb279ddc3035",
      "tag": "arcave/myapp-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000"
    }
  ],
  "safety_snapshot": "019e8491-04c8-7000-8000-000000000000"
}
```

</TabItem>
</Tabs>

Rolling back to a safety snapshot — undoing a rollback — titles the summary by the state it holds, the one before the rollback it was taken before, at that rollback's time; the savepoint named is that rollback's target, not what was restored:

```text title="stdout"
Rolled back to the state before rollback to before-migration · 019e8491-04c8-7000-8000-000000000000 · 2026-06-01 21:22:37
```

| Field | Meaning |
|---|---|
| `rolled_back_to` | The savepoint record that was applied, whole. A safety snapshot's has an empty `name` and carries `taken_before_rollback_to`. |
| `restored` | One entry per volume written back, as the savepoint recorded it. `type` is `named`, `bind` or `anon`, with the matching block. |
| `writable_layers` | One entry per container layer put back: the container it was rebuilt as, the image id, and the `tag` it now answers to in your local daemon. Omitted when the savepoint held no layer. |
| `safety_snapshot` | The id of the safety snapshot the rollback took: rolling back to it undoes the rollback. Omitted when none was taken. |

Each restored layer is tagged `arcave/<compose-project>-<service>-<replica>-<base image>:sp-<savepoint id>`, so `docker images` reads as one row per savepoint under a row per replica. A rollback to the same savepoint again puts the same tag back. The containers run on those tags: a `docker compose up` of your compose file recreates them on its own images and drops the restored layers — roll back again to get them back. [`arcave gc`](gc.md) removes a tag once no container uses it.

Every container the rollback builds carries `io.arcave.deployment.*` labels: when it was built, the session and the savepoint by id and name, when the savepoint was taken and whether it is a safety snapshot, and the replicas it recorded. [`arcave status`](status.md) reads them to tell what is deployed and what was rebuilt outside arcave since; no network or volume gets them, since compose would take a label it did not write for a change and recreate them.

## Errors

| Condition | What happens |
|---|---|
| No argument | cobra refuses the call: `rollback` takes exactly one. |
| The id is not there | `finding savepoint "019e843e-1650-7000-8000-000000000000": savepoint not found: 019e843e-1650-7000-8000-000000000000` |
| The name is not there | `finding savepoint "clean": savepoint not found: clean` |
| The name matches several | `finding savepoint "clean": 2 savepoints are named "clean"; name one by id instead: 019e843e-1650-7000-8000-000000000000, 019e8488-3ac8-7000-8000-000000000000` |
| `latest` or a name, with an empty store | `finding savepoint "latest": no savepoints exist` |
| The savepoint carries no compose spec | `load the savepoint's compose spec: savepoint carries no compose spec: 019e843e-1650-7000-8000-000000000000`. The project is rebuilt from that spec, and savepoints taken before arcave recorded one carry none. |
| The Docker daemon is older than 25 | `prepare: unsupported docker daemon: the daemon reports version 24.0.7; arcave needs Docker 25 or newer` — before anything is stopped. |
| A Docker daemon older than 25 whose version arcave could not read, on a running project | `pre-rollback safety snapshot failed, nothing changed: capturing volumes: … unsupported image archive format: …`. The services are started again. |
| A stored layer the Docker daemon will not take | `restore the savepoint's writable layers: …` — raised **before** anything is stopped. |
| A service would be rebuilt on an image the savepoint does not hold and the daemon lacks | `image is neither in the savepoint nor in the daemon: service app runs on myapp:latest` — before anything is stopped. Pull or build the image, then roll back again. |
| The project is not running, its named volumes survive, and `rollback.leftover_volumes` is `refuse` | `compose project is not running but its volumes survive: myapp_pgdata`. Nothing is created. |
| An interrupted rollback is marked | `require no inflight: rollback to savepoint 019e843e-1650-7000-8000-000000000000 was interrupted (restoring); recover by rolling back to 019e8491-04c8-7000-8000-000000000000` — except the rollback that is the remedy, which passes: one to the marker's own safety snapshot, or, after a rollback that took none, one to any savepoint of the session and compose project. The log names the command to run. The remedy takes no safety snapshot, since it would capture half-restored volumes, and a failure leaves the marker standing. |
| Another arcave run holds the lock | `another arcave command is already running in this project` |
| The safety snapshot fails | `pre-rollback safety snapshot failed, nothing changed: …`. The services are started again. |
| Anything fails after that | `rollback failed: …`, after the safety snapshot has been replayed. You are where you started. |
| The marker can't be written, under `--no-safety-snapshot` | `recording the inflight marker, nothing changed: …`. The project keeps running. |
| Anything fails after that, under `--no-safety-snapshot` | `rollback failed: …`, with nothing cleaned up or started and the marker standing. Run the same rollback again, or one to another savepoint. |
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

```shell title="Undo a rollback that finished"
arcave rollback 019e8491-04c8-7000-8000-000000000000    # the id its result named
```

```shell title="Recover from an interrupted rollback"
arcave savepoint list --include-safety-snapshots
arcave rollback 019e8491-04c8-7000-8000-000000000000
```

## Next step

[Troubleshooting](../troubleshooting.md) — what to do when a rollback did not finish.
