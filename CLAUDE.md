# CLAUDE.md

Doctrine for anyone — human or agent — changing this code: what arcave is, how it is cut, and which invariants the compiler will not catch for you. It is reference, not history: every name below exists, and every claim is checkable by `grep`, `go list` or a build. The arguments behind the decisions live in the knowledge base (see Pointers); this file states what holds, not why it beat the alternative.

Verified against the working tree of 2026-08-14, past `360ad7e`, with the provider reorganisation applied. When you change the architecture, change this file in the same commit — a doctrine that lags the code is worse than none, because it is believed.

## What arcave is today

arcave manages the lifecycle of stateful local environments. It adds **savepoints** — consistent snapshots of every stateful volume in a stack — and **rollback** to one.

A stack is driven through a **provider**. One exists (`compose`); the seam is what lets a second runtime arrive without the savepoint layer learning about it.

It ships as **two binaries**:

- `arcave` — the client. Resolves addressing, builds a request, sends it, prints JSON. Knows nothing of Docker, compose or storage. Config: the project's `.arcave/config.yaml`.
- `control-plane` — the service. Builds one provider per registered entry and serves the API. Owns the daemon connection, the capture backends and the stores. Config: `~/.arcave/control-plane.yaml`.

**The CLI is wired to a stub.** `internal/ng/client/arcave.Stub` answers every method with a canned response; nothing but `arcave init` and `control-plane serve` does anything today. Do not read a passing command as working behaviour, and do not cite a stub's output as evidence. The full ledger is under "Current state" below.

## Build and run

```bash
make build        # bin/arcave and bin/control-plane
make test         # go test ./...
make lint         # golangci-lint
make generate     # every go:generate directive; generated code is committed
```

`make build` names the two commands rather than `./...`; both build clean, and `go build ./...` does too.

## Package map

One line per package: path, role, and the seam it sits on.

### arcave's own

| Package | Role |
|---|---|
| `internal/model` | The base. The id types that carry their own gate (`SavepointID`, `SessionID`, `ProjectName`, `ResourceName`, `ProviderFamily`, `StoreNamespace`), `Project`/`Providers`, `Target`/`ProviderKind`, `Artifact`/`SkippedUnit`, `Recommendation`, `RawSettings` — a provider's own config, carried and never read — and the sentinels of `errors.go`. Runtime-neutral by construction. **Imports no arcave package** |
| `internal/model/api` | Request/response shapes of the control plane API, each with `Validate()`. Imports only `model` |
| `internal/config` | The client config: `project`, `control_plane`, `log`, plus `Unmarshal`. A data package — no SDKs |
| `internal/config/controlplane` | The host config: `api.address`, the `providers.stacks` registry — `{name, type, settings}` per entry, the settings a `model.RawSettings` it carries and never reads — and `log`. A data package |
| `internal/provider` | The seam: `StackProvider` (`Kind`/`Capture`/`Restore`/`Discard`/`Close`), `Factory`, and the three request/result types. Imports only `model` |
| `internal/ng/cli` | The adapters, **one handler type per command**: each takes the resolved `config.Config` and its command's own raw parameters, resolves the addressing, builds the request and prints the response. `MCPHandler` is the exception that prints nothing — stdout is its JSON-RPC channel — and `InitHandler` the one that holds no api client, `init` reaching nothing. Imports `model`, `model/api` and `config`, and no cobra |
| `internal/ng/client/arcave` | The control plane client. A stub today |
| `internal/httpapi` | The generated control-plane API server plus `Handlers` |
| `internal/repository/config` | `.arcave/config.yaml`, through `os` — it is bootstrap, so it cannot be read through a store configured from it |
| `internal/service/printer/json` | The production printer: indented JSON to stdout |
| `internal/utils` | Logging setup and its defaults. A leaf |
| `cmd/arcave`, `cmd/control-plane` | The two composition roots. One file per command, named `cmd_<x>.go` |

### `internal/provider/docker` — the docker runtime

The `docker` segment is the **runtime**, not the family: `model.ProviderFamily` is `stack` or `sandbox`. So `docker/stack` is the stack family on the docker runtime, whose `ProviderKind` is `compose`. `docker/doc.go` says so in the tree; do not let the two words drift.

