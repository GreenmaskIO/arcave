---
title: Sessions and branching
description: A session is an independent line of savepoints inside a project. Fork one off a savepoint and you have branched the environment — what that means, how the two lines stay independent, and how you address a savepoint across them.
keywords: [arcave sessions, branching, fork, savepoint, environment branching, docker compose, baseline, Open-Source, Greenmask]
---

# Sessions and branching

A **session** is an independent line of savepoints inside a project. A session starts empty or from a savepoint of another session, and the lines do not mix. A branch is a session, and its name is the session's name.

`arcave init` puts you in `baseline`, the default session. A command that names no session works there, which is why the diagrams below start in it. Creating a session does not switch you into it: you name the one you want with `--session` or `ARCAVE_SESSION`, by id or by name.

A fork copies the data it starts from rather than sharing it with the session it came from. That is what keeps the two lines independent — nothing one of them writes can reach the other — and it is why a fork costs the size of the savepoint it starts from.

## You, arcave and the daemon

You are between two things: the savepoints arcave keeps, and the live state your Docker daemon is running. Every arcave command moves state across that gap. `savepoint create` reads the live state into a savepoint. `rollback` writes a savepoint back into the daemon. `session create --from` does both: it copies a savepoint into a new session, then deploys that state into the daemon.

```mermaid
sequenceDiagram
    participant S as arcave (savepoints)
    participant U as user
    participant D as docker daemon (live state)

    U->>D: docker compose up, seed the data
    Note over D: the project runs on real data

    U->>S: arcave savepoint create v1.0
    S->>D: stop the services
    D-->>S: dump volumes, container filesystems, compose spec
    Note over S: session baseline, savepoint v1.0
    S->>D: start the services

    U->>S: arcave session create --from=baseline/v1.0 --name=feat/add-new-table
    Note over S: session feat/add-new-table, savepoint v1.0 copied in
    S->>D: deploy what savepoint v1.0 recorded
    Note over D: the project runs on v1.0

    U->>D: write code, run a migration, let an agent work
    Note over D: the state diverges
    U->>S: arcave savepoint create step-1
    Note over S: savepoint step-1

    U->>S: arcave rollback step-1
    S->>D: restore volumes, layers and spec
    Note over D: back to what savepoint step-1 recorded
```

## Ids and names

A session and a savepoint each carry an id and a name. The diagrams below show both, so four labels appear on them:

| Label | What it is |
|---|---|
| `ses_id` | the session's identifier |
| `ses_name` | the name you gave the session |
| `sp_id` | the savepoint's identifier |
| `sp_name` | the name you gave the savepoint |

An id is what the thing is, and arcave assigns it. A name is what you address it by, and you choose it. Both work as selectors: `rollback` and `--from` take either one. The ids on the diagrams are shortened to two digits so the pictures stay readable; a real one is a UUID.

## A savepoint is a state you named

Bring the project up, get it to the state you want to keep, and save it. `arcave savepoint create v1.0` records the whole environment as one savepoint in the session you are working in — `baseline` on the diagram below.

```mermaid
%%{init: {"flowchart": {"wrappingWidth": 340}} }%%
flowchart LR
    U["docker compose up<br/>the project runs"] --> D["seed the data<br/>the project needs"]
    D --> C["arcave savepoint create v1.0"]
    C --> P(["sp_name: v1.0<br/>sp_id: 21"])
    subgraph s0["session — ses_id: 11, ses_name: baseline"]
        P
    end
```

## A fork starts a second line and deploys it

`arcave session create --from=baseline/v1.0 --name=feat/add-new-table` opens a second line beside `baseline`. The point that line starts from is a savepoint of its own: it carries the same `sp_name` — `v1.0`.

The fork deploys that state into the daemon, and the commands that follow name the session they work in. [`arcave session`](reference/cli/session.md) has both in full.

```mermaid
%%{init: {"flowchart": {"wrappingWidth": 340}} }%%
flowchart LR
    subgraph sA["session — ses_id: 11, ses_name: baseline"]
        A1(["sp_name: v1.0<br/>sp_id: 21"]) -- "savepoint create v1.1" --> A2(["sp_name: v1.1<br/>sp_id: 22"])
    end
    A1 --> F["arcave session create<br/>--from=baseline/v1.0<br/>--name=feat/add-new-table"]
    F -- "copied in" --> B1
    F -- "deployed" --> E["the compose project<br/>running on what v1.0 recorded"]
    subgraph sB["session — ses_id: 12, ses_name: feat/add-new-table"]
        B1(["sp_name: v1.0<br/>sp_id: 23"]) -- "savepoint create step-1" --> B2(["sp_name: step-1<br/>sp_id: 24"]) -- "savepoint create step-2" --> B3(["sp_name: step-2<br/>sp_id: 25"])
    end
```

## Addressing across sessions

One form reaches across sessions, and only one: `--from`. `baseline/v1.0` names the savepoint `v1.0` in the session `baseline`, and `--from=baseline` names that session's latest. Every other command resolves a name inside the session it was given, because it does not need more: the fork copies the point it started from into the new session, so `v1.0` is there to roll back to by its own name.

```shell
arcave session create --from=baseline/v1.0 --name=feat/add-new-table
```

## Related docs

* [`arcave session`](reference/cli/session.md) — the command, its flags and its errors.
* [Environment branching](use_cases/environment_branching.md) — the workflow a fork is for, and where the branching analogy stops.
* [How arcave works](how_it_works.md) — what a savepoint captures and what a rollback restores.
