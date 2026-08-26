---
title: arcave session
description: Create, list and delete sessions — the independent lines of savepoints inside a project, and the way arcave expresses a branch.
keywords: [arcave session, session create, fork, branch, docker compose, savepoint]
---

# arcave session

A **session** is an independent line of savepoints inside a project. A session forked from a savepoint is what arcave calls a branch; there is no separate verb for it.

```text title="Synopsis"
arcave session create --name <name> [--from <address>] [--force]
arcave session list
arcave session delete <id|name>
```

## Naming the session a command works in

Every command works in exactly one session, and it is named per invocation.

| Where | How |
|---|---|
| Flag | `--session feat/add-new-table` |
| Environment | `export ARCAVE_SESSION=feat/add-new-table` |
| Neither | `baseline`, the default session |

Both forms take an id or a name. **Creating a session does not switch you into it** — arcave keeps no pointer to a current session, so a command that names none works in `baseline`, whichever session you created last.

## `session create`

Opens a new line of savepoints. With `--from`, the line starts from an existing savepoint: that savepoint is copied into the new session, and the environment is deployed on the state it recorded.

| Flag | Default | Description |
|---|---|---|
| `--name` | — | The session's name. What you address it by; the id is assigned either way. |
| `--from` | — | The savepoint to start from, as `<session>/<savepoint>`. A bare `<session>` names that session's latest savepoint. |
| `--force` | `false` | Deploy over a project that is already up. |

Three things to know before you run it.

**The fork deploys into the Docker daemon.** It refuses to run while the project is up unless you pass `--force`, and `--force` does not take a safety snapshot first — what you had not saved is gone. This is where a fork differs from [`rollback`](rollback.md), which always takes one.

**The commands that follow have to name the session.** `--session=feat/add-new-table`, or `ARCAVE_SESSION` in the shell. A command that names none works in `baseline`.

**A fork copies the data it starts from** rather than sharing it with the session it came from. That is what keeps the two lines independent — nothing one of them writes can reach the other — and it is why a fork costs the size of the savepoint it starts from.

The copy carries the savepoint's name over and gets an id of its own, so `--from=baseline/v1.0` leaves a savepoint named `v1.0` in the new session. Roll back to it by that name; `<session>/<savepoint>` is an address only `--from` takes, because no other command needs to look into a session that is not the one it was given.

## `session list`

The project's sessions: id, name, how many savepoints each holds, and what they occupy.

## `session delete`

Deletes a session and everything under it — its savepoints, and the backend data they name. A session is the lifetime everything below it dies with.

## Errors

| Condition | What happens |
|---|---|
| `--from` names a session that does not exist | The command names the session and stops; nothing is created. |
| `--from` names a savepoint the session does not hold | Same, naming the savepoint. |
| The project is up and `--force` was not passed | The command names the running project and stops. Bring it down, or pass `--force`. |
| The name is already taken in this project | Refused: a name is what you address a session by, so two sessions cannot share one. |
| The name is `baseline` | Refused: `baseline` is the default session and its name is reserved. |
| Another arcave run holds the lock | `another arcave command is already running in this project` |

## Examples

```shell title="Fork a line of work off a known good state"
arcave session create --from=baseline/v1.0 --name=feat/add-new-table
```

```shell title="Work in it for the rest of the shell"
export ARCAVE_SESSION=feat/add-new-table
arcave savepoint create step-1
```

```shell title="Fork from a session's latest savepoint, over a running project"
arcave session create --from=baseline --name=feat/try-partitioning --force
```

```shell title="Drop the line when the work is merged"
arcave session delete feat/add-new-table
```

## Next step

[`arcave rollback`](rollback.md) — return the environment to a savepoint of the session you are in.