| Package | Role |
|---|---|
| `provider/docker/stack` | The docker-compose provider: `New`, `Settings`, the backend and store wiring, `Inspector`. **The only package naming a compose service** |
| `provider/docker/stack/model` | Compose's own vocabulary: `ComposeProject`, `Service`, `ServiceVolume`, the four mount-option types, `NamedVolume`, `IntrospectionResult`, `ARCEntry` |
| `provider/docker/stack/internal/client/compose` | `Loader` parses the project; `Client` stops and starts it via `compose/v2` as a library |
| `provider/docker/stack/internal/introspect` | Reads the stack's *declared* state. Touches no daemon, decides nothing |
| `provider/docker/stack/internal/repository/arc` | The recommender catalog: `//go:embed default.yaml` merged with the user's |
| `provider/docker/sandbox` | The second family's slot. `doc.go` only |
| `provider/docker/internal/model` | What the two families share: `Volume` and how it is addressed, `VolumeType`, `SnapshotBackendKind`, `BackendSpec`, the three `Backend…Request`s, `VolumeSnapshotBackend` and its factory/registry, `OneShotSpec`, `MountSource`, the inflight marker, `ErrProjectLocked`, and `Savepoint` |
| `provider/docker/internal/client/docker` | `Engine`: `RunOneShot`, `ResolveMountSource`, `OpenVolume`, `WriteVolume`, `WipeVolume`. Speaks the Docker API; shells out to nothing |
| `provider/docker/internal/core` | The savepoint workflows. Reachable only from `stack/legacy.go`, which has no callers |
| `provider/docker/internal/backend/fullcopy` | One `data.tar` per volume per snapshot, streamed through the daemon |
| `provider/docker/internal/backend/restic/cdc` | Content-defined chunking over stock `restic` containers: one repository per volume, only changed chunks written |
| `provider/docker/internal/backend/restic/rest` | The restic REST protocol over a `storages.Storager` — echo plus generated routes. The only package of that tree naming echo |
| `provider/docker/internal/store` | The one place a path becomes a store: `NewDirectory` plus the `Scope` every arm is rooted by. Provider-agnostic already; it rises to `provider/store` when a second runtime family needs it |
| `provider/docker/internal/repository/savepoint` | One record per savepoint, through a `storages.Storager` |
| `provider/docker/internal/repository/inflight` | `.arcave/inflight.json`, through `os` — host-local state, beside the lock |
| `provider/docker/internal/lock` | The flock on `.arcave/lock` |
| `provider/docker/test`, `provider/docker/stack/test` | The cross-package integration tests, each at the level whose packages it wires |

## Layers and import invariants

`model` ← `provider` ← adapters ← `cmd/*`, with the whole docker runtime hanging off `provider` and importing nothing above itself. `config` is a leaf that only `model` and `utils` sit under.

**The runtime boundary is the compiler's, not a grep's.** Everything a provider needs and nobody else may name sits under an `internal/` segment: `provider/docker/internal/…` is unreachable from `cmd/arcave`, `ng`, or a second runtime, and `provider/docker/stack/internal/…` is unreachable from `provider/docker/sandbox`. Two of the old `go list | grep` checks retired into this; the rest are below. When you are tempted to lift something out of an `internal/` tree, move the package up a level instead of widening a caller.

**Three model packages, and `model` never means two things in one file.** Root `internal/model` is imported plain as `model`; `provider/docker/internal/model` as `dockermodel`; `provider/docker/stack/model` as `stackmodel`. The one exception is unavoidable — inside those two packages the root is `arcavemodel`, since a package cannot alias an import to its own name.

**Interfaces belong to the consumer.** Each service file declares the private interfaces it needs (`savepointRepository`, `projectLocker`, `composeIntrospector`, `composeRunner`, `inflightMarkers` in `docker/internal/core`; one `<command>API` per handler, plus `printer` and `configRepository`, in `ng/cli`). Concrete types satisfy them implicitly. The exceptions live in `dockermodel` because a factory must name them: `VolumeSnapshotBackend`, `SnapshotBackendFactory`, `SnapshotBackendRegistry` — a backend package *returns* one and a service *holds* one, so both sides would otherwise import each other.

**A sentinel error that crosses a seam lives in the model of the layer it crosses.** `ErrRemoveSelectorConflict` and `ErrInvalidSavepointID` cross from a service to an adapter, so they are arcave's; `ErrProjectLocked` and `ErrRollbackNoSelector` never leave the docker runtime, so they are `dockermodel`'s. Either way a service returns it, the other side matches with `errors.Is`, and neither imports the other.

**A provider's settings are named by that provider and by nobody else.** `settings` reaches the control plane as a `model.RawSettings` — a `map[string]any` it carries to the factory verbatim. The provider decodes it (`RawSettings.Decode`, strict), defaults it and validates it; the config knows only `name` and `type`, and an unknown `type` is refused by the registry rather than the parser. Which is why `internal/config/controlplane` shrank to the host's own three sections and `docker/internal/store` declares no config type at all. The cost is accepted, not overlooked: viper lower-cases every nested key, so a provider whose settings carry a map of arbitrary keys reads them lower-cased.

