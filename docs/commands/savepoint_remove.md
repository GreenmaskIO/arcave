---
title: savepoint remove
description: Delete arcave savepoints by id, or by retention rules — keep the newest N, keep anything newer than a duration, or delete everything before a date — with a dry-run mode.
keywords: [arcave savepoint remove, retention, prune snapshots, dry run, docker compose, Open-Source, Greenmask]
---

# arcave savepoint remove

Deletes savepoints and the snapshot data they hold, selected either **by id** or **by retention rules**.

## Usage

```text
Usage: arcave savepoint remove [id...] [flags]
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--retain-recent` | `0` | Keep the newest N savepoints and delete the rest. |
| `--retain-for` | — | Keep savepoints newer than this duration. Example: `1w2d3h4m5s`. |
| `--before-date` | — | Delete savepoints created before this RFC3339Nano timestamp. |
| `--dry-run` | `false` | Report what would be deleted without deleting anything. |

Global flags apply — see [Commands](index.md#global-flags).

## The two selection modes

**By id** — one or more ids as positional arguments:

```shell
arcave savepoint remove 1780335122
arcave savepoint remove 1780335122 1780339981
```

**By retention rule** — one or more of `--retain-recent`, `--retain-for`, `--before-date`:

```shell
arcave savepoint remove --retain-recent 5
arcave savepoint remove --retain-for 1w2d
arcave savepoint remove --before-date 2026-06-01T00:00:00Z
```

The two modes **cannot be mixed**, and at least one selector is required. Both are refusals, not defaults: a `remove` with nothing to go on would otherwise be a command that deletes everything by accident.

:::warning
When several retention rules are combined, a savepoint survives if **any** one of them keeps it. `--retain-recent 5 --retain-for 30d` keeps the newest five *and* everything from the last thirty days — it does not intersect them.
:::

## Behavior

1. Validates the selection (see the errors below).
2. For a real removal, claims the project lock **before** listing, so the set it selects is the set it deletes. A `--dry-run` takes no lock — it only reads.
3. Refuses if an [interrupted rollback](../concepts.md#inflight-marker) is marked. While a marker stands, its safety snapshot is the remedy, and no selector may delete it.
4. Deletes each selected savepoint: `savepoint.json` **first**, then the data beneath it.

Uncommitting first is what makes the sweep safe. A removal killed halfway leaves data that no record names — invisible to `savepoint list`, and never restorable — rather than a record pointing at data that is already half gone.

## Which savepoints a selector can see

| Selector | Reaches a safety snapshot? |
|---|---|
| An explicit id | **Yes** |
| `--retain-recent`, `--retain-for`, `--before-date` | No |

Scanning selectors skip [safety snapshots](../concepts.md#safety-snapshot) entirely, so a retention sweep can never delete the last copy of someone's pre-rollback state by accident. Removing one is always deliberate, by id.

## Output

```json title="stdout"
{
  "dry_run": false,
  "removed": [
    {
      "id": "1780335122",
      "name": "before-migration",
      "description": "Before applying migration 2026_001",
      "created_at": "2026-06-01T19:41:12.123456789Z"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `dry_run` | Whether anything was actually deleted. |
| `removed` | The savepoints deleted — or, under `--dry-run`, the ones that would be. |

A selection that matches nothing is not an error: `removed` is `[]` and the exit code is `0`.

## Errors

| Condition | What happens |
|---|---|
| Ids and retention rules together | `cannot combine savepoint ids with retain-recent/retain-for/before-date` |
| No selector at all | `specify savepoint ids, or at least one of retain-recent, retain-for or before-date` — `remove` never defaults to "everything". |
| `--retain-recent` negative | `retain-recent must be zero or positive` |
| An unknown id | **Error, and nothing is deleted.** There are no partial deletes: a typo in a list of five ids removes none of them. |
| No `.arcave/config.yaml` | `config is not found: run 'arcave init' if not yet initialized` |
| An interrupted rollback is marked | Refused until it is resolved. |

## Examples

### Preview before deleting

```shell
arcave savepoint remove --retain-recent 5 --dry-run
```

Always worth doing first. `--dry-run` takes no lock and changes nothing.

### Keep a rolling window

```shell
arcave savepoint remove --retain-recent 10
```

### Keep the last two weeks

```shell
arcave savepoint remove --retain-for 2w
```

The duration accepts `w`, `d`, `h`, `m` and `s`, combined: `1w2d3h4m5s`.

### Drop everything before a cutover

```shell
arcave savepoint remove --before-date 2026-06-01T00:00:00Z
```

### Delete specific savepoints

```shell
arcave savepoint remove 1780335122 1780339981
```

### Clean up after an agent session

```shell
arcave savepoint remove --retain-recent 3
```

### Delete a leftover safety snapshot

```shell
arcave savepoint list --include-safety-snapshots        # find the id
arcave savepoint remove 1780340557                      # remove it by id
```

:::warning
Do this only once you are sure you do not need the pre-rollback state it holds. A safety snapshot that outlived its rollback is the last copy of that state.
:::

## A note on precious savepoints

Retention rules do not know which savepoint matters. A seeded `clean` baseline will be deleted by `--retain-recent 5` once five newer savepoints exist. If a savepoint must survive indefinitely, prune by id rather than by rule, or give it a store of its own via [`storage.directory.path`](../configuration.md#storage-section).

## Related docs

* [savepoint list](savepoint_list.md) — find the ids.
* [Environment branching](../use_cases/environment_branching.md) — pruning branch points.
* [Concepts](../concepts.md#safety-snapshot) — what a safety snapshot is.
