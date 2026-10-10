---
title: arcave savepoint list
description: List a Docker Compose project's committed arcave savepoints, and the hidden safety snapshots a rollback commits.
keywords: [arcave savepoint list, savepoints, safety snapshot, docker compose]
---

# arcave savepoint list

Prints the project's committed savepoints as a JSON array.

```text title="Synopsis"
arcave savepoint list [flags]
```

It reads the store, stops nothing and changes nothing. Only **committed** savepoints appear: a capture killed before its record was written left data no record names, and nothing lists it or puts it back.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--include-safety-snapshots` | `false` | Also list the safety snapshots a rollback takes of the live state. |

Global flags apply — see [the CLI reference](index.md#global-flags).

## Order

arcave does not sort the array. The store lists the savepoint directories by name, and an id is a UUIDv7 — its leading digits are the capture's millisecond — so what comes back is **oldest first**.

```shell title="The newest savepoint"
arcave savepoint list | jq -r 'max_by(.created_at).id'
```

## Safety snapshots are hidden

A safety snapshot is an ordinary savepoint carrying the label `arcave-safety-snapshot: "true"`. The label hides it from this listing and from every scanning selector — `latest`, a name lookup, `--retain-recent`, `--retain-for`, `--before-date`. An explicit id still reaches one, in `rollback` and in `savepoint remove`, whether or not this flag was passed.

It is hidden because one normally exists only *while* a rollback runs. One that outlived its rollback is the last copy of that rollback's pre-rollback state, and this flag is how you find its id. See [the safety snapshot](../../how_it_works.md#the-safety-snapshot).

## Output

```json title="stdout"
[
  {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "before-migration",
    "description": "before the v2 schema",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    "stats": [ … ],
    "images": [ … ],
    "spec": { "format": "compose-yaml" }
  }
]
```

Each element is the whole record, exactly as [`savepoint create`](savepoint_create.md#output) printed it — `stats`, `images` and `spec` included. An empty store prints `[]`.

With `--include-safety-snapshots`, a leftover safety snapshot carries its label and its generated name:

```json title="stdout"
[
  {
    "id": "019e8491-04c8-7000-8000-000000000000",
    "name": "safety-snapshot-019e843e-1650-7000-8000-000000000000",
    "description": "",
    "labels": { "arcave-safety-snapshot": "true" },
    "created_at": "2026-06-01T21:22:37.009412Z"
  }
]
```

The name carries the id of the savepoint that rollback was applying, so a leftover always identifies the run that abandoned it.

## Errors

| Condition | What happens |
|---|---|
| No `.arcave/config.yaml` | `config is not found: run 'arcave init' if not yet initialized` |
| No compose project named | `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME` |
| The Docker daemon is unreachable | The command fails while wiring itself up, before it reads the store. |
| A record that will not decode | `parse savepoint record 019e843e-1650-7000-8000-000000000000: …` — a corrupt commit is an error, never a silent skip. |
| No savepoints | `[]`, exit code `0`. |

## Examples

```shell title="List everything"
arcave savepoint list
```

```shell title="Ids and names, one per line"
arcave savepoint list | jq -r '.[] | "\(.id)  \(.name)"'
```

```shell title="Find a leftover safety snapshot"
arcave savepoint list --include-safety-snapshots \
  | jq -r '.[] | select(.labels."arcave-safety-snapshot" == "true") | "\(.id)  \(.name)"'
```

```shell title="What the last savepoint cost the store"
arcave savepoint list | jq '[.[-1].images[].stored_bytes] | add'
```

## Next step

[`arcave rollback`](rollback.md) — return the environment to one of them.
