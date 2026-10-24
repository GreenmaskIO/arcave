---
title: arcave gc
description: Report the garbage arcave leaves in the store and the Docker daemon, without deleting it.
keywords: [arcave gc, garbage collection, dry run, savepoint data, deduplicated volumes, image blobs, restored layers]
---

# arcave gc

Reports what no live savepoint of a session keeps alive. The report covers the session's store, every compose project in it, and the Docker daemon. In this release gc only reports, and `--dry-run` is required.

```text title="Synopsis"
arcave gc --dry-run [--session <session>] [--kind <kind>…] [--restored-layer-grace <duration>] [flags]
```

gc reads and nothing else, and stops nothing. It holds the project lock while it reads, so it refuses to run while another arcave command in the project is working, and waits for nothing. gc works on one session: the one `--session` names, or the current one. It looks at every compose project in that session, so it refuses `-p`. It ignores `ARCAVE_PROJECT_NAME`, which you may have exported for the savepoint commands. Only `restored-layers` and `deduplicated-volumes` need the Docker daemon: the second runs restic in it.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--dry-run` | `false` | Report the garbage without deleting it. Required for now. |
| `--kind` | every kind | The kinds of data to look at: `savepoint-data`, `deduplicated-volumes`, `image-layers`, `upstream-images`, `restored-layers`. Repeat the flag, or separate kinds with commas. |
| `--restored-layer-grace` | `gc.restored_layer_grace` | How long ago a layer tag must have been tagged before it counts as garbage. `0` counts every unused tag. |

Global flags apply — see [the CLI reference](index.md#global-flags).

## How gc decides what is alive

A savepoint's status decides what it keeps alive. Whatever only a dead savepoint reaches is garbage. gc never judges an object by its age.

| Status | How gc tells | What it keeps alive |
|---|---|---|
| committed | The savepoint has its `savepoint.json`. The record outranks the heartbeat. | Everything it names. Safety snapshots count. |
| in progress | No record. Its `heartbeat.json` reads `in-progress` and was written in the last 30 minutes, by the store's clock. | Its own tree, and every shared object of its compose project, because what the capture names isn't known until its record. |
| dead | No record. Its heartbeat reads `failed`, has gone stale, or is missing. A capture writes its heartbeat before its first object, so a missing one means it never got far. | Nothing. |
| unknown | No record, and the heartbeat can't be read. | Its own tree, and every shared object of its compose project, all left unjudged. |

`savepoint create` keeps its heartbeat fresh while it captures, and so does the safety snapshot a rollback takes. A capture that can't write its heartbeat for 25 minutes cancels itself and commits nothing, so a gc on another host never takes a live capture's tree for dead.

## What gc looks at

gc looks for five kinds of data. A savepoint's record is the root that keeps data alive; the data is what hangs off it.

| Kind | What it is | Garbage when |
|---|---|---|
| `savepoint-data` | A savepoint's own directory: its record, its compose spec, the volumes the `fullcopy` backend streamed, and its writable layers' archives and manifests. No other savepoint shares it. | The directory has no `savepoint.json` and its capture is dead. A live one is skipped as `capture-in-progress`; one of unknown status is left unjudged as `status-unknown`. A failed or interrupted create leaves one, and so does a remove. |
| `deduplicated-volumes` | A snapshot in a `cdc` backend's restic repository, which shares its data across savepoints. | The savepoint the snapshot's `arcave-savepoint` tag names is dead. A capture in progress is skipped as `capture-in-progress`; one of unknown status is left unjudged as `status-unknown`. |
| `image-layers` | The layers and configs of stored images, shared by writable layers and upstream images alike. | No manifest still stored lists it, and no capture in its compose project is in progress or of unknown status. |
| `upstream-images` | An image a service's `image:` names, stored once for every savepoint; its layers are `image-layers`. | No savepoint names it, and no capture in its compose project is in progress or of unknown status. |
| `restored-layers` | A writable layer a rollback restored into the Docker daemon, tagged `arcave/…:sp-<id>`. | No container uses it, by reference or by image, running or stopped, and it was tagged longer ago than `--restored-layer-grace` (`0` by default). Only layers this arcave project committed for a compose project of the session are judged. |

Each report names its `data_kind` and the `collector` that found it: how arcave looks for that kind of data in its store, such as `restic-repositories` for `deduplicated-volumes`.

While a capture in a compose project is in progress, the `image-layers` and `upstream-images` kinds judge nothing there: each object is skipped as `capture-in-progress`. This doesn't make the run partial. Nothing failed: run gc again once the capture finishes. While a capture's status is unknown, the same objects are left unjudged as `status-unknown` instead, and the run is partial.

A dead savepoint's manifests hold no layer, so the layers only it lists are garbage in the same run. Every other manifest still in the store keeps its layers, including the manifest of an upstream image no savepoint names. Such a layer is skipped as `listed-by-unreferenced` and becomes garbage only once that image is gone. A manifest no savepoint names holds nothing when a capture would write it again: one that is missing, lists nothing, is torn, or names another image. Any other manifest that can't be read makes `image-layers` refuse to judge that compose project, because it might list any layer. The report lists it under `unreadable`, with the savepoint that names it when one does.

:::note[Freeing an image takes two runs]
An unused upstream image and its layers are not freed in the same run. The first run finds the image under `upstream-images` and holds its layers back as `listed-by-unreferenced`; the run after the image is removed finds the layers under `image-layers`. This is on purpose: a capture that finds an image's manifest intact reuses its layers without writing them, so a layer may only go once no manifest lists it.
:::

`deduplicated-volumes` asks restic, not the store's files: it lists each repository's snapshots and judges each by the savepoint its tag names, so a capture in progress holds back only its own snapshots. It takes no restic lock, so it doesn't stop a backup running on another host. A savepoint gc hadn't seen when it read the store is looked up again, so a capture that started since is skipped. A snapshot with no `arcave-savepoint` tag wasn't taken by arcave. It's counted under `unattributed` and never judged. A repository restic can't list is left unjudged as `backend-unreadable`, with restic's error. The other repositories are still judged. Any unjudged object makes the run partial.

A layer tag is matched by the labels its image carries: the arcave project and compose project the layer was committed under. A tag whose image carries neither, such as one committed by an earlier arcave version, is counted under `unattributed` and never judged. A tag of another compose project of the arcave project is counted under `other_compose_projects`: a gc of the session holding that compose project judges it. Remove a tag no session holds any more by hand.

The project lock stops gc from running while a capture in the same project directory writes. A gc running on another host, against the same store, doesn't see that lock, and reads the capture's heartbeat instead. A capture that starts after such a gc has read the store may reuse a blob that gc names. This matters only once gc deletes.

A layer tag has no heartbeat. A rollback tags the layers it loads before it creates the containers that use them, and takes its safety snapshot in between, so for that time the new tags are unused. A gc in the same project directory waits for the rollback's lock. A gc in another directory of the same arcave project doesn't: two checkouts sharing `project.name` and a daemon each have their own lock. Pass `--restored-layer-grace`, or set `gc.restored_layer_grace`, to hold back the tags such a rollback has just loaded. A layer tag the daemon reports no tag time for is left unjudged as `tag-time-unknown` while a grace is set.

A session directory without `session.json`, which a failed fork leaves, belongs to no session, so gc doesn't report it. `arcave session list --all` lists it with its status, and `arcave session remove <id>` deletes it.

## Output

stdout carries one report per collector for each compose project of the session, what each dead savepoint frees, and a total. A `restored-layers` report has no `compose_project`: it covers the daemon. Each report has a `summary` that every kind shares, and a `detail` specific to its kind. `detail.ops` lists the actions a real gc would take.

```json title="stdout"
{
  "dry_run": true,
  "session": "00000000-0000-0000-0000-000000000001",
  "reports": [
    {
      "data_kind": "image-layers",
      "collector": "oci-blobs",
      "compose_project": "demo",
      "summary": {"found": 1, "bytes": 52428800, "skipped": 0, "unjudged": 0},
      "detail": {
        "ops": [{
          "action": "delete-object",
          "target": "shared/oci/blobs/sha256/4f…",
          "bytes": 52428800,
          "reason": "unreferenced",
          "last_modified": "2026-10-12T09:14:03Z",
          "savepoint": "019e843e-1650-7000-8000-000000000002"
        }],
        "live": 12
      }
    }
  ],
  "savepoints": [
    {"compose_project": "demo", "savepoint": "019e843e-1650-7000-8000-000000000002", "found": 2, "bytes": 52430912}
  ],
  "total": {"found": 1, "bytes": 52428800, "bytes_complete": true, "skipped": 0, "unjudged": 0, "aborted": 0, "failed": 0}
}
```

An op's `savepoint` names the dead savepoint whose removal alone frees the target: its tree, each blob that only its manifests list, and each `cdc` snapshot its capture took. `savepoints` adds up those ops per dead savepoint, across kinds. A stored image is never attributed: what a capture meant to reference with it is lost with the capture.

A `deduplicated-volumes` op is a `forget` of one snapshot, and its `bytes` is `null`: a snapshot shares its data, so only a prune of the whole repository can say what forgetting it frees. gc asks restic for that estimate, without changing anything, once per repository that holds dead snapshots. `detail.estimates` lists each repository, the snapshots it would forget, and `freed_bytes`: the total restic's prune dry run reports. It is rounded the way restic prints it, to three decimals of its unit. The report's `summary.bytes` adds up the estimates. A dead savepoint's `bytes` under `savepoints` is `null` when it has `cdc` snapshots, because the estimate is per repository, not per savepoint.

`bytes` is `null` when gc can't say how much would be freed:

* an estimate restic didn't give: `freed_bytes` is `null`, and so is the report's `summary.bytes`;
* a `restored-layers` tag: the image may share layers with other images.

`total.bytes_complete` is then `false`.

A target is relative to the compose project's directory in the store. A `restored-layers` target is the tag itself.

## Errors

| Condition | What happens |
|---|---|
| `--dry-run` is not passed | `garbage collection deletes nothing yet: only a dry run is supported: pass --dry-run`. Nothing is opened first. |
| A kind could not be judged, an object was left unjudged, or a collector failed | The full report is printed, then the command exits `1` with `garbage collection was partial`. |
| `--restored-layer-grace` is negative | `invalid garbage collect request: negative restored layer grace -1h0m0s`. Nothing is opened first. |
| `--kind` names a kind arcave does not know | `invalid garbage collect request: unknown kind "restic"`. Nothing is opened first. |
| Another arcave command holds the project lock | The command fails at once, without reading anything. |
| No daemon answers within 10 seconds | The `restored-layers` and `deduplicated-volumes` reports are `aborted` with the ping's error, whether or not `--kind` names them, and a warning says each is not judged. The other kinds still run, and the run is partial: the full report is printed, then the command exits `1`. |
| A `docker.*` setting is wrong | The command fails as every command does, whichever kinds run. |
| `-p/--project-name` is passed | `gc works on a session, every compose project in it: drop -p/--project-name` |
| The session was removed after gc resolved it | The command fails with `session not found`, and reads nothing. |

## Examples

```shell title="What does the current session hold that nothing uses?"
arcave gc --dry-run
```

```shell title="Another session, stored images only"
arcave gc --dry-run --session feat --kind upstream-images,image-layers
```

```shell title="Layer tags, holding back any tagged in the last hour"
arcave gc --dry-run --kind restored-layers --restored-layer-grace 1h
```
