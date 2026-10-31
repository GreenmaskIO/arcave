---
title: Prune savepoints with retention rules
description: Keep an arcave savepoint loop from filling the disk — preview with dry-run, keep the newest N, keep a time window, remove by name, delete leftover safety snapshots, and reclaim the space with arcave gc.
keywords: [arcave example, retention, prune snapshots, dry run, disk usage, savepoint remove, Open-Source, Greenmask]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Prune savepoints with retention rules

A loop that snapshots on every iteration will fill a disk. `savepoint remove` is how you bound it, and [`arcave gc`](../reference/cli/gc.md) is how you get the space back.

<video src={require('../assets/demo/gc.mp4').default} autoPlay loop muted playsInline controls width="100%" />

*What it shows:* three `cdc` savepoints, `savepoint rm --retain-recent 1`, then `arcave gc --dry-run` listing what would be freed — the removed savepoints' restic snapshots and image blobs — and `arcave gc` freeing it.

## 1. See what you have

```shell
arcave savepoint list
```

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
NAME       ID                                    CREATED           VOLUMES  LAYERS  STORED   NOT COVERED
attempt-7  019e84d4-d040-7000-8000-000000000000  2026-06-01 22:56  3        3       49.6 MB  -
attempt-6  019e84c7-14a0-7000-8000-000000000000  2026-06-01 22:41  3        3       49.4 MB  -
attempt-5  019e84b9-5900-7000-8000-000000000000  2026-06-01 22:26  3        3       49.3 MB  -
attempt-4  019e84ab-9d60-7000-8000-000000000000  2026-06-01 22:11  3        3       49.2 MB  -
attempt-3  019e849d-e1c0-7000-8000-000000000000  2026-06-01 21:56  3        3       49.1 MB  -
clean      019e83ff-30c0-7000-8000-000000000000  2026-06-01 18:36  3        3       317 MB   -
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
[
  { "id": "019e84d4-d040-7000-8000-000000000000", "name": "attempt-7", "description": "", "created_at": "2026-06-01T22:56:40Z" },
  { "id": "019e84c7-14a0-7000-8000-000000000000", "name": "attempt-6", "description": "", "created_at": "2026-06-01T22:41:40Z" },
  { "id": "019e84b9-5900-7000-8000-000000000000", "name": "attempt-5", "description": "", "created_at": "2026-06-01T22:26:40Z" },
  { "id": "019e84ab-9d60-7000-8000-000000000000", "name": "attempt-4", "description": "", "created_at": "2026-06-01T22:11:40Z" },
  { "id": "019e849d-e1c0-7000-8000-000000000000", "name": "attempt-3", "description": "", "created_at": "2026-06-01T21:56:40Z" },
  { "id": "019e83ff-30c0-7000-8000-000000000000", "name": "clean",     "description": "seeded baseline", "created_at": "2026-06-01T18:36:40Z" }
]
```

</TabItem>
</Tabs>

One row per savepoint. In the JSON each entry is the full savepoint record — `stats`, `writable_layers`, `images`, `spec` and the rest; the samples on this page keep only the fields that tell the savepoints apart.

The listing is newest first; `--sort asc` turns it around.

## 2. Always preview first

```shell
arcave savepoint remove --retain-recent 3 --dry-run
```

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
Would remove 3 savepoints:
  clean · 019e83ff-30c0-7000-8000-000000000000 · 2026-06-01 18:36
  attempt-3 · 019e849d-e1c0-7000-8000-000000000000 · 2026-06-01 21:56
  attempt-4 · 019e84ab-9d60-7000-8000-000000000000 · 2026-06-01 22:11
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "dry_run": true,
  "removed": [
    { "id": "019e83ff-30c0-7000-8000-000000000000", "name": "clean",     "description": "seeded baseline", "created_at": "2026-06-01T18:36:40Z" },
    { "id": "019e849d-e1c0-7000-8000-000000000000", "name": "attempt-3", "description": "", "created_at": "2026-06-01T21:56:40Z" },
    { "id": "019e84ab-9d60-7000-8000-000000000000", "name": "attempt-4", "description": "", "created_at": "2026-06-01T22:11:40Z" }
  ]
}
```

</TabItem>
</Tabs>

The savepoints are listed oldest first. `clean` is in that list. This is exactly why you dry-run first — retention rules do not know which savepoint matters.

A `savepoint remove --dry-run` takes no project lock, because it only reads.

## 3. Combine rules to spare the baseline

```shell
arcave savepoint remove --retain-recent 3 --retain-for 6h --dry-run
```

