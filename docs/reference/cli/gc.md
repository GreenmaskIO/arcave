---
title: arcave gc
description: Find and delete the garbage arcave leaves in the store and in the Docker daemon.
keywords: [arcave gc, garbage collection, dry run, delete, savepoint data, deduplicated volumes, image blobs, restored layers]
---

# arcave gc

Finds what no live savepoint of a session keeps alive, and deletes it. The report covers the session's store, every compose project in it, and the Docker daemon. `--dry-run` reports without deleting anything.

```text title="Synopsis"
arcave gc [--dry-run] [--session <session>] [--kind <kind>…] [--restored-layer-grace <duration>] [flags]
```

gc deletes every kind of data it finds: savepoint directories, stored images and their layers, `cdc` snapshots, and the layer tags rollbacks left in Docker. gc asks nothing before it deletes: run `arcave gc --dry-run` first to see what will go.

gc stops nothing. It holds the project lock for its whole run, so it refuses to run while another arcave command in the project is working. It also holds the session's lock in the store, which it waits for as every command does. gc works on one session: the one `--session` names, or the current one. It looks at every compose project in that session, so it refuses `-p`. It ignores `ARCAVE_PROJECT_NAME`, which you may have exported for the savepoint commands. Only `restored-layers` and `deduplicated-volumes` need the Docker daemon: the second runs restic in it.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--dry-run` | `false` | Report the garbage without deleting it. |
| `--kind` | every kind | The kinds of data to look at: `savepoint-data`, `deduplicated-volumes`, `image-layers`, `upstream-images`, `restored-layers`. Repeat the flag, or separate kinds with commas. |
| `--restored-layer-grace` | `gc.restored_layer_grace` (`1h`) | How long ago a layer tag must have been tagged before it counts as garbage. `0` counts every unused tag. |

