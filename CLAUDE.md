# CLAUDE.md

Doctrine for anyone — human or agent — changing this code: what arcave is, how it is cut, and which invariants the compiler will not catch for you. It is reference, not history: every name below exists, and every claim is checkable by `grep`, `go list` or a build. When you change the architecture, change this file in the same commit — a doctrine that lags the code is worse than none, because it is believed.

Verified against the working tree of 2026-08-21, the `feat/utility-mvp` branch.

## What arcave is

arcave is a CLI utility that manages **savepoints** — consistent snapshots of every stateful volume of a docker compose project — and **rollback** to one. One binary, `arcave`; the workflows stop the stack, move the volumes' bytes through the Docker API, and start the stack again. Config lives in the project's `.arcave/config.yaml`.

The platform vision — a control plane, a provider seam, a `sandbox` family — is deliberately parked: the branch `feat/restic-backend-impl` carries that work (its last two commits, `360ad7e` and `29edf2a`) and is the archive to harvest when the platform returns. This branch is the utility-first path; do not re-import platform packages from the archive piecemeal.

## Build and run

```bash
make build        # bin/arcave
make test         # go test ./...
make lint         # golangci-lint
```

`make generate-restic-api` regenerates the restic REST server from its OpenAPI document (oapi-codegen, pinned version); the generated code is committed.

## Package map

One line per package: path, role, and the seam it sits on.

| Package | Role |
|---|---|
| `internal/model` | The base. Domain types (`Volume`, `Savepoint`, `SnapshotStat`, the introspection result), the segment-gate types (`SavepointID`, `SessionID`, `ProjectName`, `StackName`), the backend seam (`BackendSpec`, `VolumeSnapshotBackend`, `SnapshotBackendFactory`, `SnapshotBackendRegistry`, the three `Backend…Request`s), the request/response types the adapters share, the inflight marker (`InflightMarker`, its phases, `InterruptedRollbackError`), and the sentinels of `errors.go`. **Imports no arcave package** |
| `internal/config` | The config value type and `Unmarshal` — version gate, unknown-key rejection, `ApplyDefaults`, `Validate`. A data package: no clients, no SDKs |
| `internal/repository/config` | `.arcave/config.yaml`, through `os` — it is bootstrap, so it cannot be read through a store configured from it |
| `internal/repository/savepoint` | One record per savepoint, through a `storages.Storager` |
| `internal/repository/inflight` | `.arcave/inflight.json` — the interrupted-rollback marker, through `os`: host-local state, beside the lock |
| `internal/repository/arc` | The recommender catalog: `//go:embed default.yaml` merged with the user's |
| `internal/storefactory` | The one place a configured path becomes a store: `New(cfg, projectRoot, project, session, stack)` returns a `storages.Storager` rooted at `<path>/arcave/projects/<project>/sessions/<session>/compose/<stack>` |
| `internal/service/core` | The business logic every adapter calls. `core.Service` (built from `core.Deps`) runs the savepoint/rollback/remove workflows; `core.InitService` runs `arcave init`; `core.RecommendService` runs `arcave recommend`; `registry.go` builds one backend per volume |
| `internal/service/backend/fullcopy` | The wholesale backend: one `data.tar` per volume per snapshot, streamed through the Docker engine. Its consumer-owned seam is `fullcopy.VolumeStreamer` |
| `internal/service/backend/restic/cdc` | Content-defined chunking over stock `restic` containers: one repository per volume, only changed chunks written. Seams: `cdc.VolumeRunner` (the engine half) and the `Dependencies` struct carrying `RepoURLBase` — a value, not a live server |
| `internal/service/backend/restic/rest` | The restic REST protocol over a `storages.Storager` — echo plus generated routes (`api/`), `rest.NewEcho`/`rest.Serve` at the top. See its `README.md`; the only package of that tree naming echo |
| `internal/service/introspectv2` | Reads the stack's *declared* state via the compose loader. Touches no daemon, decides nothing |
| `internal/service/lock` | The non-blocking flock on `.arcave/lock`, bound to its project root |
| `internal/service/printer/json` | The production printer: indented JSON to stdout |
| `internal/client/compose` | `Loader` parses the project; `Client` stops and starts it via `compose/v2` as a library; `progress.go` re-emits compose's progress as log records |
| `internal/client/docker` | `Engine`: volume I/O and the one-shot container mechanism (`RunOneShot`, `ResolveMountSource`, `OpenVolume`, `WriteVolume`, `WipeVolume`). Speaks the Docker API; shells out to nothing |
| `internal/cli` | The adapters, one handler per command, each declaring its own `<command>Service` seam and printing through the injected `printer`. `mcp.go` is the second adapter in the same package; it writes nothing to stdout — that is its JSON-RPC channel |
| `internal/utils` | Logging setup and its defaults. A leaf |
| `cmd/arcave` | The composition root. One file per command, named `cmd_<x>.go`; `dependencies.go` wires everything |
| `test/…` | Cross-package integration tests, self-skipping without a reachable docker daemon |

