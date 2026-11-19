---
title: arcave down
description: End a Docker Compose environment arcave deployed — containers, networks, owned and anonymous volumes and restored layer tags — once its deployment labels prove it is this session's.
keywords: [arcave down, docker compose down, environment, teardown, restored layers, force]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# arcave down

Ends the environment on the Docker daemon.

```text title="Synopsis"
arcave down [--force]
```

No arguments.

arcave takes everything of the compose project off the daemon: its containers, its networks, the volumes it owns, its containers' anonymous volumes and its [restored layer tags](rollback.md#output). Bind mounts — your own host directories — and external volumes stay. Where `docker compose down` leaves the named volumes behind, `arcave down` leaves nothing for an [`arcave up`](up.md) to trip on.

**A down is destructive.** It takes no savepoint: whatever changed since the last one is lost, and so is every volume a savepoint leaves out. Take one first:

```shell
arcave savepoint create before-down
arcave down
```

## Whose environment it takes

A rollback and an `up` label every container they build with `io.arcave.deployment.*` — the arcave project, the session and the savepoint. `down` reads those labels on every replica before it removes anything, and goes ahead only when each one names this session of this arcave project. It is refused when:

* **a replica was built in another session**, or by another checkout under another `project.name`:

  ```text
  the environment was deployed from elsewhere: session "feat/add-new-table" (019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31); run it in that session with --session feat/add-new-table, or pass --force to take it down anyway
  ```

* **a replica carries no deployment labels** — `docker compose up` of your own file built or recreated it — or no replica is left and containers, volumes or networks of the compose project are:

  ```text
  the environment was not deployed by arcave: no deployment labels on app-1; pass --force to take it down anyway
  ```

A one-off container or a hook's runner names no deployment and is not judged; it goes with the rest. A compose project the daemon holds nothing of is not refused: `down` reports that there was nothing to take down.

[`arcave status`](status.md) shows the same labels before you decide.

## `--force`

`--force` takes the environment down whoever deployed it, and clears an interrupted-rollback marker of **this** compose project, from any session. That rollback's safety snapshot is the last copy of the state before it: the result names it in `cleared_inflight`, a warning says so, and only retention keeps [`arcave gc`](gc.md) from retiring it — the first newer safety snapshot of the compose project retires it under the default `retain_recent: 1`. Roll back to it before then if you still want that state.

A marker of another compose project refuses the down, forced or not: it guards that one's environment.

## Locks

`down` claims the project lock on `.arcave/lock`, and no session lock: it writes nothing to the store, and the marker it clears lives on the daemon. It needs a reachable daemon, the initialised project and a [named compose project](index.md#naming-the-project).

## Flags

| Flag | Default | Description |
|---|---|---|
| `--force` | | Take the environment down whoever deployed it, and clear this compose project's inflight marker. |

## Output

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
Took down myapp
  deployed  before-migration (019e843e-1650-7000-8000-000000000000) · session feat/add-new-table
  dropped   2 containers, 1 volume, 1 network
  layers    2 tags removed
```

Forced, over a compose project that held nothing:

```text title="stdout"
Nothing of myapp to take down
  dropped   nothing

Warnings (1)
  ! forced: whose environment it was was not checked
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "compose_project": "myapp",
  "session": { "id": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "name": "feat/add-new-table" },
  "deployment": {
    "deployed_at": "2026-06-02T09:12:40Z",
    "arcave_project": "myapp",
    "session": { "id": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "name": "feat/add-new-table" },
    "savepoint": { "id": "019e843e-1650-7000-8000-000000000000", "name": "before-migration",
                   "created_at": "2026-06-01T19:41:12.123456789Z", "safety_snapshot": false }
  },
  "forced": false,
  "discarded": {
    "containers": ["myapp-app-1", "myapp-db-1"],
    "volumes": ["myapp_pgdata"],
    "networks": ["myapp_default"]
  },
  "removed_tags": [
    "arcave/myapp-app-1-myapp-latest:sp-019e843e-1650-7000-8000-000000000000",
    "arcave/myapp-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000"
  ]
}
```

</TabItem>
</Tabs>

| Field | Meaning |
|---|---|
| `compose_project` | The compose project taken down. |
| `session` | The session the down ran in. |
| `deployment` | What the replicas' labels named — this session's newest build, or, forced, the first foreign one. Omitted when no replica named one. |
| `forced` | `true` when `--force` skipped the owner check. |
| `cleared_inflight` | The interrupted-rollback marker a forced down cleared — `phase`, `session`, `compose_project`, `target`, `safety`, `at`. Omitted when it cleared none. |
| `discarded` | The containers, volumes and networks of the compose project, by docker name, as read before the drop. An anonymous volume goes with its container and is not listed. Every list is `[]` when there was nothing. |
| `removed_tags` | The `arcave/…:sp-<savepoint>` tags of the compose project's restored layers that came off, whichever session restored them. |
| `notices` | What `down` left behind and why — a network something else still uses, a tag that would not come off, the safety snapshot of a cleared marker. Omitted when there is none. |

A network another container still uses and a tag the daemon will not remove are notices, never the down's failure. A volume something outside the compose project still holds is: see below.

## Errors

| Condition | What happens |
|---|---|
| A replica was deployed by another session or arcave project | `the environment was deployed from elsewhere: session …`, with the `--session` to run it in. Nothing is removed. |
| A replica has no deployment labels, or no replica is left but containers, volumes or networks of the compose project are | `the environment was not deployed by arcave: …`. Nothing is removed. |
| An interrupted rollback of this compose project is marked | `rollback to savepoint … was interrupted (…); recover by rolling back to …` — `--force` takes it down and clears the marker. |
| An interrupted rollback of another compose project is marked | The same error, `--force` or not. |
| A container or a volume survives the drop — something outside the compose project holds it | `the daemon still holds the compose project after its drop: volumes myapp_pgdata: …`, naming the holder. The marker and the tags stay. See [Troubleshooting](../troubleshooting.md#the-daemon-still-holds-the-compose-project-after-its-drop). |
| Another arcave run holds the lock | `another arcave command is already running in this project` |

## Examples

```shell title="Keep the state, end the environment"
arcave savepoint create before-down
arcave down
```

```shell title="Take down what another session deployed, from that session"
arcave --session feat/add-new-table down
```

```shell title="Take down what docker compose up built"
arcave down --force
```

## Next step

[`arcave up`](up.md) — bring it back from a savepoint.
