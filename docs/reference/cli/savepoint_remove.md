---
title: arcave savepoint remove
description: Delete arcave savepoints by id or by retention rules — keep the newest N, keep anything younger than a duration, drop everything before a timestamp — with a dry-run mode.
keywords: [arcave savepoint remove, retention, dry run, safety snapshot, docker compose]
---

# arcave savepoint remove

Deletes savepoints of the compose project, in the session the command works in, selected either by id or by retention rules.

```text title="Synopsis"
arcave savepoint remove [id...] [flags]
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--retain-recent` | `0` | Keep the newest N savepoints and delete the rest. |
| `--retain-for` | — | Keep savepoints younger than this duration, e.g. `1w2d3h4m5s`. |
| `--before-date` | — | Delete savepoints created before this RFC3339Nano timestamp. |
| `--dry-run` | `false` | Report what would be deleted, and delete nothing. |

Global flags apply — see [the CLI reference](index.md#global-flags).

`--retain-for` accepts `w` and `d` beside Go's own units (`h`, `m`, `s`, `ms`…), combined. `--before-date` does not accept a bare date: it wants a full timestamp, `2026-06-01T00:00:00Z`.

## The two selection modes

**By id** — one or more ids as positional arguments:

```shell
arcave savepoint remove 019e843e-1650-7000-8000-000000000000 019e8488-3ac8-7000-8000-000000000000
```

**By retention rule** — one or more of `--retain-recent`, `--retain-for`, `--before-date`:

```shell
arcave savepoint remove --retain-recent 5
```

The two cannot be mixed, and one of them is required. Both are refusals rather than defaults: a `remove` with nothing to go on would be a command that deletes everything by accident.

When several retention rules are combined, a savepoint survives if **any** one of them keeps it. `--retain-recent 5 --retain-for 30d` keeps the newest five *and* everything from the last thirty days; it does not intersect them.

## Which savepoints a selector reaches

| Selector | Reaches a safety snapshot? |
|---|---|
| An explicit id | **Yes** |
| `--retain-recent`, `--retain-for`, `--before-date` | No |

A scanning selector cannot delete the last copy of a pre-rollback state by accident. Removing a [safety snapshot](savepoint_list.md#safety-snapshots-are-hidden) is always deliberate, by id.

## Output

```json title="stdout"
{
  "dry_run": false,
  "removed": [
    {
      "id": "019e843e-1650-7000-8000-000000000000",
      "name": "before-migration",
      "description": "before the v2 schema",
      "created_at": "2026-06-01T19:41:12.123456789Z",
      "stats": [ … ],
      "writable_layers": [ … ],
      "images": [ … ],
      "spec": { "format": "compose-yaml" }
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `dry_run` | Whether anything was actually deleted. |
| `removed` | The savepoints deleted — or, under `--dry-run`, the ones that would be — oldest first. Each is the whole record. |

A selection that matches nothing is not an error: `removed` is `null`, and the exit code is `0`.

Deletion is permanent. It frees the savepoint's own directory; the [deduplicated image blobs](../storage_layout.md#shared-blobs) and images it shared with other savepoints, and a `cdc` repository's data, stay where they are. [`arcave gc`](gc.md) deletes the blobs and images no remaining savepoint uses; deleting the whole session gives everything back — see [`session remove`](session.md#session-remove).

## Errors

| Condition | What happens |
|---|---|
| Ids and retention rules together | `cannot combine savepoint ids with retain-recent/retain-for/before-date` |
| Neither | `specify savepoint ids, or at least one of retain-recent, retain-for or before-date` |
| A negative `--retain-recent` | `retain-recent must be zero or positive` |
| An unparseable duration | `invalid --retain-for "2 weeks": …` |
| A bare date in `--before-date` | `invalid --before-date "2026-06-01": …` — it takes RFC3339Nano. |
| An id that is not there | `savepoint(s) not found: 019e843e-1650-7000-8000-000000000000`, **and nothing is deleted**. Ids are not checked for form, so a name passed as an id is reported the same way. There are no partial deletes: one typo in a list of five removes none of them. |
| An interrupted rollback is marked | `rollback to savepoint 019e843e-1650-7000-8000-000000000000 was interrupted (restoring)`. While the marker stands every removal but `--dry-run` is refused, by id or by rule: its safety snapshot is the remedy. Recover the rollback first. |
| Another arcave run holds the lock | `another arcave command is already running in this project`. `--dry-run` takes no lock and is unaffected. |

Each removal uncommits the record first and drops the data second. A removal killed halfway therefore leaves data no record names — invisible to `savepoint list` — rather than a record pointing at data that is already half gone. What it leaves is named in the log.

## Examples

```shell title="Preview first"
arcave savepoint remove --retain-recent 5 --dry-run
```

```shell title="Keep a rolling window"
arcave savepoint remove --retain-recent 10
```

```shell title="Keep the last two weeks"
arcave savepoint remove --retain-for 2w
```

```shell title="Drop everything before a cutover"
arcave savepoint remove --before-date 2026-06-01T00:00:00Z
```

```shell title="Delete a leftover safety snapshot, once the environment is right"
arcave savepoint list --include-safety-snapshots
arcave savepoint remove 019e8491-04c8-7000-8000-000000000000
```

Until you have confirmed the environment, that snapshot is the last copy of the pre-rollback state.

## Retention rules do not know which savepoint matters

A seeded `clean` baseline is deleted by `--retain-recent 5` as soon as five newer savepoints exist. A savepoint that has to survive indefinitely is pruned around by id, or kept in a [session](session.md) of its own that the retention runs never name.

## Next step

[Storage layout](../storage_layout.md) — what a savepoint occupies, and what removing one frees.
