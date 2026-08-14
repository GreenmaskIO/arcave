# arcave CLI — UX contract

**Status: pre-alpha, and most of this is not wired.** `arcave init` and `control-plane serve` do what they say; every other command reaches a stub that prints a canned response and touches nothing. See [the root README](../README.md#status-pre-alpha-and-the-cli-does-not-do-the-thing-yet) and, for the per-symbol ledger, [`CLAUDE.md`](../CLAUDE.md) › "Current state".

This document is the **UX contract**: the command tree, the flags, the shape of the output, and the exit codes. It is not design. How a savepoint is taken, why the record is the commit marker, what the backends do — that lives in `CLAUDE.md` and in the knowledge base, and is deliberately not restated here.

Verified against commit `360ad7e` (2026-08-13).

---

## Conventions

**All results are JSON on stdout. All logs are on stderr.** A caller can pipe stdout into `jq` without filtering anything out. `arcave mcp` writes nothing to stdout at all — that channel is its JSON-RPC transport.

**The one exception is `--version`**, which prints a human line. Nothing consumes it as a command result.

**Exit codes:**

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | failure — cobra has already reported the cause on stderr |
| `1` from `recommend` | **a finding, not a failure**: the JSON is still on stdout and stderr is empty |

`recommend` is the only command that reports through the exit code. `0` means it has no advice; `1` means it does. Distinguish the two cases by stderr being empty, or simply by reading the JSON.

### Field naming is PascalCase, and that is a debt

The types in `internal/model/api` carry **no `json` tags**, so Go marshals their field names verbatim:

```json
{
  "ProviderID": "p1",
  "StackName": "s1",
  "SavepointID": "28ce712d-36bd-475d-a834-6fe2c8e61531",
  "ProviderType": "linux"
}
```

Types that predate the API package — `model.Savepoint`, `model.Recommendation` — do carry tags and marshal as `snake_case`. **The two conventions are live at once.** This is unintended, and settling it is a breaking change to every consumer, so it is tracked rather than fixed quietly.

---

## Addressing

Every stack-scoped command names four things. They are flags on the **root**, inherited by the whole tree, and none is bound to the config file — no `Config` field claims those keys.

| Flag | Resolution order | Unset |
|---|---|---|
| `--project` | flag → `project.name` from the config | error |
| `--session-id` | flag → `$ARCAVE_SESSION_ID` | error naming both sources, pointing at `session create` |
| `--provider` | flag → the sole entry of `project.providers.stacks` | error; **several declared and no flag is also an error** |
| `--stack` | flag only — the config does not know it | error |

Several providers with no flag is refused rather than guessed: picking one would be a guess about where the stack lives.

Not every command needs all four. `savepoint list` and `recommend` resolve the **stack scope** — project, provider, stack — and name no session; `session create` resolves only the project.

```bash
export ARCAVE_SESSION_ID=6f8f2f0e-1a5c-4a2e-9d4b-0f1f2a3b4c5d
arcave savepoint create --provider local --stack myapp before-migration
```

## Global flags

| Flag | Default | What |
|---|---|---|
| `--config` | `.arcave/config.yaml` under the working directory | config file path |
| `--log-level` | `info` | `debug`, `info` or `warn` |
| `--log-format` | `text` | `text` or `json` |
| `--project`, `--session-id`, `--provider`, `--stack` | see above | addressing |
| `-v`, `--version` | — | version, commit and build date |

`--log-level` and `--log-format` are the only global flags bound to the config, under `log.level` and `log.format`. Environment overrides use the `ARCAVE_` prefix with dots as underscores: `ARCAVE_LOG_LEVEL=debug`.

Precedence throughout: **changed flag → environment → config file → default.**

---

## Configuration

Two files, one per binary. They do not overlap.

### `.arcave/config.yaml` — the project's, read by `arcave`

Written by `arcave init`. Only the `project` section is ever sent anywhere; the rest is local.

```yaml
project:
  name: demo
  providers:
    stacks: [local]     # provider ids, never types
    sandbox: ""         # the sandbox provider, if the project has one
control_plane:
  address: http://127.0.0.1:7654
  token: ""             # optional
log:
  level: info
  format: text
```

Neither config is versioned: there is one schema, and a file is judged by its keys. An unknown key is an error, not a value quietly ignored — a `storage:` section left over from an older layout names itself rather than being silently dropped.

A config file that exists and arcave cannot speak is **held, not raised at startup**: it must not block `arcave init --override`, which is the fix. Commands raise it through their own guard, and the message says so.

### `~/.arcave/control-plane.yaml` — the host's, read by `control-plane`

Located by `--config`, else `$ARCAVE_CONTROL_PLANE_CONFIG`, else the default path. An absent default file is fine (the service warns and serves with no providers); an absent **explicit** one is an error.

```yaml
api:
  address: 127.0.0.1:7654
providers:
  stacks:
    - name: local             # how a request addresses it
      type: compose           # selects the factory; the only implemented type
      settings:               # opaque to the control plane, read by the provider
        connection:           # how this provider reaches its daemon
          host: ""            # empty means "read the environment"
          context: ""         # mutually exclusive with host
          api_version: ""
        compose:
          wait_timeout: 120s  # health-check wait after a start
          stop_timeout: 10s   # grace before SIGKILL
          progress: log       # log | tty | quiet
        snapshot:
          backend: fullcopy   # fullcopy | cdc
        storage:
          type: directory
          directory:
            path: .arcave/savepoints
        backends:
          cdc:                            # resolved whatever snapshot.backend says
            restore_mode: incremental     # wipe | incremental
            overwrite: always             # if-changed | always
            image: restic/restic:0.19.1
            server_address: 127.0.0.1:0   # port 0 lets the kernel pick
            server_scheme: http
            container_host: host.docker.internal
log:
  level: info
  format: text
```

Everything about a runtime lives here, not in the project config: which daemon, how patient with the stack, which capture backend, where the data goes. `storage.type` is a tagged union — the tag must name a present arm.

**Everything under `settings` belongs to the provider.** The control plane carries the section verbatim: it neither defaults it, nor validates it, nor knows one key of it. `type` selects a factory, and that factory reads its own settings — so a key spelled wrong under `settings` is reported by the provider, naming what it could not read, while a key spelled wrong above it is reported by the config. An entry naming a type nothing registers is refused by the registry, not by the parser.

`container_host` is the key that gets cdc running on native Linux: `host.docker.internal` is Docker Desktop's name for the host loopback and Linux does not resolve it.

---

## Commands

### `arcave init`

```
arcave init [--name <name>] [--override]
```

Writes `.arcave/config.yaml` and **nothing else**. It applies nothing — `arcave project apply` is what sends the declaration. It opens no storage and contacts no daemon.

- `--name` — the project name; defaults to the working directory's basename, normalized (`MyApp` → `myapp`). Bound to `project.name`.
- `--override` — overwrite an existing config. Without it, an initialized project is an error.

This is the one command with no config guard: it is how a missing or unusable config gets written.

### `arcave project`

```
arcave project apply [-f|--file <path>]
arcave project list
arcave project remove <name>
```

`apply` is the only verb that creates or updates a project. It sends the config's whole `project` section; `-f` applies another file instead of the project's own, and that file must exist.

`remove` answers with nothing but an error — silence is success.

### `arcave session`

```
arcave session create [--from-session-id <uuid>]
arcave session list
arcave session status
```

A session is the lifetime everything captured under it dies with. `create` resolves only the project; the session it opens is in the response, which you then carry in `ARCAVE_SESSION_ID`. `--from-session-id` inherits from an existing session; omitted, the new one starts from scratch.

`session status` is provisional: its payload has no settled shape, and the client renders whatever the control plane answers with.

### `arcave savepoint create`

```
arcave savepoint create [name] [--name <name>] [--description <text>] [--label k=v]...
```

The name may be given as a positional argument **or** as `--name`. Supplying both is an error only when they disagree.

`--label` is repeatable and takes `key=value`; an empty key is refused. It is a `StringArray`, not a `StringSlice`, so a value may contain a comma without being split into two labels.

Labels are a general mark. A rollback stamps its own safety savepoint with `arcave-safety-snapshot`, but nothing reads a particular key back — a provider renders whatever it is given into whatever marking its own store understands.

### `arcave savepoint list`

```
arcave savepoint list [--limit N] [--offset N] [--include-safety-savepoints]
```

Addressed by provider and stack; names no session. `--include-safety-savepoints` also lists the savepoints a rollback takes of the live state — one that outlived its rollback can be rolled back to by id.

### `arcave savepoint remove`

```
arcave savepoint remove [uuid...] [--retain-recent N] [--retain-for DUR]
                        [--before-date TS] [--dry-run]
```

Two selector modes, and **they never combine**:

| Selector | Meaning |
|---|---|
| positional `uuid...` | remove exactly these |
| `--retain-recent N` | keep the newest N, remove the rest |
| `--retain-for DUR` | keep anything newer than DUR — `1w2d3h4m5s` |
| `--before-date TS` | remove anything older than an RFC3339Nano timestamp |

Mixing ids with a retention rule is `ErrRemoveSelectorConflict`. Giving neither is `ErrRemoveNoSelector`. A negative `--retain-recent` is `ErrRetainRecentNegative`. When several retention rules are combined, a savepoint survives if **any** rule keeps it.

`--dry-run` reports what would be removed and removes nothing. It takes no lock, so it never blocks on a running rollback.

A flag that was not typed is distinguished from one typed as zero: the request carries pointers, and only a **changed** flag fills one.

### `arcave rollback`

```
arcave rollback <savepoint-uuid> [--with-safety-savepoint]
```

Takes exactly one argument, and it must be a **UUID**. Resolution by name, and the `latest` keyword, are gone with the old command.

`--with-safety-savepoint` defaults to **true**: the live state is captured before anything is restored, so a failed rollback can be undone. Passing `--with-safety-savepoint=false` opts out and makes the rollback one-way.

There is no `--wait-timeout`. Timing is a property of the runtime, so it lives in the provider's `compose.wait_timeout`.

If a restore fails or the services come up unhealthy, arcave replays the safety savepoint, restarts, and returns an error. The safety savepoint is retained — with its id reported — only when the **revert itself** failed, which is the one path where it is the last copy of the pre-rollback state.

> Rollback is destructive. Bind-mounted host folders are wiped and restored like anything else.

**An interrupted rollback blocks the next mutating command.** A marker at `.arcave/inflight.json` stands from before the first destructive write until the environment is consistent again; a command that finds one is refused with a message naming the phase (`restoring`, `restore-done`, `inconsistent`) and the savepoints involved.

### `arcave recommend`

```
arcave recommend
```

Reports, as JSON, every persistent volume a service's image is known to need and the stack does not declare — state a savepoint would silently fail to capture. **Read-only**: it stops nothing, writes nothing, and takes no lock.

Exits `1` when it has anything to recommend, `0` when it does not.

```json
{
  "ProviderID": "local",
  "StackName": "myapp",
  "Recommendations": [
    {
      "service": "db",
      "image": "postgres:16",
      "volume": "/var/lib/postgresql/data",
      "operation": "add-volume",
      "hash": "9f2c…"
    }
  ]
}
```

`hash` identifies the **recommendation**, not the run, so a caller can suppress one it has already acted on. Its contract is the lowercase hex sha256 of `service + "|" + image + "|" + volume + "|" + operation`, with `image` the compose file's value verbatim, tag and all — bumping a tag is meant to produce a new hash, since the advice may no longer apply.

`operation` is `add-volume` today. It is a field of its own so a future `arcave fix` can dispatch on it without changing the wire shape.

arcave **never edits a compose file.** Applying a recommendation — deciding which findings to act on and which file to add the volume to — is the caller's job, and there is no apply command.

The catalog flags of the old command are gone: which images are known to write where is the provider's knowledge now, and the client no longer points at files to match against.

### `arcave provider list`

```
arcave provider list
```

Lists the providers registered with the control plane, by id, type and status.

### `arcave status`

Registered and **not implemented** — it returns an error. It carries no flags and no dependencies. It stays registered so the command tree is honest about what arcave intends to be; do not cite it as behaviour.

### `arcave mcp`

```
arcave mcp
```

Starts an MCP server over stdio, exposing five tools: `savepoint_create`, `savepoint_list`, `savepoint_remove`, `rollback`, `recommend`.

The addressing is resolved **once, when the server starts**, and bound to it. An agent calls tools; which project, session, provider and stack they act on is the operator's decision, made at launch. Nothing on this path writes to stdout.

The tool descriptions are the agent's only instructions, and for `recommend` they are deliberately the whole of the apply story — arcave reports, the agent decides and edits.

### `control-plane serve`

```
control-plane serve
```

Builds every registered provider, then binds and serves. Providers first, on purpose: a control plane that is listening is one that works. A provider that cannot be built fails the command and releases whatever was already open.

The address is bound explicitly rather than left to `ListenAndServe`, so an address already in use fails the command instead of a goroutine nobody is reading. `SIGINT`/`SIGTERM` trigger a graceful shutdown with a 10s bound.

The API has **one operation** today: `GET /api/v1/healthcheck`, returning a status and the build version. The document it is generated from is `openapi/control-plane.yaml`.

---

## JSON shapes

The authoritative definition of every request and response is the Go source: **`internal/model/api`**, one file per operation. Each request carries a `Validate()` that states its own requirements, and the sentinels a refused request returns are in `errors.go` of that package.

They are reproduced nowhere else, including here. A shape restated in prose drifts from the type, and the prose is what gets believed.

| Operation | Types |
|---|---|
| `project apply` / `list` / `remove` | `ProjectApplyRequest`/`Response`, `ProjectListResponse`, `ProjectRemoveRequest` |
| `session create` / `list` / `status` | `SessionCreateRequest`/`Response`, `SessionListRequest`/`Response`, `SessionStatusRequest` |
| `savepoint create` / `list` / `remove` | `SavepointCreateRequest`/`Response`, `SavepointListRequest`/`Response`, `SavepointRemoveRequest` |
| `rollback` | `SavepointRollbackRequest`/`Response`, `ItemStatus` |
| `recommend` | `RecommendRequest`/`Response`, carrying `model.Recommendation` |
| `provider list` | `ProviderListResponse` |

Two known debts in those types, named here so they are not mistaken for contract: field names marshal as PascalCase (see Conventions), and `SavepointRollbackRequest.InfrastructureName` is dead — the canon says "stack", `StackID` already names the spec, and nothing fills it.

## What is not in this document

Design belongs elsewhere and is not duplicated here:

- **How a savepoint and a rollback actually run**, and the invariants that make them crash-safe — [`CLAUDE.md`](../CLAUDE.md).
- **Why the decisions went the way they did**, the rejected alternatives, the backend and restic analysis — `.user/knowledge/arcave/proposed-impl.md`.
- **The restic REST protocol** — `internal/provider/docker/internal/backend/restic/rest/README.md`.
- **Test conventions** — [`doc/testing.md`](testing.md).
