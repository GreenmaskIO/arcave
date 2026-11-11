---
title: arcave status
description: What the Docker daemon runs for a compose project — the savepoint a rollback deployed, every replica's state and ports, and a rollback in progress or interrupted.
keywords: [arcave status, deployment, rollback, health, drift, docker compose]
---

# arcave status

Shows what the Docker daemon runs for a compose project: which savepoint a rollback deployed, from which session and when, whether every replica that savepoint recorded is up and healthy, the ports it publishes, and a rollback in progress or interrupted.

```text title="Synopsis"
arcave status -p <compose-project> [--session <session>] [flags]
```

`status` takes no lock, stops nothing and writes nothing, so you can run it in a loop, or beside a rollback in another terminal. It has no flags of its own; `-p`, `--session`, `--format` and `--progress` are [global](index.md#global-flags).

## Where the answer comes from

A rollback marks every container it builds, so `status` reads what is deployed — which savepoint, from which session, when — off the Docker daemon alone: it answers even after the savepoint or its session is removed. A container recreated outside arcave — `docker compose up` of your own file, say — is no longer part of the rollback, and `status` warns of it; once every container was recreated, the title says `not deployed by arcave`.

The store only adds to that: whether the deployed savepoint still exists, and, while it does, its description, what it restored and what it left out. A store that cannot be read — unreachable, or opened with the wrong `encryption.key` — leaves the daemon's part whole: the result is printed and the command exits `1`.

`--session` names the session your commands work in, as everywhere. A `--session` that names no session fails the command, as it fails every other: the typo is yours to fix, not something `status` could not read. A current session (`arcave session set`) that was removed since only leaves the store's part out, and the command exits `1`. When the deployed savepoint is another session's, a warning says so. When another checkout of the same compose project, under another `project.name`, deployed it, a warning names that arcave project, and its savepoint is not looked up.

`status` reads the containers alone — their labels, state, health and ports — and nothing they mount or run on, so a volume or an image the daemon cannot answer for does not fail it.

## The state

One word sums the compose project up — the first that applies:

| `state` | When |
|---|---|
| `rollback-pending` | `.arcave/inflight.json` names this compose project: a rollback is running, or was interrupted. `status` cannot tell the two apart. The marker is host-local: from another machine an interrupted rollback reads `absent` or `degraded` instead — see [Limitations](../limitations.md). |
| `absent` | No container. |
| `stopped` | No container runs, one at least exited or was never started (a completed job aside), and no replica is missing, paused, restarting, dead or being removed. |
| `degraded` | A recorded replica is missing, exited with a code other than `0`, restarting, paused, created and never started, dead, being removed, or unhealthy. |
| `starting` | Nothing is wrong, but a healthcheck is still starting. |
| `running` | Every replica runs — healthy where it has a healthcheck —, completed its job, or exited `0`. |

A replica is `completed` when it exited `0` and another service waits on it with `depends_on: {condition: service_completed_successfully}`: a migration, say. Any other container that exited `0` is `exited`: `status` cannot tell a job no service waits on from a database stopped by hand, which exits `0` too. Such a replica does not make the compose project `degraded`; the table shows `exited 0`, and a `replica-exited` warning names it while something else still runs. Once every container is stopped, `compose-project-stopped` says so once, and a replica that exited `0` adds no warning of its own. An exit code other than `0` is a failure, whatever the service.

An unhealthy compose project is not an error: `status` exits `0` whatever the state, and `1` only when it could not read something.

## Examples

After a rollback:

```text
myapp · running · savepoint before-migration (019e843e-1650-7000-8000-000000000000)
  session    baseline
  deployed   2026-11-07 14:02 (3h 12m ago)
  savepoint  taken 2026-11-06 19:41 (18h 33m ago) · restored 3 volumes, 2 layers
             the schema before 0042

SERVICE  REPLICAS  STATE      UP      PORTS
db       1/1       healthy    3h 11m  127.0.0.1:5432→5432/tcp
web      2/2       running    3h 11m  0.0.0.0:8080→80/tcp
migrate  1/1       completed  —
```

`restored` and the description come from the savepoint's record: with the savepoint removed, or the store unreadable, the line says when it was taken alone.

Something failed, something rebuilt outside arcave:

```text
myapp · degraded · savepoint before-migration (019e843e-1650-7000-8000-000000000000)
  session    baseline
  deployed   2026-11-07 14:02 (3h 12m ago) · 1 container replaced since
  ...

Warnings (2)
  ! worker-1 exited with code 1 (out of memory) 3h 09m ago [replica-exited]
  ! web-2 was recreated outside arcave 34m ago (compose replaced myapp-web-2): the writable layer the rollback restored is gone; volumes are untouched [replica-recreated]
```

A rollback interrupted, or still running elsewhere:

```text
myapp · rollback in progress or interrupted · target 019e843e-1650-7000-8000-000000000000
  phase      restoring, since 2026-11-07 17:11 (3m ago)
  safety     01a188c9-0000-7000-8000-000000000002

Warnings (1)
  ! a rollback to 019e843e-1650-7000-8000-000000000000 is in progress or was interrupted while restoring the volumes [rollback-pending]
```

Nothing running:

```text
myapp · not running
  session    baseline
```

[`arcave savepoint list`](savepoint_list.md) shows what you can roll back to.

## What the warnings say

A warning states what is so, and ends with its code in brackets; the JSON carries the code as `code`. The code is stable, the wording is not: a script or an agent acts on the code. What to do about each is below, and on [the troubleshooting page](../troubleshooting.md#what-status-warns-of).

| Code | What happened | What to do |
|---|---|---|
| `rollback-pending` | The inflight marker stands: a rollback is running, or was interrupted. | Once no arcave command is running, recover as [the troubleshooting page](../troubleshooting.md#an-interrupted-rollback) says; `rollback_in_flight.recover` is the savepoint a rollback to recovers it. A rollback started while the other is still at work is refused, so trying is safe. |
| `store-unreadable` | The store could not be read; the result shows the daemon alone and the command exits `1`. | Check `storage` and `encryption.key`. |
| `session-unresolved` | The session could not be resolved: the current session (`.arcave/current_session`) names a session removed since, or the sessions could not be listed. The message names the `--session` given, else the current session. | For a removed session, `arcave session unset`, or `arcave session set <session>`; for a listing that failed, as `store-unreadable`. |
| `inflight-marker-unreadable` | `.arcave/inflight.json` does not parse. | See [the troubleshooting page](../troubleshooting.md#an-interrupted-rollback). |
| `deployment-labels-unreadable` | What arcave marked a container with does not parse: edited by hand, or written by another arcave. It counts as deployed by none. | — |
| `deployed-savepoint-gone` | The deployed savepoint, or its whole session, was removed since — by `savepoint remove`, `session remove`, or a gc retiring a safety snapshot: no rollback can return to it. | `arcave savepoint create` keeps what runs now. |
| `other-arcave-project` | The deployed savepoint is another arcave project's: another checkout of this compose project, with another `project.name`. Its savepoint is not looked up. | Run `status` from that checkout. |
| `other-session` | The deployed savepoint is another session's than the one your commands work in. | `arcave session set <that session>` to work in it. |
| `safety-snapshot-deployed` | A rollback's undo runs: you rolled back to it, or a failed rollback put it back. | `arcave savepoint create` keeps what runs now. |
| `volume-not-restored`, `layer-not-restored` | A label or the config left that out of the deployed savepoint, as its record says: a volume kept what it held, an anonymous volume came back empty, a writable layer started fresh. Not reported once the record is gone. | — |
| `replica-missing` | A replica the deployed savepoint recorded is gone. | A rollback to the deployed savepoint rebuilds it, and puts the savepoint's volumes back: `savepoint create` first keeps today's data. |
| `replica-exited` | The container exited. With a code other than `0` the warning is severe, and says out of memory, killed or terminated where the daemon tells. An exit `0` is named only while another container still runs: with every container stopped, `compose-project-stopped` covers it. A process with no SIGTERM handler exits `143` (terminated) or `137` (killed) at a `docker compose stop`, and is named even then. | `docker logs <container>`. A replica that exited `0` was stopped by hand or finished its job. |
| `replica-dead`, `replica-removing`, `replica-restarting`, `replica-created` | The container is not doing its work. | `docker logs <container>`; `docker start <container>` for one never started. |
| `replica-paused` | Paused, by hand most likely. | `docker unpause <container>`. |
| `replica-restarted` | It runs, but restarted since it was created. | `docker logs <container>`. |
| `replica-unhealthy` | Its healthcheck fails. | `docker inspect --format '{{json .State.Health}}' <container>`. |
| `replica-recreated` | Something rebuilt the container after the rollback. Its volumes are as they were; a writable layer the rollback put back is gone, which the warning says while the savepoint's record is there to tell. | `arcave savepoint create` keeps it; a rollback to the deployed savepoint puts it back. |
| `replica-added` | A replica or a service the deployed savepoint did not record. | — |
| `compose-project-stopped` | No container runs. | A `savepoint create` or a rollback stops them while it runs; otherwise `docker compose -p <project> start`. |

## Output

As JSON, one object; [the output reference](../output.md#status) gives its shape. Under `--progress json` the result is the one line `status` prints: it reports no progress.

## Exit codes

| Code | When |
|---|---|
| `0` | The status is whole, whatever the state of the compose project. |
| `1` | The store, the current session or the inflight marker could not be read (the result is printed first); or the daemon could not be read, `--session` names no session, or `-p` is missing. |
| `130` | Interrupted. |

## Next step

[`arcave rollback`](rollback.md) — put a savepoint back.