Six checks pin what the compiler cannot. All six must stay empty:

```bash
go list -f '{{join .Imports "\n"}}' ./internal/model   | grep arcave
go list -deps ./internal/provider     | grep arcave/internal/provider/docker
go list -deps ./internal/ng/cli       | grep -E 'arcave/internal/(provider|service)'
go list -f '{{join .Imports "\n"}}' ./internal/ng/cli  | grep spf13
go list -f '{{join .Imports "\n"}}' ./internal/config/controlplane | grep arcave/internal/provider
go list -f '{{join .Imports "\n"}}' ./internal/provider/docker/internal/store | grep arcave/internal/config
```

Plus one measurement, which must not grow: `go list -deps ./cmd/arcave | grep arcave` answers with `model`, `utils`, `config`, `model/api`, `ng/cli`, `ng/client/arcave`, `repository/config`, `service/printer/json` — and nothing of any runtime.

The fourth is on **direct** imports, not deps: `ng/cli` reads the config through `internal/config`, which names viper in `Unmarshal`'s signature, so the transitive dependency is there and says nothing. What must not appear is a flag, a command or a viper of `ng/cli`'s own. The last two are direct for the same reason, and they are the compiler's half of the invariant above. A check on a dead package proves nothing — if one ever goes empty because its subject died, replace it, don't keep it.

## Current state: not built / stub / broken

**This table is the single place any of it is recorded.** Other documents link here and do not restate it: repeated warnings drift, and the stalest copy is the one that gets believed. The disposition column is not decoration — without it this reads as a to-do list, and the next agent helpfully deletes the dead package or fixes the broken one.

| Path / symbol | How it looks | What it actually does | Disposition |
|---|---|---|---|
| `internal/ng/client/arcave.Stub` | the control plane client | canned responses; **every command but `init` does nothing** | keep until the real client lands — `BACKLOG.md` › "Control plane client" |
| `docker/stack.Provider.Capture/Restore/Discard` | the `provider.StackProvider` implementation | return `"not implemented"` | fix — this is the next increment. `BACKLOG.md` › "Compose provider capture" |
| `docker/stack/legacy.go` | the old `cmd/arcave` engine, parked whole | `legacyEngine`, `initServiceDeps`, `recommendServiceDeps` — **no callers** | keep — it is the working code `Capture` is to be built from. Delete only once it is |
| `docker/stack.noopLocker` | a `core.projectLocker` | warns; serialises nothing | fix with the provider — the flock was bound to a project root a control plane has none of. `BACKLOG.md` › "Provider-side locking" |
| `docker/stack.Inspector` | the reading half of the provider | declared; the concrete `introspect.Inspector` is reached directly by `core` | keep — it is the seam `arcave status` will be built on |
| `docker/internal/lock` | the project flock | imported only by `docker/test/cdc_rollback_test.go` | keep — its consumer returns with provider-side locking |
| `docker/internal/core.InitService` | the `init` workflow | dead: `ng/cli.InitHandler` replaced it, and it is the only reason the runtime imports the client's `internal/config` | delete next pass — `BACKLOG.md` › "Мелкое / drive-by" |
| `internal/provider/docker/sandbox` | the second family | `doc.go` only | keep — the slot is what makes `docker/internal` read as shared |
| `internal/service/heartbeat` | a heartbeat worker | no callers | keep — unclaimed; decide when the control plane grows a status |
| `internal/service/controlplane` | "the ledger" | `type Service struct{}` — no fields, no methods | keep — deliberately empty; every collaborator belongs to a decision not yet made |
| `openapi/control-plane.yaml` | the control plane API | one operation: `healthcheck` | grow — the API arrives with the real client |
| `cmd/arcave/cmd_status.go` | `arcave status` | `errors.New("not implemented")` | keep registered — it keeps the command tree honest about intent |
| `dockermodel.Savepoint` and its `Stats` | the savepoint record | `Stats` is the pre-provider form, live in parallel with `Artifacts`. The record sits in the runtime's model **because of that field alone** | delete `Stats` with `core`, and the record moves up to `internal/model` in the same commit |
| `arcave rollback` by name or `latest` | a selector | gone with `internal/cli`: `ng/cli` parses a uuid and nothing else | restore with the real client — resolving a name needs the savepoint list, so it is not a flag parser's job. `BACKLOG.md` › "Мелкое / drive-by" |

