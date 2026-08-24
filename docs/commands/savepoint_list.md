---
title: savepoint list
description: List a Docker Compose project's arcave savepoints, newest first, optionally including the hidden safety snapshots a rollback takes.
keywords: [arcave savepoint list, list snapshots, safety snapshot, docker compose, Open-Source, Greenmask]
---

# arcave savepoint list

Lists the project's committed savepoints, newest first.

## Usage

```text
Usage: arcave savepoint list [flags]
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--include-safety-snapshots` | `false` | Also list the safety snapshots a rollback takes of the live state. |

Global flags apply — see [Commands](index.md#global-flags).

## Behavior

Reads the committed records from the store and prints them. It does not stop the stack, does not contact the daemon for volume data, and changes nothing.

Only **committed** savepoints appear. A run killed before its record was written left data that no record names; it is invisible here, and nothing will ever restore it.

## Safety snapshots are hidden

A [safety snapshot](../concepts.md#safety-snapshot) is an ordinary savepoint carrying the `arcave-safety-snapshot` label. The label hides it from this listing and from every scanning selector — `latest`, a name lookup, `--retain-recent`, `--retain-for`, `--before-date`.

It is hidden because one normally exists only *while* a rollback is running. One that outlived its rollback is a different matter: it is that rollback's last copy of your pre-rollback state, and `--include-safety-snapshots` is how you find its id.

```shell
arcave savepoint list --include-safety-snapshots
```

An explicit id still reaches a safety snapshot in `rollback` and `remove`, whether or not you passed this flag here.

## Output

A JSON array, newest first. Empty when the project has no savepoints.

```json title="stdout"
[
  {
    "id": "1780339981",
    "name": "after-backfill",
    "description": "",
    "created_at": "2026-06-01T21:13:01.114872Z"
  },
  {
    "id": "1780335122",
    "name": "before-migration",
    "description": "Before applying migration 2026_001",
    "created_at": "2026-06-01T19:41:12.123456789Z"
  }
]
```

With `--include-safety-snapshots`, a safety snapshot carries its label and its generated name:

```json title="stdout, showing a leftover safety snapshot"
[
  {
    "id": "1780340557",
    "name": "safety-snapshot-1780335122",
    "description": "",
    "labels": { "arcave-safety-snapshot": "true" },
    "created_at": "2026-06-01T21:22:37.009412Z"
  }
]
```

The name says which savepoint that rollback was restoring, so a leftover snapshot always identifies the run that abandoned it.

:::note
The listing carries the savepoint metadata only. `stats`, `images` and `spec` are part of each record, and appear in the output of [`savepoint create`](savepoint_create.md#output) and [`rollback`](rollback.md#output).
:::

## Errors

| Condition | What happens |
|---|---|
| No `.arcave/config.yaml` | `config is not found: run 'arcave init' if not yet initialized` |
| No compose project named | `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME` |
| No savepoints exist | An empty array, exit code `0`. Not an error. |

## Examples

### List everything

```shell
arcave savepoint list
```

### Find the most recent id

```shell
arcave savepoint list | jq -r '.[0].id'
```

### Find a leftover safety snapshot after an interrupted rollback

```shell
arcave savepoint list --include-safety-snapshots \
  | jq -r '.[] | select(.labels."arcave-safety-snapshot" == "true") | "\(.id)  \(.name)"'
```

### Count savepoints older than a week

```shell
arcave savepoint list \
  | jq --arg cutoff "$(date -u -v-7d +%Y-%m-%dT%H:%M:%SZ)" '[.[] | select(.created_at < $cutoff)] | length'
```

## Related docs

* [savepoint remove](savepoint_remove.md) — prune what this lists.
* [rollback](rollback.md) — restore one of them.
* [Concepts](../concepts.md#safety-snapshot) — why some are hidden.
