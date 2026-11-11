---
title: arcave session
description: Create, list, remove and switch sessions — the independent lines of savepoints inside a project, and the way arcave expresses a branch.
keywords: [arcave session, session create, fork, branch, docker compose, savepoint]
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# arcave session

A **session** is an independent line of savepoints inside a project. A session forked from another is what arcave calls a branch; there is no separate verb for it.

<video src={require('../../assets/demo/sessions.mp4').default} autoPlay loop muted playsInline controls width="100%" />

*What it shows:* `session create --from baseline` copies the baseline into `feature-x`; with `feature-x` current, a dropped table and its savepoint stay in `feature-x`; `session list` shows both lines; back in `baseline`, `rollback seed` brings the table back.

None of the `session` commands touches the Docker daemon, and none of them takes `-p`: a session holds every compose project of the project directory.

```text title="Synopsis"
arcave session create [--name <name>] [--from <session>]
arcave session list [--all] [--sort desc|asc]
arcave session show [<id|name>|current|latest]
arcave session remove [--force] <id|name>
arcave session set <id|name>
arcave session unset
arcave session current
```

## Naming the session a command works in

Every command works in exactly one session. The first of these that is set names it:

| Where | How | `session current` says |
|---|---|---|
| Flag | `--session feat/add-new-table` | `current (--session)` |
| Environment | `export ARCAVE_SESSION=feat/add-new-table` | `current (ARCAVE_SESSION)` |
| Current session | `arcave session set feat/add-new-table`, kept in `.arcave/current_session` | `current (set by session set)` |
| None of them | `baseline`, the default session | `current (default session)` |

Each form takes an id or a name: arcave looks the value up as a name first, then as an id. The flag and the variable **override** the current session, and that is what the output points out, so a forgotten `ARCAVE_SESSION` shows up the first time you look: `session list` adds `Current session overridden by ARCAVE_SESSION; without it: feat/add-new-table.` below its table, `session show` adds `(ARCAVE_SESSION)` to its title, and `session current` always names the source, with `; without it: …` on an override. An override that names the very session it would hide — or one whose hidden session cannot be told, the pointer broken — gets no footer in `session list`, and `session current` drops the tail. In JSON it is `current_source`: `{"source": "flag" | "env" | "pointer" | "default", "overrides": {"id": …, "name": …}}`, `overrides` only on an override that hides another session.