## Code conventions

**A client is named for its role, never "Client" and never for its transport.** `docker.Engine`, not `docker.Docker`. `introspect.Inspector`, not `introspect.DockerComposeInspector`. `compose.Client` runs the project's lifecycle — it shelled out to the CLI once and calls `compose/v2` as a library now, and the name never went stale across that move.

**Repositories speak the domain model, and a repository is CRUD, never processing.** It reads and writes one thing and owns no client. Use the test: *would this type still make sense if its only collaborator were storage?* If not, it is a service — which is why the capture backends live under `service/`, not `repository/`.

**Prefer plain `err :=` over a bespoke name.** A distinct name is worth it only when the error outlives the branch that produced it — when a revert's own failure must not overwrite the rollback failure still being reported, `revertErr` earns its name. Otherwise reuse `err` and return immediately.

**Return the primary error; log secondary ones.** A failure that happens *while* a primary error is in flight — a best-effort restart, a cleanup, a compensating write — does not change the outcome. Log it and drop it. Never fold a second cause in with `%v`, and never enumerate `(primaryErr, secondaryErr)` combinations in a switch whose cases carry no control flow, only prose. Two seams follow from it: a handler does one job and returns its own error (the moment a "restart the services" function takes a `snapErr` parameter it has stopped being a restart function), and cross-error precedence is the **orchestrator's** decision, made where both operations are visible. When behaviour genuinely must reach the caller, express it as a typed error the caller matches with `errors.Is`/`errors.As` — never as free text.

**Order declarations headline-first.** Package constants, then the types the file is about, each type's `New…` immediately under it with its methods grouped; then the exported entry point the file exists for; then private helpers **in call order**, a helper-of-a-helper under its own caller. A reader descends only as deep as they care to. `gofmt` does not enforce this.

**A default only one implementation applies belongs to that implementation.** `compose.Client`'s unexported `defaultUpWaitTimeout` is not `model.DefaultUpWaitTimeout`: nothing that cannot import its owner has to name it.

**Doc comments are terse — one to three lines.** This file's prose style is not the code's.

## Storage invariants

**The record is the commit marker.** `<savepoint-id>/savepoint.json` is a reserved key at the root of that savepoint's own directory; a backend writes beneath it and never at that key. Create writes it **last**; remove deletes it **first**. Reverse either and it still compiles — both orders are pinned by call-order tests. Uncommit-first is what makes the sweep safe: `DeleteAll("<id>")` deletes children in whatever order the store reports and could never be relied on to take the marker last.

**Storage writes are not atomic, on purpose.** `Storager` has no `Rename`, and the directory backend copies straight into the target file. arcave does not pay for a temp-file commit protocol: the record is the commit, so a savepoint that failed mid-write is invisible, dropped whole and re-created, never repaired.

**An orphan tree is refused, never swept.** `requireEmptyTree` opens a create by listing the directory the new id names and failing if anything is there. Only an interrupted run can have left it, and those objects are the last copy of whatever that run captured — so they are named, not deleted.

**No key may name the store root or anything outside it.** `store.NewDirectory(path, projectRoot, scope)` roots every store at `<path>/arcave/projects/<project>/sessions/<session>/<family>/<name>`. The family is `stack` or `sandbox` — what a provider drives, never which provider drove it: `compose` is one provider's lexicon and has no business in a shared layout. The static `arcave` segment is not configurable and is the whole safety story: point the path at `/etc` and arcave operates in `/etc/arcave`. The store library's own guard closes the other direction. In front of it stand the segment gates — `ProjectName`, `SessionID` (`global` or a canonical UUID), `ProviderFamily`, `ResourceName`, `StoreNamespace`, and `SavepointID` (digits only) — every one an **allowlist**, because a list of what a segment must not be is never provably complete.

The session is self-contained: everything under it, cdc's restic repositories included, lives inside the stack root and dies with the session — at the price of a repository that cannot be shared across sessions.

**A safety snapshot is an ordinary savepoint.** It commits a record in the same store, marked `model.SafetySnapshotLabel` (`arcave-safety-snapshot`): hidden from a scanning selector, findable by id. There is no second store and no tier — `BackendSpec` is `{Store, Volume}`, and a bare `store` is the right name again.

**`.arcave/inflight.json` is the interrupted-rollback marker.** Written before the first destructive write, cleared when the environment is consistent again, and carrying the phase whose remedy differs (`restoring`, `restore-done`, `inconsistent`). A standing marker refuses the next mutating workflow with `dockermodel.InterruptedRollbackError`.

