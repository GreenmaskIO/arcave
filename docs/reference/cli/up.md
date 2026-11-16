---
title: arcave up
description: Bring a Docker Compose environment up from an arcave savepoint where nothing of it runs — never over a live one, unless --force drops that first with no backup.
keywords: [arcave up, savepoint, docker compose, environment, cold start, force]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# arcave up

Brings the environment up from a savepoint.

```text title="Synopsis"
arcave up [<savepoint-id|name|latest>] [--force]
```

At most one argument; without one it is `latest`, the newest savepoint.

arcave builds the compose project from the spec the savepoint recorded, puts the captured writable layers back, writes every captured volume into it and starts it. It builds over **nothing**: while the Docker daemon still holds a container of the compose project, or a volume it owns, `up` is refused before anything is written. That is what tells it apart from [`rollback`](rollback.md), which replaces a running environment behind a safety snapshot. `up` takes none — there is nothing to keep.

The usual pair is [`arcave down`](down.md) and `arcave up`: end the environment, bring it back later from a savepoint, the same one or another.

```shell
arcave savepoint create before-break
arcave down
arcave up before-break
```

## Refused over a present environment

Every container and every volume of the compose project counts, not only those the savepoint restores: compose would reuse a volume it found and mix its old data into the build. A `docker compose down` without `-v` leaves the named volumes, and they refuse an `up` all the same:

```text
the daemon holds the compose project already: volumes myapp_pgdata; pass --force to drop all of it first, with no backup; 'arcave rollback' replaces a running environment and keeps a safety snapshot
```

A network holds no data, and does not refuse it.

**`--force` is destructive.** It drops every container, volume and network of the compose project first, with **no backup** — volumes no savepoint holds included — and then builds the savepoint. To replace a running environment and keep a way back, run `arcave rollback` instead: its safety snapshot undoes it.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--force` | | Drop every container, volume and network of the compose project first, with no backup. |

The bound it waits on before deciding a start has failed is [`--wait-timeout`](index.md#global-flags), which is a config key and therefore lives on the root command.

```shell
arcave up latest --wait-timeout 5m
```

## Selecting the savepoint

The argument reads as [`rollback`'s](rollback.md#selecting-the-savepoint) does: `latest`, an id, or a name; omitted, it is `latest`. An id is the only form that reaches a hidden [safety snapshot](savepoint_list.md#safety-snapshots-are-hidden).

## How it builds

What `up` builds is what a [rollback of a project that is not running](rollback.md#rolling-back-a-project-that-is-not-running) builds: the project is created from the recorded spec, the volumes and layers are written into it, and it is started. Every container gets the `io.arcave.deployment.*` labels a rollback writes, so [`arcave status`](status.md) and [`arcave down`](down.md) know it as this session's. [`rollback.leftover_volumes`](../configuration.md#rollback) plays no part: a leftover volume refuses an `up`, or `--force` drops it.

If the build fails, arcave removes what it created, and a forced `up` has already dropped what was there. If it is interrupted, the interrupted-rollback marker stands, as after a rollback that took no safety snapshot: recover with `arcave rollback` to any savepoint of the session and compose project — see [Troubleshooting](../troubleshooting.md#an-interrupted-rollback).

`up` claims the project lock and the session's lock, as a rollback does.

## Output

On a terminal, the final tree of the steps on stderr and a summary on stdout; in a script, or with `--format json`, JSON on stdout.

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
Up from before-migration · 019e843e-1650-7000-8000-000000000000 · 2026-06-01 19:41:12
  volumes   1
  layers    2
  dropped   2 containers, 1 volume, 1 network

Warnings (1)
  ! the containers run on the savepoint's writable layers
    → `docker compose up` of your compose file recreates them on its images and drops those layers
```

`dropped` is there only when `--force` dropped something.

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "rolled_back_to": {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "before-migration",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    …
  },
  "restored": [
    { "object_id": "volume-named-myapp_pgdata", "compose_project": "myapp", "type": "named", … }
  ],
  "writable_layers": [
    { "service": "app", "replica": 1, "container_name": "myapp-app-1", "image_id": "sha256:aaaa…",
      "tag": "arcave/myapp-app-1-myapp-latest:sp-019e843e-1650-7000-8000-000000000000" }
  ],
  "discarded": {
    "containers": ["myapp-app-1", "myapp-db-1"],
    "volumes": ["myapp_pgdata"],
    "networks": ["myapp_default"]
  }
}
```

</TabItem>
</Tabs>

| Field | Meaning |
|---|---|
| `rolled_back_to` | The savepoint record that was built, whole. |
| `restored` | One entry per volume written, as in [`rollback`](rollback.md#output). |
| `writable_layers` | One entry per container layer put back, with the `tag` it now answers to. Omitted when the savepoint held no layer. |
| `discarded` | What a forced `up` dropped first, by docker name, as read before the drop. An anonymous volume goes with its container and is not listed. Omitted when the daemon held nothing of the compose project. |

There is no `safety_snapshot`: `up` takes none.

## Errors

| Condition | What happens |
|---|---|
| The daemon holds containers or volumes of the compose project | `the daemon holds the compose project already: containers …; volumes …`, with the way past it. Nothing is written. |
| `latest` or a name, and the session holds no savepoint of the compose project | `finding savepoint "latest": no savepoints exist; start the compose project with 'docker compose up -d', then take one with 'arcave savepoint create'` |
| The id or the name is not there, or the name matches several | As for [`rollback`](rollback.md#errors). |
| An interrupted rollback is marked | `require no inflight: rollback to savepoint … was interrupted (…); recover by rolling back to …` — with `--force` too. Recovering is `rollback`'s. |
| The savepoint carries no compose spec | `load the savepoint's compose spec: savepoint carries no compose spec: …` |
| The Docker daemon is older than 25 | `unsupported docker daemon: …` — before anything is dropped. |
| A forced drop leaves a container or a volume — something outside the compose project still holds it | `the daemon still holds the compose project after its drop: volumes myapp_pgdata: …`, naming the holder; nothing is built. See [Troubleshooting](../troubleshooting.md#the-daemon-still-holds-the-compose-project-after-its-drop). |
| Another arcave run holds the lock | `another arcave command is already running in this project` |
| The build fails | `up failed: …`, after arcave removed what it created. |

## Examples

```shell title="End the environment, bring it back from where you left it"
arcave savepoint create end-of-day
arcave down
arcave up                           # latest
```

```shell title="Start a fresh checkout from a named savepoint"
arcave up seed
```

```shell title="Throw away what docker compose down left, and build the savepoint"
arcave up before-migration --force
```

## Next step

[`arcave down`](down.md) — end the environment arcave brought up.
