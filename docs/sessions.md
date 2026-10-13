---
title: Sessions and branching
description: A session is an independent line of savepoints inside a project. Fork one off another and you have branched the environment — what that means, how the two lines stay independent, and how you deploy a fork.
keywords: [arcave sessions, branching, fork, savepoint, environment branching, docker compose, baseline, Open-Source, Greenmask]
---

# Sessions and branching

A **session** is an independent line of savepoints inside a project. A session starts empty or as a copy of another session, and the lines do not mix. A branch is a session, and its name is the session's name.

`arcave init` puts you in `baseline`, the default session. A command that names no session works there, which is why the diagrams below start in it. Creating a session does not switch you into it: you name the one you want with `--session` or `ARCAVE_SESSION`, by id or by name.

A fork copies the session it starts from rather than sharing data with it. That is what keeps the two lines independent — nothing one of them writes can reach the other — and it is why a fork costs the size of the session it copies.

A session and a savepoint each carry an id and a name. An id is what the thing is, and arcave assigns it. A name is what you address it by, and you choose it. Both work as selectors: `--session`, `--from` and `rollback` take either one.

## A savepoint is a state you named

Bring the project up, get it to the state you want to keep, and save it. `arcave savepoint create v1.0` records the whole environment as one savepoint in the session you are working in — `baseline` on the diagram below.

:::note
The diagrams label both, so four names appear on them:

| Label | What it is |
|---|---|
| `ses_id` | the session's identifier |
| `ses_name` | the name you gave the session |
| `sp_id` | the savepoint's identifier |
| `sp_name` | the name you gave the savepoint |

The ids are shortened to two digits so the pictures stay readable; a real one is a UUID.
:::

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

## A fork copies the line, a rollback deploys it

`arcave session create --from=baseline --name=feat/add-new-table` opens a second line beside `baseline`, holding a copy of every savepoint `baseline` holds — same ids, same names. The Docker daemon is not touched: the project keeps running whatever it ran.

To start working on a state of the new line, roll back to it there: `arcave rollback --session=feat/add-new-table -p shop v1.0`. That is an ordinary rollback, safety snapshot included. The commands that follow name the session they work in. [`arcave session`](reference/cli/session.md) has both in full.

```mermaid
%%{init: {"flowchart": {"wrappingWidth": 340}} }%%
flowchart LR
    subgraph sA["session — ses_id: 11, ses_name: baseline"]
        A1(["sp_name: v1.0<br/>sp_id: 21"]) -- "savepoint create v1.1" --> A2(["sp_name: v1.1<br/>sp_id: 22"])
    end
    sA --> F["arcave session create<br/>--from=baseline<br/>--name=feat/add-new-table"]
    F -- "copied in" --> sB
    B1 -- "rollback v1.0" --> E["the compose project<br/>running on what v1.0 recorded"]
    subgraph sB["session — ses_id: 12, ses_name: feat/add-new-table"]
        B1(["sp_name: v1.0<br/>sp_id: 21"]) ~~~ B2(["sp_name: v1.1<br/>sp_id: 22"])
        B1 -- "savepoint create step-1" --> B3(["sp_name: step-1<br/>sp_id: 23"])
    end
```

A savepoint id is unique within its session, not across sessions: the copy of `v1.0` is `21` on both lines. Every command resolves a savepoint inside the session it was given, so the two never meet.

```shell
arcave session create --from=baseline --name=feat/add-new-table
export ARCAVE_SESSION=feat/add-new-table
arcave rollback -p shop v1.0
```

## Related docs

* [`arcave session`](reference/cli/session.md) — the command, its flags and its errors.
* [Environment branching](use_cases/environment_branching.md) — the workflow a fork is for, and where the branching analogy stops.
* [How arcave works](how_it_works.md) — the whole arc between the savepoints and the Docker daemon, and what a savepoint captures.
