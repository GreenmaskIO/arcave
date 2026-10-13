---
title: arcave session
description: Create, list and delete sessions — the independent lines of savepoints inside a project, and the way arcave expresses a branch.
keywords: [arcave session, session create, fork, branch, docker compose, savepoint]
---

# arcave session

A **session** is an independent line of savepoints inside a project. A session forked from another is what arcave calls a branch; there is no separate verb for it.

None of the `session` commands touches the Docker daemon, and none of them takes `-p`: a session holds every compose project of the project directory.

```text title="Synopsis"
arcave session create [--name <name>] [--from <session>]
arcave session list [--all]
arcave session delete [--force] <id|name>
```

## Naming the session a command works in

Every command works in exactly one session, and it is named per invocation.

| Where | How |
|---|---|
| Flag | `--session feat/add-new-table` |
| Environment | `export ARCAVE_SESSION=feat/add-new-table` |
| Neither | `baseline`, the default session |

Both forms take an id or a name: arcave looks the value up as a name first, then as an id. **Creating a session does not switch you into it** — arcave keeps no pointer to a current session, so a command that names none works in `baseline`, whichever session you created last.

## `session create`

Opens a new line of savepoints. Without `--from` it is empty. With `--from`, it is a copy of that session: every compose project, every savepoint and the data they name. The copy keeps each savepoint's id and name, so `v1.0` in `baseline` is `v1.0` in the new session too.

| Flag | Default | Description |
|---|---|---|
| `--name` | — | The session's name. What you address it by; the id is assigned either way. |
| `--from` | — | The session to copy, by name or id. `baseline` is a session like any other. |

It prints the new session's record — the `session list` entry without the counts — with `from` naming the session it was copied from.

Four things to know before you run it.

**A fork deploys nothing.** The running compose project keeps running whatever it ran. To put a savepoint of the new session into the daemon, roll back to it there; that [`rollback`](rollback.md) takes its usual safety snapshot first.

**The commands that follow have to name the session.** `--session=feat/add-new-table`, or `ARCAVE_SESSION` in the shell. A command that names none works in `baseline`. A `savepoint create` in the new session right after the fork captures what is running now — the state of the line you came from — which is a fine way to start.

**A fork costs the size of the session it copies.** The two lines share nothing, so nothing one of them writes can reach the other. Check the source's `size_bytes` in `session list` before you copy a large one.

**The copy holds the project's lock until it is done.** Every other arcave command in the project directory, in any session, is refused with `another arcave command is already running` meanwhile. Nothing is printed while it copies; one line goes to stderr when the copy is done. A fork that fails or is interrupted creates no session and leaves what it copied in the store: `session list --all` shows it as `failed`, and `session delete <id>` gives the space back.

## `session list`

The project's sessions as a JSON array, `baseline` first and the rest oldest first: each one's id, name, `status`, when it was created, the session it was forked from, how many savepoints it holds (safety snapshots not counted), the compose projects they belong to, and `size_bytes` — what the session occupies in the store.

Without `--all` every session listed is `ready`. `--all` adds the session directories that never got their record — what a fork leaves while it copies, or after it failed. They have no name; address them by id.

| `status` | Meaning |
|---|---|
| `ready` | A session you can work in. |
| `cloning` | A fork is copying it, or died within the last 30 minutes without saying so. |
| `failed` | A fork that failed, with its `error`, or one silent for 30 minutes. `session delete <id>` removes it. |
| `incomplete` | Neither: an interrupted `session delete`, or a directory arcave did not write. `session delete <id>` removes it. |
| `unknown` | Its heartbeat could not be read, or reports an operation other than a fork still running; `error` says which. |

`size_bytes` is measured from the store, not added up from the savepoints: it counts everything under the session's directory — volume data, container layers, the image blobs its savepoints share, and data a removed savepoint left behind. That is exactly what `session delete` frees.

```json title="Output"
[
  {"id": "00000000-0000-0000-0000-000000000001", "name": "baseline", "status": "ready", "savepoint_count": 2, "compose_projects": ["shop"],
   "size_bytes": 734003200},
  {"id": "0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31", "name": "feat/add-new-table", "status": "ready",
   "created_at": "2026-10-10T12:00:00Z", "from": {"session": "00000000-0000-0000-0000-000000000001"},
   "savepoint_count": 2, "compose_projects": ["shop"], "size_bytes": 734003396}
]
```

## `session delete`

Deletes a session and everything under it — its savepoints, and the backend data they name. A session is the lifetime everything below it dies with. It prints `{"session": "<id>", "deleted": true, "freed_bytes": <n>}` — the session's `size_bytes`, measured just before it went.

**The running compose project is left alone.** Deleting the session a compose project was rolled back from does not stop it or touch its volumes; only the savepoints go.

`baseline` cannot be deleted, and neither can a session an interrupted rollback left its marker in — recover that rollback first.

A session directory without a record — `failed`, `incomplete`, `cloning` or `unknown` in `session list --all` — is deleted by its id. A `cloning` one is refused unless `--force` is given: no fork can be running in this project directory while `session delete` holds the lock, so a `cloning` tree there is one whose fork was killed; `--force` deletes it without waiting the 30 minutes for it to turn `failed`. An `unknown` one is refused unless `--force` too: arcave cannot tell that nothing is writing it.

## Errors

| Condition | What happens |
|---|---|
| `--from` names a session that does not exist | `session not found`, naming what was given; nothing is created. |
| `--from` names a name two sessions carry | Refused, listing their ids; name the source by id. |
| The name is already taken in this project | Refused: a name is what you address a session by, so two sessions cannot share one. |
| The name is another session's id | Refused: names are looked up first, so it would hide that session's id. |
| `--session` or `session delete` names no session | `session not found`, naming what was given. |
| `session delete baseline` | Refused: the default session always exists. |
| `session delete` of a `cloning` directory without `--force` | `session is still being cloned`; nothing is deleted. |
| `session delete` of an `unknown` directory without `--force` | `session tree status is unknown`, with why; nothing is deleted. |
| `session delete` of a session an interrupted rollback names | Refused, with the interrupted rollback's recovery hint. |
| Another arcave run holds the lock | `another arcave command is already running in this project` |

## Examples

```shell title="Fork a line of work off baseline"
arcave session create --from=baseline --name=feat/add-new-table
```

```shell title="Work in it for the rest of the shell, starting from a known good state"
export ARCAVE_SESSION=feat/add-new-table
arcave rollback -p shop v1.0
arcave savepoint create -p shop step-1
```

```shell title="Drop the line when the work is merged"
arcave session delete feat/add-new-table
```

## Next step

[`arcave rollback`](rollback.md) — return the environment to a savepoint of the session you are in.