## Layers and import invariants

`model` ← `repository`/`client`/`storefactory` ← `service` ← `cmd/arcave`, with `cli` beside `service`: it declares its own consumer-side service interfaces and meets the services only in `cmd/arcave`, the one package that imports every layer.

**Interfaces belong to the consumer.** Each service file declares the private interfaces it needs (`savepointRepository`, `projectLocker`, `composeIntrospector`, `composeRunner`, `inflightMarkers` in `core`; one `<command>Service` per handler, plus `printer`, in `cli`). Concrete types satisfy them implicitly. The exceptions live in `model` because a factory must name them: `VolumeSnapshotBackend`, `SnapshotBackendFactory`, `SnapshotBackendRegistry` — a backend package *returns* one and `core` *holds* one, so both sides would otherwise import each other. Exported consumer-owned seams (`fullcopy.VolumeStreamer`, `cdc.VolumeRunner`) are constructor parameters, named at the constructor so godoc states the contract.

**A sentinel error that crosses a seam lives in `model`.** `ErrRemoveSelectorConflict`, `ErrRemoveNoSelector`, `ErrRetainRecentNegative`, `ErrProjectLocked`, `ErrInvalidSavepointID`, `ErrRollbackNoSelector`, and the typed `InterruptedRollbackError`: a service returns it, an adapter matches with `errors.Is`/`errors.As`, and neither imports the other.

Five checks pin what the compiler cannot. All five must stay empty:

```bash
go list -deps ./internal/service/core ./internal/service/backend/... | grep arcave/internal/client
go list -deps ./internal/cli | grep arcave/internal/service
go list -deps ./internal/cli | grep arcave/internal/client
go list -f '{{join .Imports "\n"}}' ./internal/model | grep arcave
go list -f '{{join .Imports "\n"}}' ./internal/cli | grep spf13
```

A backend reaches the daemon only through a seam it declares itself, so "knows no client" holds for the backends exactly as it does for `core`. A check on a dead package proves nothing — if one ever goes empty because its subject died, replace it, don't keep it.

## Code conventions

**A client is named for its role, never "Client" and never for its transport.** `docker.Engine`, not `docker.Docker`; `introspect.Inspector`, not `introspect.DockerComposeInspector`. `compose.Client` runs the project's lifecycle — it shelled out to the CLI once and calls `compose/v2` as a library now, and the name never went stale across that move.

**Repositories speak the domain model, and a repository is CRUD, never processing.** It reads and writes one thing and owns no client. Use the test: *would this type still make sense if its only collaborator were storage?* If not, it is a service — which is why the capture backends live under `service/`, not `repository/`.

**Prefer plain `err :=` over a bespoke name.** A distinct name is worth it only when the error outlives the branch that produced it — when a revert's own failure must not overwrite the rollback failure still being reported, `revertErr` earns its name. Otherwise reuse `err` and return immediately.