Two words name a session by its role instead: `current`, the session a command works in when it names none, and `latest`, the newest session. They are accepted only where a session is read or copied — `session show` and `session create --from` — and refused in `--session` and `ARCAVE_SESSION`. No session may be named either. **Creating a session does not switch you into it** — run `session set` to make it the current one. [`session show`](#session-show) lists what a session holds, every compose project at once.

The current session is local to the host: `.arcave/current_session` sits beside `.arcave/config.yaml` and is not in the store, so another machine reading the same store keeps its own. If the session it names has been deleted, commands that name no session are refused rather than quietly falling back to `baseline`:

```text
the current session no longer exists: <id>, named by .arcave/current_session; run 'arcave session unset' or 'arcave session set <session>'
```

## `session create`

Opens a new line of savepoints. Without `--from` it is empty. With `--from`, it is a copy of that session: every compose project, every savepoint and the data they name. The copy keeps each savepoint's id and name, so `v1.0` in `baseline` is `v1.0` in the new session too.

| Flag | Default | Description |
|---|---|---|
| `--name` | — | The session's name. What you address it by; the id is assigned either way. |
| `--from` | — | The session to copy, by name or id; `current` for the session you work in, `latest` for the newest. `baseline` is a session like any other. |

It prints the new session's record — the `session list` entry without the counts — with `from` naming the session it was copied from.

Four things to know before you run it.

**A fork deploys nothing.** The running compose project keeps running whatever it ran. To put a savepoint of the new session into the daemon, roll back to it there; that [`rollback`](rollback.md) takes its usual safety snapshot first.

**The commands that follow have to name the session.** `arcave session set feat/add-new-table`, `--session=feat/add-new-table`, or `ARCAVE_SESSION` in the shell. Otherwise they keep working in the session they worked in before. A `savepoint create` in the new session right after the fork captures what is running now — the state of the line you came from — which is a fine way to start.

**A fork costs the size of the session it copies.** The two lines share nothing, so nothing one of them writes can reach the other. Check the source's `size_bytes` in `session list` before you copy a large one.

**The copy holds the project's lock until it is done.** Meanwhile every arcave command in the project directory that changes something — `savepoint create`, `rollback`, `savepoint remove`, the other `session` commands that write — is refused with `another arcave command is already running in this project`, in any session. `savepoint list`, `session list`, `session current` and `savepoint remove --dry-run` take no lock and keep working. Nothing is printed while it copies; one line goes to stderr when the copy is done. A fork that fails or is interrupted creates no session and leaves what it copied in the store: `session list --all` shows it as `failed`, and `session remove <id>` gives the space back.

## `session list`

The project's sessions, newest first — `baseline`, created by no one, comes last — or oldest first with `--sort asc`: each one's id, name, `status`, `current_source` on the current one — the session a command naming none works in, or the one `--session` names —, when it was created, the session it was forked from, how many savepoints it holds — `savepoint_count`, safety snapshots counted apart in `safety_snapshot_count` and, on a terminal, as `(+1 safety)` —, the compose projects they belong to, and `size_bytes` — what the session occupies in the store. On a terminal they are a table, the current session marked `*`; in a script, a JSON array.

Without `--all` every session listed is `ready`. `--all` adds the session directories that never got their record — what a fork leaves while it copies, or after it failed. They have no name; address them by id.

| `status` | Meaning |
|---|---|
| `ready` | A session you can work in. |
| `cloning` | A fork is copying it, or died within the last 30 minutes without saying so. |
| `failed` | A fork that failed, with its `error`, or one silent for 30 minutes. `session remove <id>` removes it. |
| `incomplete` | Neither: an interrupted `session remove`, or a directory arcave did not write. `session remove <id>` removes it. |
| `unknown` | Its heartbeat could not be read, or reports an operation other than a fork still running; `error` says which. |

`size_bytes` is measured from the store, not added up from the savepoints: it counts everything under the session's directory — volume data, container layers, the image blobs its savepoints share, and data a removed savepoint left behind. That is exactly what `session remove` frees.

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
  NAME                ID                                    CREATED           SAVEPOINTS  SIZE    COMPOSE PROJECTS
* feat/add-new-table  019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31  2026-10-10 12:00  3           292 MB  shop
  baseline            00000000-0000-0000-0000-000000000001  -                 2           251 MB  shop
```

With `ARCAVE_SESSION=baseline` set over that, the star moves and the listing says what it hides:

```text title="stdout"
  NAME                ID                                    CREATED           SAVEPOINTS  SIZE    COMPOSE PROJECTS
  feat/add-new-table  019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31  2026-10-10 12:00  3           292 MB  shop
* baseline            00000000-0000-0000-0000-000000000001  -                 2           251 MB  shop

Current session overridden by ARCAVE_SESSION; without it: feat/add-new-table.
```

`STATUS` is a column only when a row is not `ready` — under `--all`, when it finds a session directory without a record. A current-session pointer naming a deleted session stars no row; a warning on stderr says so, with the way out.

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
[
  {"id": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "name": "feat/add-new-table",
   "created_at": "2026-10-10T12:00:00Z", "from": {"session": "00000000-0000-0000-0000-000000000001"},
   "status": "ready", "current_source": {"source": "pointer"}, "savepoint_count": 3, "safety_snapshot_count": 0, "compose_projects": ["shop"], "size_bytes": 291585156},
  {"id": "00000000-0000-0000-0000-000000000001", "name": "baseline",
   "status": "ready", "savepoint_count": 2, "safety_snapshot_count": 0, "compose_projects": ["shop"], "size_bytes": 250689663}
]
```

</TabItem>
</Tabs>

## `session show`

One session and its committed savepoints, grouped by compose project — every compose project of the session at once, so it takes no `-p`. The session is the one the argument names — by id or name, `latest` for the newest; without one, or with `current`, the session a command works in ([above](#naming-the-session-a-command-works-in)), its title naming the flag or the variable when one of them overrides the current session, and a line naming the session it hides. The argument outranks `--session`, which still marks the current session; one naming no session marks none, with a warning on stderr, as in `session list`.

Compose projects come by name, and each one's savepoints newest first, in the columns of [`savepoint list`](savepoint_list.md), safety snapshots among them, each named by the rollback it was taken before and marked `(safety)`. It reads the store alone — no daemon, no lock — and writes nothing.

<Tabs groupId="output">
<TabItem value="terminal" label="Terminal" default>

```text title="stdout"
Session feat/add-new-table · 019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31 · current
  Created    2026-10-10 12:00 (2h 05m ago)
  From       00000000-0000-0000-0000-000000000001
             baseline
  Savepoints 3 (+1 safety)

blog
  NAME  ID                                    CREATED           VOLUMES  LAYERS  STORED   NOT COVERED
  init  019a0c30-1b2c-7d3e-8f40-5a6b7c8d9e01  2026-10-10 12:10  1        0       48.0 MB  -

shop
  NAME                              ID                                    CREATED           VOLUMES  LAYERS  STORED   NOT COVERED
  before rollback to seed (safety)  019a0c4b-4e5f-7061-b273-8d9e0f1a2b34  2026-10-10 13:20  2        1       1.25 GB  -
  before-migration                  019a0c3a-2c3d-7e4f-9051-6b7c8d9e0f12  2026-10-10 12:55  2        1       1.20 GB  -
  seed                              019a0c31-3d4e-7f50-a162-7c8d9e0f1a23  2026-10-10 12:15  2        1       1.10 GB  -
```

`Savepoints` counts the session's savepoints, its safety snapshots apart. An empty session prints `No savepoints in this session yet.` below its header.

</TabItem>
<TabItem value="json" label="JSON">

```json title="stdout"
{
  "session": {"id": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "name": "feat/add-new-table",
              "created_at": "2026-10-10T12:00:00Z", "from": {"session": "00000000-0000-0000-0000-000000000001"}},
  "current_source": {"source": "pointer"},
  "from": {"id": "00000000-0000-0000-0000-000000000001", "name": "baseline"},
  "savepoint_count": 3,
  "safety_snapshot_count": 1,
  "compose_projects": [
    {"compose_project": "blog", "savepoints": [ … ]},
    {"compose_project": "shop", "savepoints": [ … ]}
  ],
  "checked_at": "2026-10-10T14:05:00Z"
}
```

</TabItem>
</Tabs>

`from` is the session this one was forked from, by id and by the name it carries now — left out for a session forked from none; `name` is `""` for a source without a name or since removed, and the text output then prints its id alone, else the name below the id. `current_source` means what it means in `session list`. Each savepoint is the whole record [`savepoint list`](savepoint_list.md) prints. `compose_projects` is `[]` for an empty session, never `null`.

## `session remove`

Removes a session and everything under it — its savepoints, and the backend data they name. A session is the lifetime everything below it dies with. It prints `{"session": "<id>", "removed": true, "freed_bytes": <n>}` — the session's `size_bytes`, measured just before it went.

**The running compose project is left alone.** Deleting the session a compose project was rolled back from does not stop it or touch its volumes; only the savepoints go.

`baseline` cannot be deleted: `--force` only empties it, and it stays the default session. A session an interrupted rollback left its marker in cannot be deleted at all — recover that rollback first.

The current session is refused unless `--force` is given; with it, the session is deleted and `.arcave/current_session` is cleared, so commands that name no session work in `baseline` again.

A session directory without a record — `failed`, `incomplete`, `cloning` or `unknown` in `session list --all` — is removed by its id. A `cloning` one is refused unless `--force` is given: no fork can be running in this project directory while `session remove` holds the lock, so a `cloning` tree there is one whose fork was killed; `--force` deletes it without waiting the 30 minutes for it to turn `failed`. An `unknown` one is refused unless `--force` too: arcave cannot tell that nothing is writing it.

## `session set`

Makes a session the current one: every command given no `--session` and no `ARCAVE_SESSION` works in it from then on, and so does an [`arcave mcp`](mcp.md) server started without `--session`. It writes `.arcave/current_session` and prints the session's record.

```shell
arcave session set feat/add-new-table
```

```json title="Output"
{"id": "019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31", "name": "feat/add-new-table", "created_at": "2026-10-10T12:00:00Z",
 "from": {"session": "00000000-0000-0000-0000-000000000001"}}
```

On a terminal: `Session feat/add-new-table · 019a0c2f-4a6d-7e4b-9a2c-1d5e8b7a4c31 · from 00000000-0000-0000-0000-000000000001`.

## `session unset`

Clears the current session, so commands that name none work in `baseline` again. It prints `baseline`'s record, `{"id": "00000000-0000-0000-0000-000000000001", "name": "baseline"}`. It is also the way out when the current session has been deleted.

## `session current`

Prints the record of the session a command would work in right now — the one `--session` or `ARCAVE_SESSION` names, else the current session, else `baseline` — and which of them named it, with what an override hides: `current (ARCAVE_SESSION; without it: feat/add-new-table)` as text, `current_source` in JSON. It changes nothing and takes no lock.

```shell
arcave session current
```

## Errors

| Condition | What happens |
|---|---|
| `--from` names a session that does not exist | `session not found`, naming what was given; nothing is created. |
| `--from` names a name two sessions carry | Refused, listing their ids; name the source by id. |
| The name is already taken in this project | Refused: a name is what you address a session by, so two sessions cannot share one. |
| The name is another session's id | Refused: names are looked up first, so it would hide that session's id. |
| `--session`, `session set` or `session remove` names no session | `session not found`, naming what was given. |
| `--session`, `ARCAVE_SESSION`, `session set` or `session remove` is `current` or `latest` | Refused: the words name a session to show or copy — `session show`'s argument, `session create --from` — never the session a command works in. |
| `--name` is `current` or `latest` | Refused: a session named so would hide the word. |
| The current session has been deleted | `the current session no longer exists`, with how to get out: `arcave session unset` or `arcave session set <session>`. |
| `.arcave/current_session` holds no session id, or cannot be read | `the current-session pointer cannot be read`, with the same way out. |
| `session remove baseline` without `--force` | Refused: the default session always exists. `--force` empties it — every savepoint it holds goes — and it stays the default session. |
| `session remove` of a `cloning` directory without `--force` | `session is still being cloned`; nothing is deleted. |
| `session remove` of an `unknown` directory without `--force` | `session tree status is unknown`, with why; nothing is deleted. |
| `session remove` of the current session without `--force` | `session is the current session`, naming it; switch away first or force it. |
| `session remove` of a session an interrupted rollback names | Refused, with the interrupted rollback's recovery hint. |
| Another arcave run holds the lock | `another arcave command is already running in this project` |

## Examples

```shell title="Fork a line of work off baseline"
arcave session create --from=baseline --name=feat/add-new-table
```

```shell title="Work in it from now on, starting from a known good state"
arcave session set feat/add-new-table
arcave rollback -p shop v1.0
arcave savepoint create -p shop step-1
```

```shell title="Drop the line when the work is merged"
arcave session unset
arcave session remove feat/add-new-table
```

## Next step

[`arcave rollback`](rollback.md) — return the environment to a savepoint of the session you are in.