## CLI conventions

Both binaries follow the same shape. Every file defining a cobra command is `cmd_<x>.go`, mirroring the command path. Package `main` is flat, so every symbol carries its command's prefix; the command variable is **never** named `cmd` — it would shadow the `cmd *cobra.Command` parameter in `RunE`. Registration lives in that file's `init()`, which is order-independent.

Root uses **`PersistentPreRunE`**, not `PreRunE`: cobra runs `PreRunE` only for the command that executes, so a plain one would never fire for a subcommand and every command would see a zero-valued config.

**The resolved config is package state, not a type.** `cmd_root.go` holds it, `config.go` holds the resolution that fills it. The seam is the `*viper.Viper` **parameter** those functions take, so a test passes a fresh viper per case instead of calling `viper.Reset()`. The global is `projectCfg`, never `cfg` — a bare `cfg` is shadowed silently by any local `cfg :=`.

**Only bound flags feed the config.** `viperBind` is what binds one; transient flags (`--override`, `--dry-run`) stay out. `savepoint create --name` is deliberately unbound: it would collide with `init --name` on the shared `name` key.

Guards: `requireConfig()` for a command that reads the project's config, `requireUsableConfig()` for one that only needs the endpoint. `init` carries neither — it is how a missing config is written.

**A command hands its handler values, never a request.** `RunE` runs its guard, then calls one `ng/cli` handler with `projectCfg`, `rootAddressing()` (the four addressing flags verbatim) and its own flags as typed. Everything downstream of that — precedence, parsing, the `apimodel` request, `Validate` — belongs to the handler, so `cmd/arcave` holds no knowledge of the wire and the same rule is checkable: nothing in `main` builds an `apimodel` value.

Addressing resolves in `ng/cli/addressing.go`, one chain for every command: project = flag, then `project.name`; session = flag, then `$ARCAVE_SESSION_ID`, else an error; provider = flag, then the sole `project.providers.stacks` entry (several without a flag is an error, not a guess); stack = flag only. `resolveStackScope` is the same chain without the session, for a command scoped by provider and stack alone.

**A handler reads its own values before it resolves the addressing.** A savepoint id that is not a uuid, a label that is not `key=value`: those are refused whatever the environment says, and the message the user gets names what they typed.

**`recommend` reports a finding through the exit code, not an error.** `RecommendHandler.Recommend` returns `(found bool, err error)`, `runRecommend` sets the package var `rootExitCode`, and `main` exits through it. A typed error carrying a code was rejected: cobra would print it to stderr, and a finding is not a failure.

**All output is JSON on stdout; logs go to stderr.** `arcave mcp` writes nothing to stdout at all — that is its JSON-RPC channel, which is why `MCPHandler` holds no printer while every other handler does.

## Writing tests

Full conventions: [`doc/testing.md`](doc/testing.md). The short of it, for new and modified tests:

- **Table-driven** wherever a behaviour has more than one meaningful input/output pair; one `t.Run` per case.
- **testify**: `require` for preconditions, `assert` for the things under test. Never hand-roll `if got != want { t.Errorf(...) }`.
- **Strict mocks**: `mock.Anything` only for `ctx`; an unstable argument gets `mock.MatchedBy`; every mock closes with its own `AssertExpectations(t)`; call order is pinned in the expectations themselves via `mock.InOrder`/`.NotBefore`, never a hand-rolled journal read back afterwards.
- **AAA**: Arrange, Act, Assert, in that order, separated by blank lines.
- **Naming**: `Test{Object}_{method}[_{case}]`; the case suffix only when the test is not table-driven.
- No sleeps for synchronisation; drive time with fixed `time.Date(...)` values; silence loggers with `zerolog.Nop()`.

## Pointers

- [`doc/spec.md`](doc/spec.md) — the CLI UX contract: command tree, flags, JSON shapes, exit codes.
- [`doc/testing.md`](doc/testing.md) — the test conventions in full.
- [`internal/provider/docker/internal/backend/restic/rest/README.md`](internal/provider/docker/internal/backend/restic/rest/README.md) — the restic REST protocol and its implementation. Current and thorough.
- `.user/knowledge/arcave/` — the knowledge base (gitignored, Russian). `proposed-impl.md` is this file's companion: the arguments, the rejected alternatives, the seam analysis. `BACKLOG.md` carries the dispositions from the table above. `actual/` is what bears on today, `archive/` is how decisions were reached, `trash/` is not to be read.
- **Do not restate the "Current state" table anywhere.** Link to it.