Global flags apply — see [the CLI reference](index.md#global-flags).

## How gc decides what is alive

A savepoint's status decides what it keeps alive. Whatever only a dead savepoint reaches is garbage. gc never judges an object by its age.

| Status | How gc tells | What it keeps alive |
|---|---|---|
| committed | The savepoint has its `savepoint.json`. | Everything it names. Safety snapshots count. |
| dead | No record. | Nothing. |

gc holds the session's lock for its whole run, and so does every command that writes a savepoint: `savepoint create`, the safety snapshot a rollback takes and `savepoint remove`. No capture runs while gc reads, so a directory with no record was left by a run that died, however young it is and whatever its `heartbeat.json` says.

## What gc looks at

gc looks for five kinds of data. A savepoint's record is the root that keeps data alive; the data is what hangs off it.

| Kind | What it is | Garbage when |
|---|---|---|
| `savepoint-data` | A savepoint's own directory: its record, its compose spec, the volumes the `fullcopy` backend streamed, and its writable layers' archives and manifests. No other savepoint shares it. | The directory has no `savepoint.json`. A failed or interrupted create leaves one, and so does a remove. |
| `deduplicated-volumes` | A snapshot in a `cdc` backend's restic repository, which shares its data across savepoints, or data in a repository that no snapshot uses. | The savepoint the snapshot's `arcave-savepoint` tag names has no record. `savepoint remove` leaves a `cdc` savepoint's snapshots in place for gc to forget. A repository with no dead snapshot is pruned when restic's prune dry run says the prune would free something (`unpruned-data`): the data an earlier prune that was cut short left behind. |
| `image-layers` | The layers and configs of stored images, shared by writable layers and upstream images alike. | No manifest still stored lists it. |
| `upstream-images` | An image a service's `image:` names, stored once for every savepoint; its layers are `image-layers`. | No savepoint names it. |
| `restored-layers` | A writable layer a rollback restored into the Docker daemon, tagged `arcave/…:sp-<id>`. | No container uses it, by reference or by image, running or stopped, and it was tagged longer ago than `--restored-layer-grace` (`1h` by default). Only layers this arcave project committed for a compose project of the session are judged. |

Each report names its `data_kind` and the `collector` that found it: how arcave looks for that kind of data in its store, such as `restic-repositories` for `deduplicated-volumes`.

A dead savepoint's manifests hold no layer, so the layers only it lists are garbage in the same run. Every other manifest still in the store keeps its layers, including the manifest of an upstream image no savepoint names. Such a layer is skipped as `listed-by-unreferenced` and becomes garbage only once that image is gone. A manifest no savepoint names holds nothing when a capture would write it again: one that is missing, lists nothing, is torn, or names another image. Any other manifest that can't be read makes `image-layers` refuse to judge that compose project, because it might list any layer. The report lists it under `unreadable`, with the savepoint that names it when one does.

:::note[Freeing an image takes two runs]
An unused upstream image and its layers are not freed in the same run. The first run finds the image under `upstream-images` and holds its layers back as `listed-by-unreferenced`; the run after the image is removed finds the layers under `image-layers`. This is on purpose: a capture that finds an image's manifest intact reuses its layers without writing them, so a layer may only go once no manifest lists it.
:::

`deduplicated-volumes` asks restic, not the store's files: it lists each repository's snapshots and judges each by the savepoint its tag names. A snapshot whose savepoint has no record is dead, whether its directory is still in the store or gone. It takes no restic lock. A snapshot with no `arcave-savepoint` tag wasn't taken by arcave. It's counted under `unattributed` and never judged. A repository restic can't list is left unjudged as `backend-unreadable`, with restic's error. The other repositories are still judged. Any unjudged object makes the run partial.

A layer tag is matched by the labels its image carries: the arcave project and compose project the layer was committed under. A tag whose image carries neither, such as one committed by an earlier arcave version, is counted under `unattributed` and never judged. A tag of another compose project of the arcave project is counted under `other_compose_projects`: a gc of the session holding that compose project judges it. Remove a tag no session holds any more by hand.

The project lock keeps out a capture in the same project directory, and the session's lock one on any host sharing the store. Neither covers the Docker daemon, which every session and store on the machine shares. A rollback tags the layers it loads before it creates the containers that use them, and takes its safety snapshot in between, so for that time the new tags are unused. A gc in the same project directory waits for the rollback's lock. A gc in another directory of the same arcave project doesn't: two checkouts sharing `project.name` and a daemon each have their own lock. That is what `gc.restored_layer_grace`, an hour by default, holds back: the tags such a rollback has just loaded. Pass `--restored-layer-grace 0` to judge every unused tag when no other checkout shares the daemon. A layer tag the daemon reports no tag time for is left unjudged as `tag-time-unknown` while a grace is set.

A session directory without `session.json`, which a failed fork leaves, belongs to no session, so gc doesn't report it. `arcave session list --all` lists it with its status, and `arcave session remove <id>` deletes it.

## How gc deletes

gc first looks with every collector, at the daemon and at every compose project, then deletes what they found: the daemon's layer tags first, then one compose project after the other. Every finding is made before anything is deleted: the `image-layers` finding is read off the very manifests deleting a tree or an image removes. Deletion runs in a fixed order:

1. `restored-layers`: each unused tag, taken off the daemon one at a time, never forced. The image goes with its last tag. A tag whose image a container was created from since gc found it is refused, even when another tag names the same image, and listed under `deletion.failed`.
2. `savepoint-data`: each dead savepoint's directory, whole.
3. `upstream-images`: each unused image's directory, its manifest first. A deletion cut short then leaves no manifest that a capture would take for the image stored; the next gc deletes what is left of the directory.
4. `image-layers`: each blob, one at a time.
5. `deduplicated-volumes`: one restic run per repository, up to `common.concurrency` repositories at once. gc clears the repository's stale locks, then runs `restic forget <snapshots> --prune`, or `restic prune` for a repository with nothing to forget. Either takes restic's own lock. A repository whose run fails fails each of its ops.

gc deletes exactly the ops the report lists, by their `target`. A target already gone counts as deleted. A target it fails to delete is listed under the report's `deletion.failed`, and gc goes on with the next one. Before each kind, in each compose project, it checks that it still holds the session's lock. A check that fails stops every deletion after it, which is counted under `not_run`. A report whose kind was aborted or failed deletes nothing. An object left unjudged doesn't keep the kind's ops from being deleted.

Because a layer an image lists is held back as `listed-by-unreferenced` until the image is gone, the layers of an image gc deletes go on the next run. Run `arcave gc` twice to free an unused image whole.

A prune that is cut short after restic has forgotten the snapshots leaves their data in the repository: the next gc finds no dead snapshot there, so it runs no prune. See [Limitations](../limitations.md).

:::caution[Don't delete on a store another machine writes to]
On a store several machines share, the session's lock can, rarely, let a capture on another machine run while gc deletes: the lock is taken by writing and reading back, with no write-if-absent. gc could then delete a blob that capture has just reused. Run `arcave gc` without `--dry-run` only while no other machine works in the session. See [Limitations](../limitations.md#gc-on-a-shared-store).
:::

gc refuses to delete while an interrupted rollback is marked in `.arcave/inflight.json`: what that rollback was writing isn't settled. Recover or finish the rollback first. `--dry-run` still runs, and warns of the marker.

## Output

stdout carries one report per collector for each compose project of the session, what each dead savepoint frees, and a total. A `restored-layers` report has no `compose_project`: it covers the daemon. Each report has a `summary` that every kind shares, and a `detail` specific to its kind. `detail.ops` lists the actions gc takes, or under `--dry-run` would take. An op's `action` is what deleting its target takes: `delete-tree` (a savepoint's directory), `delete-image` (a stored image, its manifest first), `delete-object` (a blob), `forget` (a `cdc` snapshot, or a `cdc` repository to prune) or `untag` (a restored layer's tag). A run that deletes adds each report's `deletion`: what deleting those ops did. A dry run and a report that was aborted or failed carry none; a report that found nothing carries an empty one.

```json title="stdout"
{
  "dry_run": false,
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
      },
      "deletion": {
        "deleted": [{"target": "shared/oci/blobs/sha256/4f…", "bytes": 52428800}],
        "not_run": 0
      }
    }
  ],
  "savepoints": [
    {"compose_project": "demo", "savepoint": "019e843e-1650-7000-8000-000000000002", "found": 2, "bytes": 52430912}
  ],
  "total": {
    "found": 1, "bytes": 52428800, "bytes_complete": true, "skipped": 0, "unjudged": 0, "aborted": 0, "failed": 0,
    "deleted": 1, "freed_bytes": 52428800
  }
}
```

| Field | Meaning |
|---|---|
| `deletion.deleted` | Each op deleted, or found already gone, with the `bytes` deleting it freed: `0` for one already gone. Left out when there is none. |
| `deletion.failed` | Each op gc failed to delete, with the error. `interrupted` is `true` when gc was stopping while it deleted the op, which may then be partly deleted. Left out when there is none. |
| `deletion.not_run` | How many ops gc started no deletion for: it was interrupted, or no longer held the session's lock. |
| `deletion.reclaimed` | What a deletion freed beyond its ops' own sizes: each `cdc` repository pruned, as `target`, with the `bytes` restic's prune reports, `null` when it printed none. A forgotten snapshot's own `bytes` under `deletion.deleted` is `0`. Left out when there is none. |
| `total.deleted`, `total.delete_failed`, `total.not_run` | The reports' deletions added up. Left out when zero, and always in a dry run. |
| `total.freed_bytes` | What the deletions freed, `deletion.reclaimed` included. Left out in a dry run, and when any freed size is unknown: an untagged layer's is never known. |

An op's `savepoint` names the dead savepoint whose removal alone frees the target: its tree, each blob that only its manifests list, and each `cdc` snapshot its capture took. `savepoints` adds up those ops per dead savepoint, across kinds. A stored image is never attributed: what a capture meant to reference with it is lost with the capture.

A `deduplicated-volumes` op is a `forget` of one snapshot, and its `bytes` is `null`: a snapshot shares its data, so only a prune of the whole repository can say what forgetting it frees. gc asks restic for that estimate, without changing anything, once per repository: for the dead snapshots of one that holds any, else for a prune alone, which becomes a `forget` op targeting the repository when it would free anything. `detail.estimates` lists each repository, the snapshots it would forget (none for a prune alone), and `freed_bytes`: the total restic's prune dry run reports. It is rounded the way restic prints it, to three decimals of its unit. The report's `summary.bytes` adds up the estimates. A dead savepoint's `bytes` under `savepoints` is `null` when it has `cdc` snapshots, because the estimate is per repository, not per savepoint.

`bytes` is `null` when gc can't say how much would be freed:

* an estimate restic didn't give: `freed_bytes` is `null`, and so is the report's `summary.bytes`;
* a `restored-layers` tag: the image may share layers with other images.

`total.bytes_complete` is then `false`.

A target is relative to the compose project's directory in the store. A `restored-layers` target is the tag itself.

## Errors

| Condition | What happens |
|---|---|
| A kind could not be judged, an object was left unjudged, a collector failed, or an op failed to delete or was not run | The full report is printed, then the command exits `1` with `garbage collection was partial`. |
| An interrupted rollback is marked, and `--dry-run` is not passed | `rollback to savepoint 019e843e-… was interrupted (restoring)`. Nothing is read or deleted. |
| `--restored-layer-grace` is negative | `invalid garbage collect request: negative restored layer grace -1h0m0s`. Nothing is opened first. |
| `--kind` names a kind arcave does not know | `invalid garbage collect request: unknown kind "restic"`. Nothing is opened first. |
| Another arcave command holds the project lock | The command fails at once, without reading anything. |
| No daemon answers within 10 seconds | The `restored-layers` and `deduplicated-volumes` reports are `aborted` with the ping's error, whether or not `--kind` names them. The other kinds still run, and the run is partial: the full report is printed, then the command exits `1`. |
| A `docker.*` setting is wrong | The command fails as every command does, whichever kinds run. |
| `-p/--project-name` is passed | `gc works on a session, every compose project in it: drop -p/--project-name` |
| The session was removed after gc resolved it | The command fails with `session not found`, and reads nothing. |
| gc loses the session's lock midway through `--dry-run` | The command fails with `session lock lost` and prints no report: what it read past the loss is no verdict. Run it again. |
| gc loses the session's lock midway through deleting | The report is printed with what was deleted — nothing deleted comes back — and the ops not started counted under `not_run`. Then the command exits `1` with `garbage collection stopped deleting: session lock lost`. Run it again. |

## Examples

```shell title="What does the current session hold that nothing uses?"
arcave gc --dry-run
```

```shell title="Delete it, then free the layers of the images that went"
arcave gc
arcave gc
```

```shell title="Another session, stored images only"
arcave gc --session feat --kind upstream-images,image-layers
```

```shell title="Every unused layer tag, however recent"
arcave gc --dry-run --kind restored-layers --restored-layer-grace 0
```