**Return the primary error; log secondary ones.** A failure that happens *while* a primary error is in flight — a best-effort restart, a cleanup, a compensating write — does not change the outcome. Log it and drop it. Never fold a second cause in with `%v`. Cross-error precedence is the **orchestrator's** decision, made where both operations are visible; behaviour that genuinely must reach the caller is a typed error matched with `errors.Is`/`errors.As`, never free text.

**Order declarations headline-first.** Package constants, then the types the file is about, each type's `New…` immediately under it with its methods grouped; then the exported entry point the file exists for; then private helpers **in call order**. `gofmt` does not enforce this.

**A default only one implementation applies belongs to that implementation.** `compose.Client`'s unexported `defaultUpWaitTimeout` is not `model.DefaultUpWaitTimeout`: nothing that cannot import its owner has to name it.

**Doc comments are terse — one to three lines.** This file's prose style is not the code's.

## Storage invariants

**One store, rooted by `storefactory.New`.** `<path>/arcave/projects/<project>/sessions/<session>/compose/<stack>` — the tenant, the lifetime, and the stack. The static `arcave` segment is not configurable and is the whole safety story: point the path at `/etc` and arcave operates in `/etc/arcave`; the store library's own key guard closes the other direction. In front of it stand the segment gates — `ProjectName`, `SessionID` (`global` or a canonical UUID), `StackName`, and `SavepointID` (digits only — Unix seconds) — every one an **allowlist**, because a list of what a segment must not be is never provably complete. `compose/` has a reserved sibling, `sandbox/`, for the second timeline when it is built.

**The session is self-contained.** Everything lives inside the stack root and dies with the session — cdc's restic repositories included, under `<stack root>/restic/<object-id>` (`cdc.RepoNamespace`), beside the savepoint directories — at the price of a repository that cannot be shared across sessions.

**The record is the commit marker.** `<savepoint-id>/savepoint.json` is a reserved key at the root of that savepoint's own directory; a backend writes beneath it (fullcopy: `<object-id>/data.tar`) and never at that key. Create writes it **last**; remove deletes it **first**. Reverse either and it still compiles — both orders are pinned by call-order tests. Uncommit-first is what makes the sweep safe: `DeleteAll("<id>")` deletes children in whatever order the store reports and could never be relied on to take the marker last.

**On error the record is zero.** `captureVolumes`/`performSavepoint` return a zero `Savepoint` on any failure: an uncommitted capture is garbage no caller may address. What a half-finished fan-out wrote is logged — the one place that knows the id — and left for a later gc; a failure path is the worst moment to delete data. This is also why there is no per-backend `Exists` pre-flight: a record naming a capture already *is* the statement that the capture completed.

**An orphan tree is refused, never swept.** `requireEmptyTree` opens a create by listing the directory the new id names and failing if anything is there. Only an interrupted run can have left it, and those objects are the last copy of whatever that run captured — so they are named, not deleted.

**Storage writes are not atomic, on purpose.** `Storager` has no `Rename`, and the directory backend copies straight into the target file. arcave does not pay for a temp-file commit protocol: the record is the commit, so a savepoint that failed mid-write is invisible, dropped whole and re-created, never repaired.

**A safety snapshot is an ordinary savepoint.** It commits a record in the same store, marked `model.SafetySnapshotLabel` (`arcave-safety-snapshot`): hidden from `savepoint list` by default (`--include-safety-snapshots` shows it), findable and rollable-back-to by id. There is no second store and no tier — `BackendSpec` is `{Store, Volume}`, and a bare `store` is the right name in `core` again.

**`.arcave/inflight.json` is the interrupted-rollback marker.** Written before the first destructive write, cleared when the environment is consistent again, and carrying the phase whose remedy differs (`restoring`, `restore-done`, `inconsistent`). A standing marker refuses the next mutating workflow with `model.InterruptedRollbackError` — except a rollback to the marker's own safety snapshot, which is the remedy and passes. The marker is cleared **before** the safety snapshot is dropped, so a failed clear never leaves a marker naming a deleted remedy.

## The rollback flow

