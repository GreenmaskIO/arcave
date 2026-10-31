---
title: arcave savepoint list
description: List a Docker Compose project's committed arcave savepoints, and the hidden safety snapshots a rollback commits.
keywords: [arcave savepoint list, savepoints, safety snapshot, docker compose]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# arcave savepoint list

Prints the committed savepoints of the compose project, in the session the command works in, as a JSON array.

```text title="Synopsis"
arcave savepoint list [flags]
```

It reads the store, stops nothing, changes nothing and takes no lock, so it runs while another arcave command holds the project. Only **committed** savepoints appear: a capture killed before its record was written left data no record names, and nothing lists it or puts it back.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--include-safety-snapshots` | `false` | Also list the safety snapshots a rollback takes of the live state. |
| `--sort` | `desc` | Order by age: `desc` lists the newest first, `asc` the oldest. Any other value is refused. |

Global flags apply — see [the CLI reference](index.md#global-flags).

## Order

The array is **newest first**; `--sort asc` lists the oldest first. An id is a UUIDv7 — its leading digits are the capture's millisecond — so the order by age is the order by id.

```shell title="The newest savepoint"
arcave savepoint list | jq -r '.[0].id'
```

## Safety snapshots are hidden

A safety snapshot is an ordinary savepoint carrying the label `arcave-safety-snapshot: "true"`. The label hides it from this listing and from every scanning selector — `latest`, a name lookup, `--retain-recent`, `--retain-for`, `--before-date`. An explicit id still reaches one, in `rollback` and in `savepoint remove`, whether or not this flag was passed.

It is hidden because one normally exists only *while* a rollback runs. One that outlived its rollback is the last copy of that rollback's pre-rollback state, and this flag is how you find its id. See [the safety snapshot](../../how_it_works.md#the-safety-snapshot).

## Output

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
NAME              ID                                    CREATED           VOLUMES  LAYERS  STORED  NOT COVERED
before-migration  019e843e-1650-7000-8000-000000000000  2026-06-01 19:41  1        2       210 MB  1
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
[
  {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "before-migration",
    "description": "before the v2 schema",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    "stats": [ … ],
    "writable_layers": [ … ],
    "images": [ … ],
    "spec": { "format": "compose-yaml" },
    "not_covered": { … }
  }
]
```

</TabItem>
</Tabs>

On a terminal, one row per savepoint: what it captured, what it added to the store, and how many things it does not cover — `not_covered` and images not stored, spelled out by [`savepoint create`](savepoint_create.md#output). In the JSON each element is the whole record, exactly as `savepoint create` printed it — `stats`, `writable_layers`, `images`, `spec` and `not_covered` included. An empty store prints `No savepoints.` on a terminal, `[]` in JSON.

With `--include-safety-snapshots`, a leftover safety snapshot is listed among the others, marked `(safety)` on a terminal; in the JSON it carries its label and its generated name:

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
NAME                                                           ID                                    CREATED           VOLUMES  LAYERS  STORED   NOT COVERED
safety-snapshot-019e843e-1650-7000-8000-000000000000 (safety)  019e8491-04c8-7000-8000-000000000000  2026-06-01 21:22  1        2       40.8 MB  1
before-migration                                               019e843e-1650-7000-8000-000000000000  2026-06-01 19:41  1        2       210 MB   1
```

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout (each record trimmed)"
[
  {
    "id": "019e8491-04c8-7000-8000-000000000000",
    "name": "safety-snapshot-019e843e-1650-7000-8000-000000000000",
    "description": "",
    "labels": { "arcave-safety-snapshot": "true" },
    "created_at": "2026-06-01T21:22:37.009412Z",
    …
  },
  {
    "id": "019e843e-1650-7000-8000-000000000000",
    "name": "before-migration",
    "description": "before the v2 schema",
    "created_at": "2026-06-01T19:41:12.123456789Z",
    …
  }
]
```

</TabItem>
</Tabs>

The name carries the id of the savepoint that rollback was applying, so a leftover always identifies the run that abandoned it.

## Errors

| Condition | What happens |
|---|---|
| No `.arcave/config.yaml` | `config is not found: run 'arcave init' if not yet initialized` |
| No compose project named | `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME` |
| The Docker daemon is unreachable | The command fails while wiring itself up, before it reads the store. |
| A record that will not decode | `load savepoint 019e843e-1650-7000-8000-000000000000: parse record: …` — a corrupt commit is an error, never a silent skip. |
| No savepoints | `No savepoints.` on a terminal, `[]` in JSON; exit code `0`. |

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
arcave savepoint list | jq '[.[0] | (.stats // [])[].stored_bytes, (.writable_layers // [])[].stored_bytes, (.images // [])[].stored_bytes] | map(. // 0) | add'
```

## Next step

[`arcave rollback`](rollback.md) — return the environment to one of them.