Rules are combined with **OR**: a savepoint survives if *any* rule keeps it. That does not save `clean` here — it is both outside the newest three and older than six hours — but it does show the semantics:

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
Would remove 1 savepoint:
  clean · 019e83ff-30c0-7000-8000-000000000000 · 2026-06-01 18:36
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "dry_run": true,
  "removed": [
    { "id": "019e83ff-30c0-7000-8000-000000000000", "name": "clean", "description": "seeded baseline", "created_at": "2026-06-01T18:36:40Z" }
  ]
}
```

</TabItem>
</Tabs>

`attempt-3` and `attempt-4` now survive on the time rule even though the count rule would have dropped them.

## 4. Remove by name when a savepoint is precious

```shell
arcave savepoint remove attempt-3 attempt-4
```

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stderr + stdout"
✔ savepoint.remove         1.0s
└── ✔ savepoint.drop  2/2  0.0s

Removed 2 savepoints:
  attempt-3 · 019e849d-e1c0-7000-8000-000000000000 · 2026-06-01 21:56
  attempt-4 · 019e84ab-9d60-7000-8000-000000000000 · 2026-06-01 22:11
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "dry_run": false,
  "removed": [
    { "id": "019e849d-e1c0-7000-8000-000000000000", "name": "attempt-3", "description": "", "created_at": "2026-06-01T21:56:40Z" },
    { "id": "019e84ab-9d60-7000-8000-000000000000", "name": "attempt-4", "description": "", "created_at": "2026-06-01T22:11:40Z" }
  ]
}
```

</TabItem>
</Tabs>

Ids and names mix freely. Deletion is **all-or-nothing**: one unknown id or name in a list of five is an error and removes none of them:

```shell
arcave savepoint remove attempt-4 attempt-9
```

```text title="stderr"
Error: savepoint(s) not found: attempt-9
```

`attempt-4` is still there.

## 5. What you cannot do

```shell
arcave savepoint remove attempt-4 --retain-recent 3
```

```text title="stderr"
Error: cannot combine savepoint ids or names with retain-recent/retain-for/before-date
```

```shell
arcave savepoint remove
```

```text title="stderr"
Error: specify savepoint ids or names, or at least one of retain-recent, retain-for or before-date
```

Neither is a default. A `remove` with nothing to go on would be a command that deletes everything by accident.

## 6. Retention in a scheduled job

```shell title="prune.sh"
#!/usr/bin/env bash
set -euo pipefail

export ARCAVE_PROJECT_NAME=arcave-demo

arcave --log-format json savepoint remove --retain-for 7d --retain-recent 5
arcave --log-format json gc
```

The `OR` semantics are what you want here: the newest five survive no matter how old they are, and everything from the last week survives no matter how many there are.

## 7. Deleting a leftover safety snapshot

Retention rules never see [safety snapshots](../concepts.md#safety-snapshot), and neither does a name. A rollback drops its own once it succeeds or reverts, so one is left only when a rollback died mid-flight, its revert failed, or it could not drop it — a successful rollback then names it in `safety_snapshot_kept`. Find one explicitly:

```shell
arcave savepoint list --include-safety-snapshots \
  | jq -r '.[] | select(.labels."arcave-safety-snapshot" == "true") | "\(.id)  \(.name)"'
```

```text
019e8491-04c8-7000-8000-000000000000  safety-snapshot-019e843e-1650-7000-8000-000000000000
```

```shell
arcave savepoint remove 019e8491-04c8-7000-8000-000000000000
```

While the rollback that left it is unrecovered — `.arcave/inflight.json` still stands — every removal that is not a `--dry-run` is refused, because the safety snapshot may be the way back:

```text title="stderr"
Error: rollback to savepoint 019e843e-1650-7000-8000-000000000000 was interrupted (restoring)
```

The log line before it names the rollback that recovers. Run that first; the removal works once the marker is cleared.

:::warning
Only after you are certain you do not need the pre-rollback state it holds. A safety snapshot that outlived its rollback is the last copy of that state — see [Troubleshooting](../reference/troubleshooting.md#an-interrupted-rollback).
:::

## 8. Reclaim the space

`savepoint remove` deletes a savepoint's record and its own directory. What it shared with other savepoints — the deduplicated image blobs and images, and with the `cdc` backend its restic snapshots — stays in the store until [`arcave gc`](../reference/cli/gc.md) finds no savepoint using it:

```shell
arcave gc --dry-run    # what would be freed, nothing deleted
arcave gc              # delete it
```

gc covers every compose project of the session at once, so it takes no `-p`. An unused image's layers go on the run after the image, so a second run can free more — see [gc frees space in more than one run](../reference/limitations.md#gc-frees-space-in-more-than-one-run). In a scheduled job, run `arcave gc` after the retention step.

## Reducing what a savepoint costs in the first place

Retention bounds how many savepoints you keep. The [`cdc` backend](../reference/configuration.md#snapshot) bounds what each one costs:

```yaml title=".arcave/config.yaml"
snapshot:
    backend: cdc
```

Each volume gets a restic repository that outlives any one savepoint, and a capture writes only the chunks that changed. A twenty-iteration loop stops costing twenty full copies.

Existing `fullcopy` savepoints keep working — a rollback reads the backend out of the record it restores.

## Related docs

* [savepoint remove](../reference/cli/savepoint_remove.md) — the command reference.
* [gc](../reference/cli/gc.md) — what gc deletes, and when.
* [Configuration](../reference/configuration.md#snapshot) — choosing a backend.
* [Agent iteration loops](../use_cases/agent_loops.md) — where the savepoints come from.