`Rollback` holds the project flock for the whole workflow and runs: resolve the record → gate on the inflight marker → build both backend sets (the savepoint's from `record.Stats`, the live set from the compose files) ahead of the stop, so a backend that cannot be built leaves the project running → stop services → safety snapshot of the live state → write the inflight marker (`restoring`) → wipe-and-restore every volume → start services → clear the marker, drop the safety snapshot.

Any failure past the marker replays the safety snapshot through the very instances that took it. The revert runs on `context.WithoutCancel` with its own 10-minute budget (`revertBudget`): the Ctrl-C that aborted the rollback must not tear the revert meant to save the environment. A failed revert marks the phase `inconsistent` and retains the safety snapshot — it is now the last copy of the pre-rollback state.

The cdc backend's safety capture is an increment in the same per-volume repository (same host, same path — what restic picks a parent by), so the undo costs almost nothing; the label rides along as a restic tag.

## CLI conventions

Every file defining a cobra command is `cmd_<x>.go`, mirroring the command path. Package `main` is flat, so every symbol carries its command's prefix; the command variable is **never** named `cmd` — it would shadow the `cmd *cobra.Command` parameter in `RunE`. Registration lives in that file's `init()`, which is order-independent.

Root uses **`PersistentPreRunE`**, not `PreRunE`: cobra runs `PreRunE` only for the command that executes, so a plain one would never fire for a subcommand.

**The resolved config is package state, not a type.** `cmd_root.go` holds it, `config.go` holds the resolution that fills it. The seam is the `*viper.Viper` **parameter** those functions take, so a test passes a fresh viper per case instead of calling `viper.Reset()`. The global is `projectCfg`, never `cfg` — a bare `cfg` is shadowed silently by any local `cfg :=`.

**Only bound flags feed the config.** `viperBind` is what binds one; transient flags (`--override`, `--dry-run`) stay out. `savepoint create --name` is deliberately unbound: it would collide with `init --name` on the shared `name` key.

A command that touches storage or Docker wraps its body in `withDeps`, which builds `initAllDependencies` and defers its `Close`; half-finished wiring is released newest-first by the `partial` unwind. `arcave init` deliberately does not use it — it opens no storage and contacts no daemon. The compose loader is built first: the store root ends at the compose project's name, so nothing is addressable until the stack is known, and inspector and runner share that one loader.

**`recommend` reports a finding through the exit code, not an error.** The handler returns `(found bool, err error)`, `runRecommend` sets the package var `rootExitCode`, and `main` exits through it: a finding is not a failure, and cobra must not print it to stderr.

**All output is JSON on stdout; logs go to stderr.** `arcave mcp` writes nothing to stdout at all — that is its JSON-RPC channel, which is why `MCPHandler` holds no printer while every other handler does.

**`arcave status` is a registered stub, not absent.** It returns `not implemented` and keeps the command tree honest about intent; do not cite it as behaviour.

## Writing tests

Full conventions: [`doc/testing.md`](doc/testing.md). The short of it: table-driven wherever a behaviour has several input/output pairs; testify (`require` for preconditions, `assert` for the things under test); strict mocks — `mock.Anything` only for `ctx`, `mock.MatchedBy` for unstable arguments, every mock closes with `AssertExpectations(t)`, call order pinned via `mock.InOrder`/`.NotBefore`; AAA with blank lines; `Test{Object}_{method}[_{case}]`; no sleeps, fixed `time.Date(...)`, `zerolog.Nop()`.

The integration tests under `test/` need a reachable docker daemon and self-skip without one — a fast green `go test ./test/...` is a skipped run, not a passed one.

## Pointers

- [`doc/spec.md`](doc/spec.md) — the CLI UX contract: command tree, flags, JSON shapes, exit codes.
- [`doc/testing.md`](doc/testing.md) — the test conventions in full.
- [`internal/service/backend/restic/rest/README.md`](internal/service/backend/restic/rest/README.md) — the restic REST protocol and its implementation.
- `.user/knowledge/arcave/` — the knowledge base (gitignored, Russian): the arguments behind the decisions, `BACKLOG.md`, and the platform material archived with `feat/restic-backend-impl`.
