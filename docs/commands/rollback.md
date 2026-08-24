---
title: rollback
description: Restore a Docker Compose environment to an arcave savepoint — volumes, container writable layers and the recorded compose spec — with an automatic safety snapshot and revert.
keywords: [arcave rollback, restore environment, docker compose, safety snapshot, revert, volume restore, Open-Source, Greenmask]
---

# arcave rollback

Restores the environment to a savepoint — the reverse of [`savepoint create`](savepoint_create.md).

## Usage

```text
Usage: arcave rollback [savepoint-id|name|latest]
```

Exactly one argument is required.

## Flags

`rollback` has no flags of its own. The bound it waits for before deciding a start has failed is [`--wait-timeout`](index.md#global-flags), a global flag, because both `savepoint create` and `rollback` need it and one config key can hold exactly one binding.

```shell
arcave rollback latest --wait-timeout 5m
```

## Selecting the savepoint

Which kind of reference you gave is decided by the token itself:

| Token | Resolves as |
|---|---|
| `latest` | The most recently created savepoint. Reserved word. |
| A run of decimal digits | An **id**. Exact, and never falls back to a name. |
| Anything else | A **name**. |

```shell
arcave rollback latest
arcave rollback 1780335122
arcave rollback before-migration
```

An id is the only form that reaches a hidden [safety snapshot](../concepts.md#safety-snapshot), and it never falls back — `arcave rollback 1780335122` will not resolve to a savepoint merely *called* `1780335122`.

A name matching several savepoints is an **error** listing the candidate ids, never a silent guess.

## Behavior

```text title="The workflow, in order"
 1.  Claim the project lock
 2.  Resolve the savepoint record
 3.  Refuse if an interrupted rollback is marked
 4.  Build both backend sets — the savepoint's and the live one
 5.  Load and parse the savepoint's compose spec
 6.  Load every stored writable layer into the daemon and tag it
 7.  Stop the services
 8.  Take a safety snapshot of the live state
 9.  Write the inflight marker
10.  Remove the project's containers and networks — volumes are kept
11.  Recreate the stack from the savepoint's own compose spec, on the restored images
12.  Wipe and restore every volume
13.  Start the services and wait for health checks
14.  Remove the orphaned anonymous volumes, clear the marker, drop the safety snapshot
```

Steps 4 through 6 run **while the project is still up**, on purpose: a backend that cannot be built, a compose spec that will not load, or an image archive the daemon will not accept all fail the rollback before anything is torn down.

Volumes are **kept** at step 10. A volume arcave did not capture has no copy anywhere.

## What a rollback restores

**The stack the savepoint recorded, not the one that is running.** A service added since is gone. One removed comes back. A changed image, port, environment variable or healthcheck goes back to what it was. That is what `project.yaml` is for.

**The containers' writable layer.** Whatever a service wrote outside all of its mounts comes back from the image the savepoint committed. Every replica past the first is rebuilt on its own layer, since a compose service names one image while a layer is captured per replica.

**Every captured volume**, wiped and restored.

Restored images are tagged `arcave/<project>-<service>-<replica>-<base-image>:sp-<savepoint-id>` — for example `arcave/shop-db-1-postgres-16:sp-1780335122` — so `docker images` reads as one row per replica and one tag per savepoint.

:::note
Those tags are not collected. The running containers reference them, so nothing can be dropped at the end of the workflow; one tag per (service, replica, savepoint) survives each rollback. A `docker compose up` run by hand afterwards recreates the containers from *your* compose file and drops the restored layer with them.
:::

## Safety: the automatic revert

Before overwriting anything, arcave takes an ordinary savepoint of the live state, named `safety-snapshot-<target id>` and hidden from `savepoint list`.

If any step past that point fails — a restore error, or services that never become healthy within `--wait-timeout` — arcave replays that snapshot through the very instances that took it, restarts the services, and reports the original error. You end up where you started.

The revert runs on a context that ignores cancellation, with its own 10-minute budget: the Ctrl-C that aborted your rollback must not tear down the revert that is saving your environment.

| Outcome | The safety snapshot |
|---|---|
| Rollback succeeds | Deleted |
| Rollback fails, revert succeeds | Deleted |
| Rollback fails, **revert also fails** | **Kept**, its id printed, phase marked `inconsistent` |

In the last row it is the last copy of your pre-rollback state. See [Troubleshooting](../troubleshooting.md#an-interrupted-rollback).

:::warning
Rollback is destructive. Every captured volume is wiped before it is restored — bind-mounted host directories included. A volume mounted through `subpath:` is restored **whole**, not just the subdirectory the service sees, so everything else in that volume goes back to its savepoint state too.
:::

## Output

```json title="stdout"
{
  "rolled_back_to": {
    "id": "1780335122",
    "name": "before-migration",
    "description": "Before applying migration 2026_001",
    "created_at": "2026-06-01T19:41:12.123456789Z"
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
| `rolled_back_to` | The savepoint record that was restored. |
| `restored` | One entry per restored volume. `type` is `named`, `bind` or `anon`, with the matching `named`/`bind`/`anon` block. |
| `images` | One entry per restored writable layer, with the local tag it now answers to. |

## Progress

The health-check wait is where a rollback spends most of its time, so it is not silent:

```text title="stderr"
INF starting services
INF waiting for services to become healthy wait_timeout=2m0s
INF compose kind=Container name=myapp-db-1 status=Started
INF compose kind=Container name=myapp-db-1 status=Healthy
```

`--log-format json` turns those into records a CI job or an agent can parse. `--log-level debug` adds the in-flight steps: `Waiting`, `Starting`, `Creating`, image-pull progress.

## Errors

| Condition | What happens |
|---|---|
| No savepoint reference given | Refused — `rollback` requires exactly one argument. |
| The name matches several savepoints | Error, listing the candidate ids. |
| The id does not exist | `savepoint not found: <id>` |
| `latest` with no savepoints | `no savepoints exist` |
| The savepoint has no recorded compose spec | **Refused.** The stack is rebuilt from the spec, and there is nothing honest to rebuild it from. Savepoints taken before arcave recorded specs carry none. |
| An interrupted rollback is marked | Refused — except a rollback to that marker's own safety snapshot, which is the remedy. |
| A backend cannot be built | Fails **before** the stack is stopped. |
| An image archive the daemon rejects | Fails **before** the stack is torn down. |
| Services unhealthy within `--wait-timeout` | The safety snapshot is replayed automatically; the command reports the failure. |

## Examples

### Undo the last thing you did

```shell
arcave rollback latest
```

### Return to a named baseline

```shell
arcave rollback clean
```

### Restore a specific savepoint

```shell
arcave rollback 1780335122
```

### Give a slow stack more time before arcave decides it failed

```shell
arcave rollback latest --wait-timeout 5m
```

Worth raising whenever a service's health check is slower than two minutes — otherwise a healthy-but-slow start reads as a failed rollback and triggers a revert you did not want.

### Roll back inside a script

```shell
./migrate up || arcave rollback pre-migration
```

### Recover from an interrupted rollback

```shell
arcave savepoint list --include-safety-snapshots
arcave rollback 1780340557          # the safety snapshot, by id
```

See [Troubleshooting](../troubleshooting.md#an-interrupted-rollback) for the full procedure.

## Related docs

* [savepoint create](savepoint_create.md) — the operation this reverses.
* [How arcave works](../how_it_works.md#what-a-rollback-restores) — the mechanism in full.
* [Limitations](../limitations.md) — what a rollback does not put back.
