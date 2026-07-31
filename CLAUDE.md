# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What arcave Is

arcave is a local environment lifecycle manager for stateful Docker Compose applications. It introduces **savepoints** — consistent snapshots of all registered stateful service volumes — so developers and AI agents can safely iterate on schema migrations, data transformations, and application changes without manually recreating environments.

The two core workflows are:
- **Create savepoint** → stop services → copy volume data → restart services → wait for health checks.
- **Rollback** → stop services → delete volume data → copy savepoint data back → restart services → wait for health checks.

arcave has two adapters over the same service layer: the CLI (`arcave savepoint create`, `arcave rollback`)
and `arcave mcp`, an MCP server over stdio exposing those operations as native tools for AI agents
(`savepoint_create`, `savepoint_list`, `savepoint_remove`, `rollback`, `recommend`). Both are shipped, so
where this file argues a decision was made "so `arcave mcp` gets it too", it is describing a real second
caller. `arcave status` is the one command that still returns "not implemented".

The `recommend` tool is where the two adapters deliberately stop being symmetric: arcave reports what a
compose project is missing and never edits it. Applying a recommendation — choosing which findings to
act on, which file to add the volume to — is the *agent's* job, driven by the instructions in the tool's
own description. There is no apply tool, and no code path in arcave writes a compose file.

## Commands

```bash
make build            # Compile to bin/arcave
make test             # Run all tests
make test-verbose     # Run tests with -v
make lint             # Run golangci-lint
go test ./internal/service/core/... # Run tests for a specific package
go run ./cmd/arcave -- <args>           # Run without building
```

## Architecture

Four layers — each only imports the layers to its left:

```
model  ←  repository   ←  service  ←┐
       ←  client       ↗            ├─  cmd/arcave
       ←  storefactory ↗            │
                           cli   ←──┘
config    (leaf) ← repository, service, storefactory, cli, cmd/arcave
utils     (leaf) ← cmd/arcave
```

`cli` is drawn beside `service`, not above it: it declares its own consumer-side service interfaces
and imports **no** `internal/service/*` package. `cmd/arcave` is the one place the two meet — it is the
composition root, and the only package that imports every layer.

`storages.Storager` (the external `github.com/greenmaskio/storages`) is the object-CRUD seam every
byte of capture data and savepoint metadata passes through. arcave speaks that library's interface
directly and owns no storage implementation of its own: `internal/storefactory` is the one place a
configured path becomes a store, and every constructor there already gates its keys against escaping
the root.

`internal/service/` holds no Go files of its own — it is a directory of packages, each a collaborator
some consumer declares a seam for:

- `core/` — the business logic every adapter calls, plus the `SnapshotBackendRegistry` implementation.
- `backend/` — one package per snapshot backend (`tarcopy/`, `restic/cdc/`), each an implementation
  of `model.VolumeSnapshotBackend` behind a `model.SnapshotBackendFactory`. `restic/` is a *family*
  rather than a backend: the cdc backend, the REST protocol it stores through (`restic/rest/`), the
  request edge above it (`restic/edge/`) and the server that carries them all live under the one
  namespace, because they are ends of one thing.
- `introspectv2/` — the compose introspector, `lock/` — the project flock, `printer/json/` — the
  production `model.Printer`.

Adding a package here only pays off under the same test: a seam with more than one implementation
worth building, or a cut in the import graph.

| Package | Role |
|---|---|
| `internal/model/` | Shared data types, one per file, each file named for its type: domain types (`Volume` and its three addressing arms, `SkippedVolume`, `Savepoint`, `SnapshotStat`, the introspection result), the two id types that carry their own gate (`SavepointID.Validate`, `SessionID.Validate`), the backend seam (`BackendSpec`, `BackendTier`, `VolumeSnapshotBackend`, `SnapshotBackendFactory`, `SnapshotBackendRegistry`, the four `Backend…Request` types), the request types (`CreateSavepointRequest`, `RollbackRequest`, `RemoveRequest`), the JSON response envelopes (`RollbackResponse`, `RemoveResponse`) the adapters marshal, the recommend types (`ARCEntry`, `Recommendation`, whose `Hash` and `Operation` fields state their contracts in doc comments while the rules that fill them stay with the one place that produces them in `service/core`), and the sentinel errors of `errors.go` |
| `internal/repository/` | **CRUD only.** `config/` writes `.arcave/config.yaml` through `os`; `savepoint/` reads/writes one record per savepoint through a `storages.Storager`. No repository owns a client or streams data |
| `internal/repository/arc/` | `arc.Repository` — reads the Arcave Recommender Config `arcave recommend` matches images against: `default.yaml`, compiled in via `//go:embed`, merged with the optional `--recommender-file`. It reads through `os` for the same reason `repository/config` does — the catalog is user input naming what to look for, not data of the savepoint store, and half of it is not on disk at all. The yaml schema is the private `entry` struct; the type crossing the seam is `model.ARCEntry` |
| `internal/storefactory/` | `storefactory.New(cfg, projectRoot, session)` and `NewShared(cfg, projectRoot)` — the one place a configured path becomes a store: it resolves the `config.Storage` union, appends the static `arcave` namespace and the session segment, and returns a `storages.Storager` already rooted at `<path>/arcave/<session>` — or at `<path>/arcave/shared`, for the data a backend keeps across sessions. The namespace is the safety story for destructive operations — everything under the root is arcave's own data, wherever the user points the path — and the library's own key guard closes the other direction |
| `internal/service/lock/` | `lock.Locker` — a non-blocking flock on `.arcave/lock`, bound to its project root. It is the implementation of the `projectLocker` seam `core` declares |
| `internal/service/backend/tarcopy/` | `tarcopy.Factory`/`Snapshotter` — the wholesale backend: one `data.tar` per volume per snapshot, streamed through the Docker engine and stored verbatim. It holds a `MountStreamer` *and* a `Storager` and runs the whole copy/restore pipeline, so it is processing, not CRUD; that is why it lives under `service/` and not `repository/` |
| `internal/service/backend/restic/cdc/` | `cdc.Factory`/`Snapshotter` — the content-defined-chunking backend, over stock `restic` containers: one repository per volume in the shared store, a tagged restic snapshot per capture, and only the changed chunks written. Half of it never starts a container — arcave owns the store the repository lives in, so `repo_admin.go` answers "initialised?", clears orphaned locks and drops a snapshot as object operations. `runner.go` holds the command plan and the `--json` parsing; `factory.go` the two seams (`ContainerRunner`, `RepoPublisher`) and the `RepoURLBase` value, all collaborators it does not own. Selected by `snapshot.backend: cdc` — the *methodology* is the user's word, and restic underneath it is arcave's business, which is why the package keeps the name the config uses |
| `internal/service/backend/restic/rest/` | The restic REST protocol over a `storages.Storager`: `rest.NewMux()` serves many repositories, routed by the first path segment — the prefix a backend registers under and withdraws when its operation ends. Three sub-packages behind it, cut along the seams that were already there: `handler/` (the protocol — `handler.New(store)` serves one repository; `handler.go` wires the seam and the route table, `grammar.go` decides which paths exist, `routes.go` is the ten route bodies), `repo/` (the objects — `repo.Repository`, its two sentinels, and the lazy `io.ReadSeekCloser` a ranged GET is served through) and `logging/` (one logger per request, both ends — a plain `func(http.Handler) http.Handler`, run by `edge/` above the mux, which is how this tree stays free of the framework) |
| `internal/service/backend/restic/edge/` | `edge.New(ctx, next)` — the cross-cutting layer every request crosses before `next` routes it, and the point a future integration hangs off. It owns the one `*echo.Echo` in arcave: the middleware chain, the plain-text error handler, and the bridge that keeps echo's own loggers off stdout. It routes nothing — two catch-all registrations, `/` and `/*` — and returns an `http.Handler`, so `restic.HTTPServer` above it and `rest/` below it both stay as they were |
| `internal/service/backend/restic/` | `restic.HTTPServer` — the server the containers reach the edge through, on `net/http` alone: `New(ctx, handler, opts)` returns it already listening on a host port, as `backends.cdc.data_plane` says. `RepositoryURLBase()` renders the `rest:` URL minus its path — the **value** the DI root hands down, so no layer below holds the server. It imports no arcave package at all — which is why the Echo chain is `edge/`'s and not its — and sits at the root of the restic family as the one thing `cdc/`, `edge/` and `rest/` are joined by |
| `internal/service/introspectv2/` | `introspect.Inspector` — reads the project's *declared* state: loads the compose project and reports volumes at two levels (project-level named volumes, service-level mounts) plus each service's image. Touches no daemon and decides nothing — what a savepoint captures is `core`'s call |
| `internal/service/core/` | Business logic; embeds repositories and clients; owns all orchestration. `core.Service` runs the savepoint/rollback workflows; `core.InitService` runs `arcave init`; `core.RecommendService` runs `arcave recommend`; `core.SnapshotBackendRegistry` (`registry.go`) builds one backend per volume |
| `internal/config/` | The config value type (`config.go`) and `Unmarshal` (`unmarshal.go`) — the single home of the config *policy*: version gate, unknown-key rejection, `ApplyDefaults`, `Validate`. It takes the loaded `*viper.Viper` and decodes it: the `mapstructure` tags on `Config` *are* the schema viper fills, so the decode belongs with the type. A **data package**: it reaches for no client and no SDK, so `arcave init` never drags a cloud SDK into its import graph |
| `internal/client/compose/` | `Loader` parses the project via `compose-go`; `Client` stops and starts services (`compose/v2` as a library); `progress.go` re-emits compose's progress events as arcave log records. The loader is its own type because `introspect.Inspector` loads the same project the runner acts on — one loader, so both resolve the same file set, `.env` and project name |
| `internal/client/docker/` | Acts on the *running* stack. `Engine` moves volume bytes in/out of service containers (Docker SDK); arcave shells out to nothing, so it needs no docker CLI or compose plugin installed. `oneshot.go` holds the throwaway-container *mechanism* (`runOneShotContainer`) the wipe policy is built on, exported as `RunOneShot` for the backends that drive their own containers. `ResolveMountSource` and `WipeMount` are the halves of `WriteMount` those backends need on their own: what to bind to reach a mount, and how to clear one |
| `internal/cli/` | One handler per command (`ConfigInitHandler`, `SavepointCreateHandler`, …); each calls a service method and renders the result through the injected `model.Printer`. `errors.go` holds the cross-handler `storeError`, which rephrases a store sentinel as a remedy. Knows nothing of cobra or viper. `mcp.go` is the second adapter in the same package — `MCPHandler`, the tool schemas, and the descriptions that are the agent's only instructions; it declares its own `mcpService` seam and writes nothing to stdout, which is its JSON-RPC channel |
| `internal/service/printer/json/` | The production `model.Printer`: indented JSON to stdout |
| `internal/utils/` | Logging setup and the log-level/format defaults `cmd/arcave` uses for its global flags. A leaf |
| `cmd/arcave/` | The composition root. One file per command — the cobra definition, its flags, and its `RunE`. Resolves config via viper and builds per-command dependencies |

`internal/service/core`, every `internal/service/backend/*` package, and `internal/cli` import **no**
`internal/client/*` package, and `internal/cli` imports no `internal/service/*` package either.
Anything they need from a package below them that carries no behaviour is data, and lives in `model`:
`model.IntrospectionResult` (what `introspect.Inspector.Introspect` returns) and the sentinel errors
of `model/errors.go`. Four checks pin this, and all four must stay empty:

```bash
go list -deps ./internal/service/core ./internal/service/backend/... | grep arcave/internal/client
go list -deps ./internal/cli | grep arcave/internal/service
go list -deps ./internal/cli | grep arcave/internal/client
go list -f '{{join .Imports "\n"}}' ./internal/model | grep arcave   # model is the base
```

A backend reaches the daemon only through a seam it declares itself (`tarcopy.MountStreamer`), so
"knows no client" holds for the backends exactly as it does for `core`.

**A sentinel error that crosses a seam lives in `model`.** When an adapter matches a service's error
with `errors.Is`, the error is part of the contract between them — and a contract that lives in the
implementation drags the implementation into every consumer's import graph. `ErrRemoveSelectorConflict`,
`ErrRemoveNoSelector` and `ErrRetainRecentNegative` are therefore `model` vars: `core` returns them,
`cli.removeUsageError` matches them, and neither one imports the other. The message names the domain
condition; the adapter rephrases it in terms of the flags the user typed. They keep their domain names
rather than a `ErrSavepointService…` prefix — the package qualifier already namespaces them, and naming
them for a service would re-couple `model` to the very layer it is meant to sit below.

**A default that only one implementation applies belongs to that implementation.** `compose.Client`'s
unexported `defaultUpWaitTimeout` is the bound `Up` substitutes when it is handed neither a request
timeout nor `Options`; it exists because that method waits on health checks
(`api.StartOptions{Wait: true, WaitTimeout: …}`), not because the domain says a savepoint has a
timeout. It is not `model.DefaultUpWaitTimeout`: `core` never reads it (it forwards `req.WaitTimeout`
verbatim), and what a *user* sees and overrides is `config.DefaultWaitTimeout`. A constant reaches
`model` only when a layer that *cannot* import its owner has to name it.

### The `cmd/arcave` command layer

Every file defining a cobra command is named `cmd_<x>.go`, mirroring the command path
(`cmd_savepoint_create.go` → `arcave savepoint create`). The prefix sorts all command definitions
together and keeps them apart from the plumbing (`main.go`, `config.go`, `dependencies.go`).

Package `main` is flat, so every symbol carries its command's prefix. For a command in `cmd_<x>.go`:

```go
var (
    xFlagFoo string           // flags: package vars, prefixed
    xCmd     = &cobra.Command{Use: "...", RunE: runX}
)

func runX(cmd *cobra.Command, args []string) error { ... }

func init() {
    xCmd.Flags().StringVar(&xFlagFoo, "foo", "", "...")
    parentCmd.AddCommand(xCmd)   // registration lives with the definition
}
```

- The command variable is **never** named `cmd` — it would shadow the `cmd *cobra.Command` parameter
  in `RunE`. Use `<x>Cmd`.
- Registration in `init()` is order-independent: Go initialises all package vars (`rootCmd`,
  `savepointCmd`) before any `init()` runs, so file order never matters.
- `rootCmd` uses **`PersistentPreRunE`**, not `PreRunE`: cobra runs `PreRunE` only for the command
  that executes, so a plain `PreRunE` on root would never fire for a subcommand and every command
  would see a zero-valued `cfg`.
- `initRoot` (cmd_root.go) resolves the effective config (flag > env > file > default) and sets up
  the context logger. Every command that reads an existing project opens its `RunE` with
  `requireInitialized()`; `init` is the sole exception, since it creates the config the others need.
- `viperBind` binds a flag to a config key. **Only bound flags feed the config**, which keeps
  transient flags (`--override`, `--dry-run`) out of it. `savepoint create --name` is deliberately
  unbound: it would collide with `arcave init --name` on the shared `name` key.
- **The resolved config is package state, not a type.** `cmd_root.go` holds the five variables every
  command reads — `projectRoot`, `projectCfg`, `configFound`, `configErr` (a config file that exists
  but arcave cannot speak, held for `requireInitialized` to raise), and `rootConfigFile` (the `--config`
  target) — and `config.go` holds the resolution that fills them: `setupViper`, `resolveConfig`,
  `viperBind`. There is no `app` struct: it would have one instance, grant no polymorphism, and
  bundle the *result* of resolution with its *mechanism*. The mechanism's seam is the
  `*viper.Viper` **parameter** each of those three functions takes, so a test passes a fresh viper
  per case instead of calling `viper.Reset()`. The one package-level `cfgViper` exists because
  commands bind their flags to it from `init()`, which runs long before `initRoot`.
- The config global is `projectCfg`, never `cfg`: a bare `cfg` is shadowed by the `cfg config.Config`
  parameter of `initAllDependencies` and by any local `cfg :=` in a `RunE` — silently, with no
  diagnostic. `projectRoot` stays bare, since `dependencies.go` names its parameter `projectPath`.
- A command that touches storage or Docker wraps its body in `withDeps(cmd, func(ctx, d) error)`,
  which calls `initAllDependencies` and defers `d.Close(ctx)`. Both stores and the Docker engine are
  owned by the returned `*dependencies` — never deferred inside the constructor, or they would shut
  down before the command ran. `arcave init` deliberately does **not** use it: it builds an `InitService`
  via `initServiceDeps` and so never opens storage or contacts the daemon.

### Key design decisions

**Interfaces belong to the consumer (dependency inversion).** Each service file declares its own private interfaces for the repositories and clients it needs (`savepointRepository`, `projectLocker`, `composeInspector`, `composeRunner` in `service/core/service.go`; `configWriter` in `init.go`; `recommendationCatalog` in `recommend.go`). Each `cli` handler declares one for the service method it calls. `service/backend/tarcopy` declares `MountStreamer` — the two engine methods it needs — and `service/backend/restic/cdc` declares two of its own: `ContainerRunner` (the engine's one-shot and mount halves) and `RepoPublisher` (`rest.Mux`). Concrete types in `repository/`, `client/`, `service/lock/` and `service/` satisfy them implicitly — no interface definitions live there.

Exported or not is a separate axis from *where it lives*: `tarcopy.MountStreamer` and cdc's three are exported and still consumer-owned, because they are `NewFactory`'s parameters and godoc should name the contract at the constructor. cdc takes its four as a `Deps` **struct** rather than a parameter list: the seams have no natural order, so a new one would otherwise silently swap with an existing one at a call site. It is named for the capability (streaming a mount out and back in), never for the thing it happens to reach — `DockerVolume` named a noun the type is not, and `…Engine` would have named the mechanism.

**The interfaces that live outside their consumer are the ones a factory must name.** `model.Printer` is one: several consumers, and `cmd/arcave`'s `dependencies` holds a `Printer` field it hands to every handler. The backend seam is the other — `model.VolumeSnapshotBackend`, `model.SnapshotBackendFactory` and `model.SnapshotBackendRegistry` — because a factory in `service/backend/*` **returns** one and `core` holds a registry field, and both sides would otherwise have to import each other. A consumer-private interface can be a parameter, but never a return type or a struct field outside its package, so these contracts live where every side already looks. `storages.Storager` is the third and is not arcave's at all: it comes from the external storage library. An interface reaches `model` only under that same argument, not merely because it has two implementations.

**A client is named for its role, never "Client" and never for its transport.** The package name already qualifies the identifier, so a type never repeats it: `docker.Engine`, not `docker.Docker`; `introspect.Inspector`, not `introspect.DockerComposeInspector`. And the name states the contract, not the mechanism: `compose.Client` runs the project's lifecycle — it shelled out to the CLI once and calls `compose/v2` as a library now, and the name never went stale across that move. The consumer-side interfaces mirror these: `composeInspector`, `composeRunner` — no `docker` prefix, since the inspector never reaches the daemon.

**Repositories speak the domain model.** A repository's method contract accepts and returns the shared `internal/model` types; how it translates them to its on-disk representation is private to the repository. The savepoint repository's `List`/`Save`/`Delete` deal in `model.Savepoint`, and the record on disk is that type marshalled directly. Nothing outside `repository/` depends on a repository-specific persistence type.

**A repository is CRUD, never processing.** It reads and writes one thing, and it owns no client. `repository/snapshot` used to violate this — it held a Docker engine and streamed `OpenMount → PutObject` — so it moved under `service/`, where it is now `service/backend/tarcopy`. Use the test: *would this type still make sense if the only collaborator it had were storage?* If not, it is a service.

**The savepoint records live in the store they describe.** `savepoint.New` takes a `storages.Storager`, not a project path, and the DI root builds **one repository, for the savepoint tier**, over that tier's store — the very Storager its backends write captures into. The safety tier has none: a rollback's undo lives no longer than the rollback that took it. Records and data therefore travel together: point `storage.directory.path` at S3 and the records go to S3 with the captures. Absence is decided via `Storager.Exists`, never by matching `os.ErrNotExist` — that error is the directory backend's, and an object store would report a missing key differently. Every method takes a `ctx`, because reaching the store may cross a network.

`repository/config` is the deliberate exception and stays on `os`: `config.yaml` is *bootstrap*, the thing that tells arcave where the savepoint store is, so it cannot be read through a Storager configured from it. `arcave init` opens no storage at all, and should keep that property.

**One record per savepoint, and the record is the commit marker.** `<id>/savepoint.json` is a *reserved key at the root of the savepoint's own directory* — that directory belongs to the savepoint layer, and a backend writes its captures beneath it (`<object-id>/data.tar` for tarcopy, always ≥2 segments deep) but never at the reserved key. This replaced a monolithic `manifest.json`, for two reasons that are not the same reason:

- **Blast radius.** `PutObject` is deliberately non-atomic, so a torn write to a single index destroyed the *whole* savepoint index, not the one savepoint being written.
- **The race.** `CreateSavepoint` loaded the index, stopped the services, fanned out over every volume, and only then saved `append(records, record)` — silently clobbering anything written in that multi-minute window. With one record per savepoint there is nothing to append to, and the window is gone.

**The ordering is the crash-safety story, and nothing in the type system enforces it.** Create (`performSavepoint`): `requireEmptyTree` → every backend's `Snapshot` → `repo.Save` **last**. Remove (`dropSavepoint`): `repo.Delete` **first** → every backend's `Remove` → sweep the directory.

Uncommit-first *inverts* what a monolith wanted (data first, "so the index never points at data that is gone") — that was safe only because rewriting the whole index was a single swap. With a per-savepoint marker, data-first leaves a savepoint that still lists and still resolves but whose captures are half gone. Uncommit-first also is what lets the record live *inside* the savepoint directory: the sweep is a `DeleteAll("<id>")`, which deletes children in whatever order the store reports and could never be relied on to take the marker last — but by then step 1 already deleted it, as its own operation on its own object. **Reversing either pair compiles clean**, so both orders are pinned by a call-order unit test. The cost of uncommit-first is an orphaned directory when the data delete fails; it is logged and reported, and a future `arcave savepoint gc` sweeps directories with no record.

**An orphan tree is refused, never swept.** `requireEmptyTree` opens a create by listing the directory the new id names and failing with `errCaptureDirectoryNotEmpty` if anything is there. Only an interrupted run can have left it — a committed savepoint's id is never created twice — and those objects are the last copy of whatever that run captured, so they are named rather than deleted. This deliberately **inverts** the old core, which silently dropped the tree first; the refusal is also why a failed create skips its own cleanup in that one case (`dropCaptures` would delete data this run never wrote).

**Storage writes are not atomic, on purpose.** `Storager` has no `Rename`, and the directory backend copies straight into the target file, so an interrupted `PutObject` leaves a partial object. arcave does not pay for a temp-file commit protocol to avoid that: the *record* is the commit, so a savepoint that fails mid-write is invisible, dropped whole and re-created, never repaired. Don't add atomicity here without a failure this actually costs us.

**No key may name the store root, or anything outside it.** The storage library gates every object-addressing key against escaping the root it was given, and arcave adds the other half: `storefactory` roots every store under a static `arcave` namespace segment, so everything a delete can reach is arcave's own data — point `storage.directory.path` at `/etc` and arcave operates in `/etc/arcave`. That pair replaced the old `validateStoreRoot` blocklist, which enumerated bad filesystem roots and had nothing to say for an object store.

Two more gates stand in front of it, each at the layer that owns the value: `model.SavepointID.Validate` (digits only — a savepoint id is Unix seconds), which every layer rendering an id into a key calls first — `tarcopy.Snapshotter.captureStore`, `core.forEachBackend`, the savepoint repository; and `model.SessionID.Validate` (`"global"` or a canonical UUID), which `storefactory.New` calls before the session becomes a store root. Both are **allowlists**, for the same reason: a list of what a path segment must not be is never provably complete, while a known shape is.

**Four addressing levels, each owned by exactly one layer, and no layer names another's mechanism.** The *session* is `storefactory`'s: a `model.SessionID` that becomes the last segment of a store root, and the lifetime everything under it dies with — with `shared` as its sibling, for what a backend keeps across sessions. The *store* is the DI root's: a `storages.Storager` already rooted, never a path in a request. The *savepoint* is `service/core`'s: a `model.SavepointID` (Unix seconds), which doubles as the capture directory. The *volume* is `model.Volume`, addressed by its `ObjectID`. There is no `Namespace` type: a savepoint and a rollback's safety backup live in **separate stores** (`cfg.Storage` vs `.arcave/tmp`), so nothing needs a `rollback-` prefix to tell them apart — the id alone addresses either.

**`ComposeProject`, never `ProjectName`.** The name `docker compose` knows the stack by (used to locate containers) and the project as a *storage tenant* are different concepts, and conflating them is exactly what the old field names invited. So the stack's name is a field of `model.Volume` — stamped once, by `FindBackupVolumes`, and carried in the record — while the calls mention only a `SnapshotID`, which says where the data lands.

**The backend is volume-scoped and tier-bound; the service owns the fan-out.** `model.VolumeSnapshotBackend` is built for **one** volume and **one** `BackendTier` and serves a whole workflow: `Snapshot(ctx, BackendSnapshotRequest) (SnapshotStat, error)`, `Restore`, `Exists`, `Remove`, plus `Volume()` for attributing logs. The errgroup that works every volume concurrently lives once in `core.forEachBackend`, which every group goes through — and each group is a **barrier**: a rollback's revert must not start restoring while the failed restore's writes are still in flight.

The tier is the *instance's*, never the call's, so no call site can mix a savepoint's captures with a safety backup's — the mistake the old `savepointBackend`/`safetyBackupBackend` pair could only prevent by naming discipline. A rollback reverts through the very instances that took its backup.

**One backend per volume, built by a registry.** `model.Volume.Backend` names a `SnapshotBackendKind`; `core.SnapshotBackendRegistry` indexes one `model.SnapshotBackendFactory` per kind and builds the instance from a `model.BackendSpec{SessionStore, SafetyStore, Volume, Tier}`. The registry is assembled in `cmd/arcave/dependencies.go`, not in `core` — that is what keeps `internal/service/backend/*` out of `core`'s import graph — and it is the only place that names the engine, through the seam its backend declares, which is why `core` still knows no client package. The restic server it also owns crosses no seam at all: what `cdc.Deps` gets is the URL base that server answered with. `buildBackends` builds **every** backend up front, before the services are stopped: a volume whose backend cannot be built leaves the project running.

**A backend kind has no lifecycle of its own; whatever it shares lives for the process.** `model.SnapshotBackendFactory` is `Kind()` and `New(spec)`, and `model.SnapshotBackendRegistry` is `New(spec)` — nothing else. The one resource a kind shares between its instances is cdc's restic server, and `cmd/arcave/dependencies.go` builds it once — listening, beside the stores and the Docker engine — and closes it in `(*dependencies).Close`. One port per process.

**Through the backend seam goes neither a path nor a server object — only a value.** `cdc.Deps.RepoURLBase` is a string carrying the scheme and the host the container resolves; the backend appends `"<prefix>/"` and knows nothing of a transport. It replaced a `DataPlane` seam whose one method asked a *live server* for a URL, and that is what the deletion bought: everything in the answer — the bound port above all — is known before the first backend is built, so a question at capture time could only ever report what wiring already knew, or answer a lie about a server that was never started. (It did exactly that once: a server constructed and never started rendered port 0, and restic retried it for four minutes.) The seam that stays is `RepoPublisher`, and it stays because registering *is* an action at operation time — an asymmetry worth naming rather than smoothing over.

This deliberately **replaced** a per-workflow bracket (`Init`/`Done` on the factory, `Acquire(ctx, kinds)` on the registry, `openBackends` in `core`, `backendKindsOf` to name the kinds a run touches). It bought a narrower window for the loopback listener — reissued per workflow rather than per process — and cost an implicit control loop threaded through three layers: every workflow opened and released it, every mock declared it, and the ordering against `Stop` needed its own call-order test. The window is not worth that: the listener is `127.0.0.1:0` behind a fresh credential, and it now fails at wiring time, before a command touches the project, which is the property the bracket was really there for. `core` still never learns what an endpoint is, and neither does a backend.

What stays per operation is the *publish prefix*: `rest.Mux.Register` at the start of a cdc `Snapshot`/`Restore` and `unregister` when it ends, so a repository is reachable only while its own operation runs.

The spec carries both roots whatever the tier says, and `BackendSpec.TierStore()` reads the tier as naming one of them — the policy of a backend that keeps captures in the tree it was handed. A backend whose tier decides something else (`cdc`, whose repository lives outside both trees) never calls it.

**No path ever crosses the backend seam, but the layout is legible.** A backend is bound to its store at construction (`spec.TierStore()`, already rooted by `storefactory.New`), so the calls mention only a `SnapshotID`, never a directory. `tarcopy` narrows that root to `<snapshot-id>/<object-id>/` and writes one `data.tar` there — computed identically on the write and read sides, so the record stores no archive handle and a restore finds the capture by recomputing the key. `SnapshotStat.Payload` exists for the backends that *do* need to carry something, and only the backend that produced a payload interprets one. tarcopy reports the bytes it streamed. cdc carries the **restic snapshot id**, and that one is not a recomputable key but an address in restic's own name space: a restic id is content-derived, so nothing on the read side could rebuild it. The principle is loosened deliberately and only that far.

**The registry is injected at the DI root; the engine's lifecycle stays there.** The Docker engine is created once per command in `cmd/arcave/dependencies.go` (`docker.NewEngine` only builds the SDK client — API-version negotiation is deferred to the first call, so docker-free commands like `savepoint list` create it without contacting the daemon) and closed by `(*dependencies).Close`, which `withDeps` defers. `initAllDependencies` builds the savepoint and safety stores through `storefactory.New` and the cross-session store through `storefactory.NewShared`, starts the restic server, assembles the registry over them, and hands `core.New` one repository, both tier stores, and the backend kind `snapshot.backend` names. Whatever a half-finished wiring opened is released newest-first by a single deferred unwind (`partial`): a per-failure list of closers went stale every time a resource was added, and the one it forgot was the one that leaked. There is no `Close` on a backend or on the service.

**`storefactory.New` is the only place a configured path becomes a store.** It resolves a relative path against the project root (an absolute one is honoured verbatim), appends the `arcave` namespace and the session, and returns a Storager **already rooted** — which is why `filepath.IsAbs` no longer appears in `dependencies.go`, an `IsAbs` check no object store could honour. Only the *prefix* is created (`WithCreatePrefix`), never the root: a relative path is rooted at the project, which exists by definition, while an absolute path must already exist, so a typo cannot quietly grow a fresh tree.

**`internal/config` is a data package: no clients, no SDKs.** Its pluggable sections declare their own plain union arms rather than embedding `directory.Config` or a future `s3.Config` — embedding would drag the AWS SDK into the import graph of `repository/config`, and therefore of `arcave init`. `cmd/arcave` translates each section into its package's own options struct (`flags.ClientOptions`, `storefactory.New`, `cdcOptions` → `cdc.Options`). `storage` is a tagged union: `Type` names the arm, `Validate()` requires that arm to be present, and the arms are pointers so an unset one is omitted on marshal.

`backends` follows the same shape for a different reason: one optional section per snapshot backend, each a pointer, so a project carries a block only for a backend it configures. `ApplyDefaults` fills `backends.cdc` when the arm is present **or** `snapshot.backend` is `cdc`, and otherwise leaves it nil — a full-copy project should not have a block for a backend it never runs. `config.Snapshot.BackendKind()` is the one place the config's token vocabulary (`fullcopy`, `cdc`) meets `model.SnapshotBackendKind`; it lives in `config` because `config` already imports `model` and nothing else may.

`backends.cdc.data_plane` is a union in all but the tag's name — `transport` selects the arm, and a key of the *other* arm (`socket` under `tcp`, `address` under `unix`) is an **error**, not a value quietly ignored, exactly as `storage.type` treats its arms. That is also why `applyDefaults` fills one arm only: defaulting both would make every project fail its own validation. Only the `tcp` arm with `auth: none` is **implemented**, and the schema still carries the rest: `config` judges a value's shape, and "arcave cannot serve this yet" is not a shape. So `cdcDataPlane` in the DI root — the same place every pluggable section is translated, beside `cdcOptions` and named for the section it reads — refuses `transport: unix` and `auth: basic` at wiring time, before the command touches the project. It returns the section itself rather than one collaborator's options, because its two halves go to different places: `address` binds the listener, and `container_host` is no business of a listener's — it reaches the URL as an argument. `restic.Options` therefore carries neither a transport tag nor a credential.

**The config *policy* is `config.Unmarshal`'s; the *sources* are `cmd/arcave`'s.** `resolveConfig` owns only the merge: reading the file and letting viper layer flag > env > file > default. It then hands the loaded `*viper.Viper` to `config.Unmarshal`, which decodes and judges it. The decode lives with the type it fills — the `mapstructure` tags on `Config` are its schema, and re-implementing that decode over an `AllSettings()` map one layer up would only be viper's own `Unmarshal` spelled by hand. Everything a *value* means therefore lives in `config`: a test feeds it a viper built from a yaml literal.

**arcave speaks exactly one config version, and unknown keys are an error.** The real decode passes `rejectUnknownKeys` — a `viper.DecoderConfigOption` setting `mapstructure`'s `ErrorUnused` — so a key no field claims fails it. But that decode runs **only after the version gate**, and the gate reads the version through a **probe struct carrying nothing but `Version`**, never through `Config`: decoding the whole `Config` leniently just to read `version` would fail on a *type*, not a version, the day a schema arcave does not speak spells `storage` as a string. The probe cannot — it claims one key and ignores the rest.

The probe is **seeded with the current `Version`**, so a source naming no version at all reads as current. That is what lets a fresh project — no config file, resolved from flags and defaults alone — through the gate; `arcave init` stamps the version when it writes. Only a version that is *present and different* (`version: 0`, `version: 2`) is stale, and `model.ErrUnsupportedConfigVersion` reports it. arcave never migrates such a file in place: that would clobber a `savepoint_dir` the user set deliberately.

The consequence is that the pre-v1 flat layout is **not** caught by the version gate — it carries no `version`, so it reaches the strict decode and is rejected by the keys it uses (`invalid keys: name, savepoint_dir`). Naming the foreign keys is the diagnosis; there is no second supported version for the gate to route it to.

**The engine and the compose runner share one resolved client, so they cannot target different daemons.** `docker.Engine` reaches the daemon through the SDK; `compose.Client` and `wipeMount` (the package-level helper behind both `Engine.WriteMount` and the exported `Engine.WipeMount`) reach it through `compose/v2` and the SDK respectively. Nothing shells out. The resolution happens **once, in the DI root**: `initDockerClient` translates `config.Docker` into `flags.ClientOptions`, hands them to a `command.DockerCli`, and takes the `client.APIClient` that instance resolves; `docker.NewEngine` is handed that client and `dockercompose.NewComposeService` is built over that *same* CLI instance. "Both halves honour the same options" used to be a contract each had to keep by hand — an exported savepoint restored into the wrong daemon was one forgotten env var away — and is now a property of the construction.

Two consequences of moving onto `docker/cli`:

- **`docker.context` no longer needs `docker.host`; the two are now mutually exclusive.** The old rule existed because a bare SDK client cannot resolve a CLI context. `docker/cli`'s context store can, so a context alone is the supported way to name a remote daemon — and `docker/cli` itself refuses host+context, since a context already names a host. `getDockerOptions` rejects the pair with arcave's own message rather than letting the CLI's surface.
- **The endpoint is resolved eagerly, through `DockerEndpoint()` and never `Client()` first.** `Client()` reports a failure to resolve by writing to its error stream — which arcave points at a buffer until resolution succeeds, since that is where compose draws its progress UI — and then calling `os.Exit(1)`. A mistyped `docker.context` would kill arcave with no output at all, and the check cannot live behind the engine's own accessor: the compose library calls `Client()` itself. Resolving does ping the daemon, but a failed ping is not an error, so `savepoint list` still works with docker stopped.

**The compose library is a pinned, expensive dependency, and it dictates `compose-go`.** `github.com/docker/compose/v2` drags in `docker/cli`, buildkit, buildx and the AWS SDK: `bin/arcave` went from **17 MB to 97 MB** (65 MB with `-s -w -trimpath`) — well past the 20–40 MB the migration estimated, and the price paid for not requiring a docker CLI on the host. `pkg/api`/`pkg/compose` are semi-public and offer no compatibility guarantee (`NewComposeService` has changed signature before, and it returns `api.Compose`, not `api.Service`), so the version is pinned and upgrading it is deliberate work, not a `go get -u`. Note also that **`compose-go` is pinned down to whatever compose/v2 was built against** — v2.40.3 needs v2.9.1, and v2.11.0 does not compile against it (`types.OptOut` vs `bool` on `CreateHostPath`). Bump the two together or not at all.

`docker.api_version` is carried by setting `DOCKER_API_VERSION` in arcave's own process: the client `docker/cli` builds from a resolved endpoint reads the version from the environment (`client.WithVersionFromEnv`) and otherwise negotiates, and that is the only seam it leaves. Setting a process env var is safe here in a way it would not have been before — arcave spawns no child processes, so it configures arcave's client and nothing else.

**Wiping a mount is a policy over a reusable one-shot-container mechanism.** `runOneShotContainer`
(`client/docker/oneshot.go`) drives one throwaway container through its whole lifecycle — ensure the
image, create, attach (only when the spec carries stdio), start, exchange, wait, and always remove,
with a non-zero exit reported as an error carrying `containerOutput`. `wipeMount` is now only the
*policy*: which bind source a `mountInfo` maps to, the `busybox` image, and the `find … -exec rm -rf`
command. arcave will run other one-shot containers — some of them talking over stdio — and they need the
mechanism without the wipe.

Both helpers stay **unexported**; what is exported are the `Engine` methods over them, and only once a
caller outside the package pulled on one. `RunOneShot` and `WipeMount` are those callers' — the cdc
backend runs its own containers and clears a mount before a wipe-mode restore — alongside
`ResolveMountSource`, which answers what a container has to bind to reach a mount and is the only way
to address an anonymous volume, whose docker name is created per container. `ResolveMountSource` and
`wipeMount` share one rule (`mountSource`), so a backend addressing a volume can never disagree with
the wipe that clears it.

The extension point is the **spec struct, not the parameter list**: stdio is three optional fields
(`stdin`, `stdout`, `stderr`), so a new kind of run adds a field and `wipeMount` — which sets none of
them — is untouched. `Tty` stays false even with stdio, since demultiplexing stdout from stderr needs
the framed stream. Two orderings inside the mechanism are load-bearing and pinned by tests, because
both compile clean when wrong:

- **Attach precedes start.** A container started first can produce, and lose, output before anyone is
  listening.
- **The stdin writer and the stdout reader are concurrent** (an errgroup). Filling stdin before
  reading any output deadlocks on any payload past the pipe buffer: the container blocks writing
  output nobody drains while arcave blocks writing input nobody reads.
  `TestRunOneShotContainer_StdioLargePayload` is what fails — by timing out — if that is ever
  unwound.

A container may legitimately exit without draining its stdin (think `head`), which fails the copy on
the *write* side. That is not an error — the exit status decides the run's outcome — so `exchange`
reports only a failure of the caller's own source, which is what `recordingReader` exists to tell
apart.

**The project lock belongs to the workflow, not to the command layer.** "A savepoint is never created while a rollback is running" is a property of the workflow, so `core.Service` claims the lock itself: `CreateSavepoint`, `Rollback`, and a non-dry-run `RemoveSavepoints` each open with `s.lockProject(ctx)` and `defer unlock()`. `ListSavepoints` and `--dry-run` take no lock, so a read never blocks on a running rollback.

A lock taken in `cmd/arcave` (a `withLockedDeps` helper) would have been **invisible to `arcave mcp`**, which calls `core.Service` directly — every agent-driven rollback would have run unserialised. The same argument rules out `internal/cli`. The lock is taken **first**, before the store is read and before `docker compose stop`: a caller that cannot have the project must not touch it.

The seam is `core.projectLocker` — `Acquire(ctx) (release func() error, err error)` — and `lock.Locker` is bound to its project root at construction, exactly as a snapshot backend is bound to its store, so no path crosses it. `Acquire` takes a `ctx` it does not use today, because the seam has to survive a locker that reaches a network: a lease in the shared store is the only kind of lock that could serialise two *hosts*. The flock cannot: it is **host-local** and does not protect a store shared between machines.

The sentinel is **`model.ErrProjectLocked`, not `lock.ErrHeld`** — `core` returns it and must not import `service/lock`, so the contract lives in `model` with `ErrRemoveSelectorConflict` and friends. Releasing is best-effort: `lockProject` returns an `unlock func()` that logs a release failure rather than handing the caller a second error to reconcile with the one it is already returning.

**Neither tier's store is called `store`.** The two stores share a type and `rollback.go` works both within twenty lines of each other; swapping them would capture into one tree and sweep the other, and compile clean. So the *names* carry the distinction the type system cannot: `savepointStore` and `safetyBackupStore`, and the helpers that pair a store with what it holds (`dropSavepoint` takes the repository and store as parameters; `dropSafetyBackup` is the one caller that names the safety store). Never reintroduce a bare `store`, and never let one name be a prefix of the other. `savepointRepo` keeps its qualifier for the same reason, even though it is now the only repository: a bare `repo` next to two stores reads as belonging to either.

**`time.Now` is called where it is needed, not injected.** `core.New` takes collaborators only; the savepoint id comes off the wall clock inside `performSavepoint` and `--retain-for` counts back from `time.Now()` in `retentionCutoffs`. A test reads the id back from what the run returned or recorded (`candidateIDs` covers the second the clock may tick into), and asserts a relative cutoff against the clock it read itself. A test that exercises one method passes `nil` for the dependencies that method never touches — `New(repo, locker, nil, nil, registry, session, nil, model.SnapshotBackendCopy)` for `RemoveSavepoints`, which reads records, takes the lock, and drops directories, but never inspects or runs compose.

**`arcave init` is its own service.** `core.InitService` (`service/core/init.go`) holds only the project root, a `configWriter`, and a `composeInspector` — it shares nothing else with the savepoint workflows, so `arcave init` runs without storage, a backend registry, or the Docker engine. Splitting further was rejected: `Rollback`'s dependencies are a strict superset of `Create`/`List`/`Remove`'s, so separating those would only duplicate five wiring fields.

**`Init` takes the `config.Config` it persists, not a request type.** It is the one service method whose input *is* a config — verifying the compose project and writing the file is the whole of it — so a `model.InitRequest` mirroring a subset of the sections could only ever be a lossy copy: it dropped `docker` and `snapshot`, which meant `arcave init --override` silently erased a configured `docker.host` from the file it rewrote. And it would have had to re-flatten the `storage` union into request fields (`SavepointDir` today, `S3Bucket`/`S3Prefix` tomorrow) — naming one arm's mechanism at a layer that must not know the arms, the same smell a backend avoids by taking a `model.Volume` and a `SnapshotID` rather than a path. `override` stays an argument rather than a field: it steers the run and is never persisted. `Init` creates no savepoint directory, because there is nothing to create — the directory is a `storage` section here and nothing more, and `storefactory.New` brings it into existence at the first savepoint.

`ApplyDefaults` therefore has to be safe on a value: `Config` reaches `Init` by copy, and the storage arm is a **pointer**, so filling `Directory.Path` through it would reach across that copy and mutate the caller's own config. It fills a copy of the arm and re-points at it.

**A safety backup is a capture without a record.** `Rollback` takes it through `captureVolumes` — the half of `performSavepoint` that stops before the commit — handed the safety tier's store and backends. `performSavepoint` is then `captureVolumes` plus `repo.Save`, and only the create path calls it.

No record, because there is no reader for one: the backup lives for the length of one `arcave rollback`, held in memory, and the revert gates on `requireComplete` over the very instances that took it. A record would only pay for itself under a *crash-recoverable* revert — arcave restarting and finishing a rollback it died in the middle of — and that is deliberately not built. `dropSafetyBackup` is therefore `dropCaptures` over the safety store, with no uncommit in front of it, and `core.New` takes one repository rather than two.

What a human is left with instead is the **label**. `Rollback` stamps `arcave-tier=safety` on the capture, and a backend that has somewhere to put it renders it into its own store — cdc into a restic tag, so a backup a killed rollback left behind is findable with a plain `restic snapshots`. tarcopy ignores labels, having nowhere to write them; for it the leftover is a directory under `.arcave/tmp` with no record, which is what a future `arcave savepoint gc` sweeps.

**Labels are a general mark, and the safety tier is only their first buyer.** `model.Savepoint.Labels` sits beside `Description`, `CreateSavepointRequest` accepts them, and `BackendSnapshotRequest` carries them to every backend. They are *not* a tier flag with a nicer name: a backend renders whatever it is given, and never reads a particular key. The adapter-side flags (`savepoint create --label`, the MCP field) are not built yet.

**One backup concept, one name: `safetyBackup`.** `rollback.go` takes exactly one kind of backup, and every symbol in the flow spells it out: `dropSafetyBackup`, the `safetyBackup` record, `safetyBackends`, and the `safety_backup_id` log field. A bare `backup…` name is banned here *precisely because* nothing else in the file is a backup — the reader cannot know that, so `backupID` sitting next to `safetyBackup` reads as two different things. Same rule as the per-tier pairs above, in the other direction: when the type system cannot separate two concepts the names must, and when there is only *one* concept the names must not imply two.

**`SnapshotStat` in the savepoint record.** `model.Savepoint` is both the metadata and the single-savepoint wire contract, and carries `[]model.SnapshotStat` — one per volume, each `{volume, type, payload}`. There is no stored archive path: a rollback rebuilds one backend per `stat.Volume` and hands it `{record.ID, stat.Payload}`, and the backend addresses its own capture. `Stats` therefore indexes alike with the backends built from it, which is what lets the fan-out write results back by index.

**Pre-flight is per backend, not a store listing.** `requireComplete` asks every backend `Exists` for the id and names **all** the gaps at once — a half-restore is worse than none — rather than asking one backend which archives are missing. It runs before a rollback stops anything, and again before a revert: a safety backup with a hole would mix savepoint data with live data.

What "exists" means is the backend's, and the two answer differently. tarcopy stats its archive. **cdc answers `true` without asking anything**: the record is the commit marker, written last and only after every backend reported success, so a record naming a capture already *is* the statement that the capture completed — re-confirming it would cost a round trip per volume to learn what the caller knew before it asked. The blind spot is bounded and deliberate: the marker covers arcave's own interruptions, not somebody deleting a snapshot behind arcave's back (an external `restic forget`, a hand-edited store). That case surfaces mid-rollback and rides the ordinary revert. A real server-side check — `Exists("snapshots/<rid>")`, which needs the payload in `BackendExistsRequest` — belongs to the increment about stability under an unfinished transaction, not here.

### The cdc backend

**A repository per volume, in the shared store, outliving every session.** `<configured path>/arcave/shared/restic/<object-id>` — `shared` sits *beside* the sessions rather than under one, because a repository that died with its session would make every session change pay a cold full backup, which is the one thing this backend exists to avoid. It cannot collide with a session: `SessionID.Validate` admits only `global` or a canonical UUID. Per *volume* rather than per project, because an exclusive operation (`prune`, `forget`) would otherwise block every volume at once, the per-volume fan-out would contend on one lock space, and cross-volume dedup between a postgres volume and a minio volume buys almost nothing — while dedup *across time*, where all the value is, is untouched.

**Both tiers write to the same repository, and the tier is a tag.** A safety backup names the same `--host` and the same path as the savepoints of that volume, which is exactly what restic picks a parent snapshot by — so the undo is an increment over the state it is undoing, and costs almost nothing. Nothing but the publish prefix and the tier label distinguishes the two instances. Give the safety tier its own repository and the first rollback of a project pays a full backup for nothing.

**Half of the backend never starts a container.** arcave owns the store the repository lives in and already speaks its object protocol, so `repo_admin.go` answers the state questions server-side: `Exists("config")` for "is it initialised", `List("locks/")`+`Delete` for the lock sweep, `Delete("snapshots/<rid>")` for a removal — byte for byte what `forget <id>` does on the wire, without the container and without the exclusive lock. Only what needs the repository *key* has to run restic: `init`, `backup`, `restore`.

**Every standing lock is an orphan, and `restic unlock` cannot clear it.** A lock written inside a container carries that container's hostname, so to restic in the *next* container it is a live lock held by a stranger — the `stale()` predicate ("same hostname, dead PID") never fires, and only the 30-minute age quarantine would ever clear it. A backup killed a minute ago is therefore unpickable by `unlock`. So arcave deletes `locks/*` server-side at the start of each operation instead. It is safe because arcave holds the project flock for the whole workflow and the repository has no other client by construction. Only at the start: restic refreshes its own lock while it works, and those writes are ordinary traffic. It inherits the flock's host-locality, like everything else in arcave.

**A restore is `--target /` with `--include`.** The paths inside a snapshot are absolute (`/m/<object-id>`, the mount point inside the container), so restoring at the root puts them back exactly in the mounted volume; `--include` is what keeps that scoped, and is required for `--delete` to aim at the volume rather than the container's whole filesystem. `restore_mode: wipe` (the default) clears the mount with the existing busybox policy and then restores it whole — the same destructive shape tarcopy has always had. `restore_mode: incremental` skips the wipe and lets restic write only what differs (`--delete --overwrite <if-changed|always>`, needing restic ≥0.17, which compares content rather than size+mtime). The wipe must finish before restic writes a byte; reversed it would delete its own restore, so the order is pinned by a test.

**The password is a constant.** Encryption buys nothing in this threat model — whoever can read the store can read `.arcave` too — while a lost password is lost data. The bonus is that a user can open the repository with a plain `restic -r <path> snapshots` to debug it.

**Every restic command is a one-shot container of the stock image.** arcave ships no adapter into the container: the plan is linear and built *before* any container starts (the branching is `repoAdmin`'s), so the execution strategy — a container per command today, one container plus `docker exec` per command tomorrow — is a detail of `runner.go`, invisible to the snapshotter, and a change to make by measurement. Worst case is two containers per volume per operation (`init`+`backup`), which is not where the time goes.

**The container reaches the repository over the restic server, and the prefix is the caller's.** `Register(prefix, repoStore)` at the start of `Snapshot`/`Restore`, `defer unregister()`; `Exists` and `Remove` need no prefix at all. The prefix is `<tier>-<object-id with "/"→"-">` — one path segment, flattened by the *caller* because that is the mux's contract, and carrying the tier so a rollback's two instances of one volume collide loudly (`model.ErrRepositoryPrefixRegistered`) rather than one quietly capturing the other's traffic.

**The transport is the user's, under `backends.cdc.data_plane`, and it has one implemented arm.** `transport: tcp` binds `127.0.0.1:0` and the container reaches it at `container_host: host.docker.internal` — which Docker Desktop resolves and native Linux does not, so that key is the one that gets cdc running there (with an `ExtraHosts`/`host-gateway` field on `OneShotSpec`, still to come). Port `0` is what keeps two arcave processes on one project — a CLI run and an `arcave mcp` server — from colliding, and a fixed target from being a predictable one; an explicit `address` is honoured, and an occupied one fails the command at wiring time rather than mid-capture.

There is **no authorisation** and no unix socket. Both were built and then deleted: what they guarded is a loopback port carrying data whoever can read the store can read anyway, and what they cost was a generated credential threaded through three layers, a constant-time validator, a socket-path length gate and a per-arm default table — for a listener that is open for the length of one command. `Ping` went the same way: a health check made once at wiring time only ever proved the process could reach its own listener.

**Echo went out with them and came back on its own argument.** It was deleted as the *carrier* of that authorisation; it is back as an extension point, because more integrations are coming and each of them would otherwise be another hand-rolled `func(http.Handler) http.Handler` with no shared logger, error handler or ordering. What is deliberately different is *where* it sits: above `rest.Mux`, in `restic/edge`, never in front of the protocol. The routing stays `http.ServeMux`'s, because what a path means, which method it takes, the sorted `Allow` of a 405 and the HEAD implied by every GET are all pinned against a real `rest-server` by the oracle test — and `echo.WrapHandler` hands the wrapped handler the *original* `*http.Request`, so the escaped path `grammar.go` judges arrives untouched.

The chain is three deep and its order is load-bearing: `RequestID` first, so the id is on the request before the journal stamps a logger from it; then `echo.WrapMiddleware(logging.Middleware)` — `rest/logging` unchanged and still a plain `func(http.Handler) http.Handler`, which is what keeps `rest/` free of the framework; then `Recover` *inside* the journal, so a recovered panic still ends in a `served` line. `RemoveTrailingSlash` is refused on purpose — restic's protocol needs `/<prefix>/locks/` — and so is `middleware.Logger`, which is `rest/logging` with less in it.

Raising the journal to the edge is what finally gives the mux's own 404 — a prefix no repository is registered under — the same pair of lines as everything else; `Mux.ServeHTTP` therefore reads the logger straight out of the context rather than stamping it a second time.

What `restic.HTTPServer` was already doing it still does, and Echo is given neither job: `BaseContext` carries the command's logger down to `rest/logging`, and `ErrorLog` catches a panic that escapes the chain itself. Two things are re-pointed at that same logger instead of **stdout**, which is a command's result channel and `arcave mcp`'s JSON-RPC channel: `echo.New` aims `e.Logger` and `e.StdLogger` there, and `edge` aims them back. Echo's JSON error body is replaced wholesale by `handler.HTTPError`, so a 405 or a recovered panic reads like every other error on this wire. `restic.HTTPServer` keeps a conformance test of its own: the same restic lifecycle as `rest`'s, driven through the real listener and the real edge.

**All output is JSON.** Every command writes JSON to stdout. Single-savepoint payloads marshal `model.Savepoint` directly — its `json` tags are the wire contract, so no duplicate type or identity mapper. The multi-part response envelopes that have no other domain equivalent (`model.RollbackResponse`, `model.RemoveResponse`) live in `internal/model/` alongside the domain types; both CLI and MCP adapters share them. There is no separate `dto` package.

**`.arcave/` directory layout.**
- `.arcave/config.yaml` — project config (`config.Config`): `version: 1` plus one section per subsystem (`project`, `docker`, `compose`, `storage`, `snapshot`, `backends`, `log`). The one file read through `os` rather than a Storager, because it says where the Storager points
- `.arcave/lock` — the flock target `core.Service` holds for the duration of a mutating workflow. Created empty and never unlinked (flock guards the inode, not the name)
- capture data lives under `<configured path>/arcave/<session>/`, where the configured path is `cfg.Storage.Directory.Path` (default `.arcave/savepoints/<project-name>/`), `arcave` is the static namespace `storefactory` appends, and `<session>` is `model.DefaultSession` (`global`) today. Below that root, one self-contained directory per savepoint:
  - `<savepoint-id>/savepoint.json` — the record, a **reserved key** at the directory root, owned by `repository/savepoint`. Written last on create, deleted first on remove. Holds the `model.Savepoint`, whose `stats` carry one entry per volume: the `model.Volume` itself, the backend kind, and that backend's payload — no archive path, since each backend addresses its own capture
  - `<savepoint-id>/<object-id>/data.tar` — the tarcopy capture, where `<object-id>` is `model.Volume.ObjectID` (`volume/<docker-name>` for a named volume, `service/<service>/<target>` — the mount path with its leading `/` stripped — for a bind or anonymous one). Nesting the real target keeps the layout collision-free, since the filesystem distinguishes `/export/data` from `/export_data` (unlike flattening `/` to `_`)
  - a directory with **no** `savepoint.json` is an incomplete savepoint: invisible to `list`, safe to GC — and refused as a capture directory if a new savepoint's id lands on it
  - a cdc savepoint's directory holds **only** the record: its bytes live in the repository below
- `<configured path>/arcave/shared/restic/<object-id>/` — one restic repository per volume, beside the sessions rather than under one, so it survives a session change. Written by cdc through the restic server, never by the savepoint layer; a plain `restic -r <that path>` opens it
- `.arcave/tmp/arcave/<session>/<id>/…` — a rollback's safety backup, in its own store (so no
  `rollback-` prefix is needed to tell it from a savepoint) and with **no `savepoint.json`**: the
  safety tier commits no record. tarcopy leaves its archives here for the length of the rollback;
  cdc leaves nothing at all, its safety capture being a tagged snapshot in the shared repository.
  Created and deleted within one `arcave rollback`, retained only when the revert itself failed; the
  now-empty parent is left in place.

**Savepoint IDs** are Unix timestamp integers (e.g. `1780335122`) used as both identifier and storage directory. `model.SavepointID.Validate` enforces "a non-empty run of digits" — that is the first path-safety gate, and it is why a hand-planted `"id": ""` or `"../.."` can never reach a syscall.

**`arcave status` is a registered stub, not absent.** It exists in `cmd/arcave` and returns `not implemented`; it carries no flags, no handler, and no dependencies yet. Registering it early keeps the command tree honest about what arcave intends to be — but do not cite it as existing behaviour. `arcave fix` does not exist in any form. `arcave doctor` does not either: `arcave recommend` shipped the one check that was worth shipping alone (a stateful image with nothing persistent mounted where it writes), and anything further under a `doctor` name must keep the property `recommend` already has — strictly read-only, compose files and catalog and nothing else.

**`arcave recommend` is read-only, and its exit code is a finding.** `core.RecommendService` reads the ARC catalog and the compose services, matches each service's normalized image repository against the catalog, and emits one `model.Recommendation` per catalog volume no persistent mount already covers. It takes no lock — there is nothing to serialise against a running rollback — and touches neither storage nor the daemon. "There are recommendations" exits 1, and it is **not** an error: the JSON array still goes to stdout and nothing goes to stderr. The mechanism is `cli.RecommendHandler.Recommend` returning `(found bool, err error)` and `runRecommend` setting the package var `rootExitCode`, which `main` exits through after a successful `ExecuteContext`. A typed error carrying an exit code was rejected — cobra would print it to stderr, and a finding is not a failure.

Recommend reads the **same introspection the savepoint workflows read**: one `composeInspector` seam, one `Introspect` call, `model.Service` carrying both the verbatim `Image` (what the user sees, what the hash covers) and the mounts that decide coverage. Normalizing an image to its match key is the service's own `imageRepo`, so `distribution/reference` stays in `service/core`. There is no second inspector method and no `composeServiceInspector` seam: the introspection is a fact both consumers read the half they need of.

**Volume detection captures every service.** arcave snapshots the persistent volumes of *all* compose services — there is no image-name allowlist. Every unit is stamped with the backend `snapshot.backend` names, which `FindBackupVolumes` takes as a parameter; a rollback reads the backend out of the record instead, so a savepoint is always restored by the backend that took it, whatever the config says now. Named volumes, bind mounts, and anonymous volumes are all units of backup, deduplicated by `ObjectID`, so a named volume three services mount is captured **once** with every mount recorded against it; only tmpfs and other non-persistent mount types are skipped, with a warning.

**A create covers exactly what its rollback would.** Both call `resolveBackupVolumes`, which runs `FindBackupVolumes` (what a savepoint covers, on the backend `snapshot.backend` names) and then `reachableVolumes` (what the backends can get at). The second is a separate pass because reachability is the *backend's* property, not the domain's: a named volume nobody mounts, or one only ever mounted at a subpath, has no container to read the whole volume through. Capturing it would produce a savepoint no rollback could apply.

**`arcave rollback <savepoint-id|name|latest>`** restores the environment to a savepoint. Resolution order: `latest` (newest by `CreatedAt`) → exact ID (IDs are unique, so they win) → name. A name matching several savepoints is an *error* listing the candidate IDs, never a silent guess. Flow:

0. Acquire the project lock (`.arcave/lock`); a second concurrent `arcave` fails fast.
1. Resolve the record, resolve the *live* volumes, and build both sets of backends — the savepoint's from `record.Stats`, the safety tier's from the live volumes, since a volume may have been added or dropped since the savepoint was taken.
2. Pre-flight (`requireComplete`): ask every savepoint backend whether its capture exists; abort untouched if any is missing.
3. `composeRunner.Stop` — compose's `Stop`, the equivalent of `docker compose stop` (containers kept, never `down`, so mounts stay resolvable).
4. **Safety backup** of the *current* live state under `<unix-ts>`, taken by the same `captureVolumes` a create uses but committing no record, and stamped `arcave-tier=safety` — so a failed rollback can be undone, and a backup a killed rollback left behind is at least findable in the backend's own store.
5. For each savepoint volume: **wipe** then **restore**. Wipe creates, starts and waits on a throwaway root `busybox` container that mounts the volume/bind path and clears it — the only host-agnostic way to delete data the service wrote as root (`CopyToContainer` only merges, never deletes). It runs through the SDK, pulling the image only if the daemon lacks it, and folds the container's own logs into the error on a non-zero exit. Restore extracts `data.tar` via `CopyToContainer` into the *parent* of the mount path (the tar's top entry is the mount basename).
6. `composeRunner.Up` — compose's `Up` with `Start.Wait` and a `WaitTimeout` (120s by default, applied by the compose runner when the request carries none; overridable via `--wait-timeout`, which `savepoint create` carries too), the equivalent of `docker compose up -d --wait --wait-timeout <n>`. The bound is required: without it an unhealthy `restart: always` container would hang the wait forever.

Both calls load the project through the same `compose.Loader` the inspector uses, so the stack arcave
inspects and the stack it stops are the same stack — and then stamp each service with compose's own
labels (`labelProject`). The labels are not optional: called as a library, compose does not add them
for you, and without them it does not recognise its own containers.

**The compose library's zero options are not the compose CLI's defaults, and the gap is silent.**
`Up` therefore sets `api.CreateOptions.Inherit: true` explicitly. Inherit carries a recreated
container's *anonymous* volumes over from the container it replaces; the CLI defaults it on (it is
what `--renew-anon-volumes` turns off) and the zero value is off. arcave captures anonymous volumes like
any other mount, so leaving it off discarded their data whenever a container diverged from the
compose file — which is precisely what a arcave user is in the middle of doing. Nothing in the type
system flags this: the field is a `bool` whose wrong value is the zero value, and a savepoint/rollback
over *named* volumes passes either way, so the unit test asserting `Inherit` pins it.

**Rollback auto-reverts on failure.** If any restore step fails *or* the services come up unhealthy (compose reports an unhealthy container, or the wait bound elapses), arcave replays the safety backup — through the very backend instances that took it — to return the environment to its pre-rollback state, restarts services, and returns an error. The backup is deleted on full success **and after a successful revert**; it is retained, with its id logged, only when the revert itself failed, which is the one path where it is the last copy of the pre-rollback state. Restore is destructive — bind mounts (the user's own host folders) are wiped too, by design.

### Error-variable naming

Prefer the plain `err :=` over a bespoke named variable (`upErr`, `revertErr`, …). A distinct name is only worth it when the error outlives the branch that produced it — e.g. it's compared, wrapped later, or coexists with another live `err` in the same scope. When the error is used only inside the `if` that guards it and that branch exits immediately, reuse `err`:

```go
// Prefer this:
if err := s.composeRunner.Stop(ctx, req.ComposeFiles...); err != nil {
    return model.RollbackResult{}, fmt.Errorf("stopping services: %w", err)
}

// Over an unnecessary alias:
if stopErr := s.composeRunner.Stop(ctx, req.ComposeFiles...); stopErr != nil {
    return model.RollbackResult{}, fmt.Errorf("stopping services: %w", stopErr)
}
```

`Rollback` shows the exception the rule exists for. Inside the `if err := s.applySavepoint(...)`
branch, `err` is the rollback failure being reported — still live at the `return` — so the revert's
own error genuinely needs a name of its own:

```go
if err := s.applySavepoint(ctx, record, savepointBackends, req.ComposeFiles, req.WaitTimeout); err != nil {
    revertErr := s.revert(ctx, safetyBackup, safetyBackends, req.ComposeFiles, req.WaitTimeout)
    if revertErr != nil {
        log.Error().Err(revertErr).Str("safety_backup_id", safetyBackup.ID.String()).
            Msg("revert failed: environment left in an inconsistent state, safety backup retained")
        return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
    }
    ...
    return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
}
```

### Return the primary error; log secondary ones

A handler returns **one** error — the primary cause. A secondary failure that happens *while* the primary error is in flight (a best-effort restart, a cleanup, a rollback of a partial write) does not change the outcome, so it does not belong in the returned value. **Log it and drop it.** Do not build a combined message that folds a second cause in with `%v`, and do not enumerate every `(primaryErr, secondaryErr)` combination in a `switch` — those cases carry no control-flow, only prose.

Two seams to keep clean:

1. **A handler does one job and returns its own error.** Don't pass a prior step's error *into* a handler for it to reconcile — the moment a "restart the services" function takes a `snapErr` parameter, it has stopped being a restart function and become an ad-hoc error combiner. Cross-error precedence ("which of these two failures do we return?") is the **orchestrator's** decision, made at the call site where both operations are visible — not buried in a helper.
2. **Wrapping with caller context happens *above* the handler**, never inside the step that produced the error and also decides the outcome.

```go
// Prefer this (CreateSavepoint) — the restart is inline in the orchestrator. The
// capture error is primary, so the failure path restarts best-effort, logs what
// went wrong there, and returns the capture error; the success path returns the
// restart's own error. The orchestrator, not a helper, decides precedence:
record, err := s.performSavepoint(ctx, s.savepointRepo, s.savepointStore, backends, req.Name, req.Description)
if err != nil {
    log.Info().Msg("starting services")
    if err := s.composeRunner.Up(ctx, req.WaitTimeout, req.ComposeFiles...); err != nil {
        log.Error().Err(err).Msg("services failed to restart after capture failure")
    }
    return model.Savepoint{}, fmt.Errorf("capturing volumes: %w", err)
}

log.Info().Msg("starting services")
if err := s.composeRunner.Up(ctx, req.WaitTimeout, req.ComposeFiles...); err != nil {
    return model.Savepoint{}, fmt.Errorf("starting services: %w", err)
}

// Over this — a "restart" helper that takes the prior cause and reconciles the
// two, plus a switch whose cases only assemble strings and a %v that smuggles a
// second cause into one error:
func (s *Service) restartAfterSnapshot(ctx context.Context, dc composeRunner, snapErr error) error {
    upErr := dc.Up(ctx, req.WaitTimeout)
    switch {
    case snapErr != nil && upErr != nil:
        return fmt.Errorf("%w; additionally, services failed to restart: %v", snapErr, upErr)
    case snapErr != nil:
        return snapErr
    default:
        return upErr
    }
}
```

`Rollback` applies the same shape to its own two abort paths: `abortBeforeRestore` when the safety
backup itself fails (nothing was touched), and `revert` when a restore fails after it succeeded.
Both are best-effort, both log, and neither reaches the returned error.

When special behaviour genuinely must reach the caller (not just the log), express it as a **custom error type with a contract** the caller matches on (`errors.Is`/`errors.As`) — never as free-text the caller would have to string-match. Rule of thumb: **UX is either a typed error with a contract, or a log line — never a hand-concatenated multi-cause string.**

### Function ordering within a file

Order declarations so a file reads top-to-bottom, "headline → details" (caller above callee):

1. **Package constants**, then the **types** the file is about (exported types first). A constant the
   whole file is bounded by leads it (`maxConcurrentOperations` in `service/core/service.go`);
   one that only a single helper reads sits with that helper (`refLatest`, above `findSavepoint` at the
   foot of `savepoint.go`).
2. A type's **constructor (`New…`) sits immediately under that type**, and its methods are grouped
   with it — never scattered.
3. The **entry point** — the exported function/method the file exists for — comes right after the
   types, before its helpers.
4. **Private helpers follow in call order**: each helper appears after the function that first calls
   it, and a helper-of-a-helper sits under its own caller. A reader descends into detail only as
   deep as they care to.

`rollback.go` is the reference: `Rollback` (the entry point) sits at the top, then
`resolveSavepoint`, `abortBeforeRestore`, `applySavepoint`, `revert`, `dropSafetyBackup`, and the
projection helper `volumesOf` — the exact order the entry point reaches them. `gofmt` does not
enforce this, so it is on you.

## Spec and Implementation Plan

- Full CLI UX spec (commands, flags, JSON output schemas, exit codes): `doc/spec.md`
- Detailed implementation plan with interface signatures: `doc/proposed-impl.md`


# Writing tests

These are the rules for **new and modified** tests. Some older tests still use bare
`t.Errorf`/`t.Fatalf`; do not copy that style — when you touch such a test, migrate it toward the
conventions below.

## 1. Table-driven wherever possible

If a behavior has more than one meaningful input/output pair, express it as a table. One `t.Run`
per case with a descriptive `name`, and the table is a slice of anonymous structs.

```go
func TestIntervalSkipsWhenCovered(t *testing.T) {
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	today := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		lastAt   time.Time
		wantSkip bool
		wantFrom time.Time
	}{
		{name: "checkpoint covers yesterday", lastAt: today, wantSkip: true},
		{name: "checkpoint a day earlier", lastAt: today.AddDate(0, 0, -1), wantSkip: false, wantFrom: today.AddDate(0, 0, -1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			from, _, skip := (&Collector{}).Interval(model.Checkpoint{LastExportedAt: tt.lastAt}, now)

			// Assert
			assert.Equal(t, tt.wantSkip, skip)
			if !tt.wantSkip {
				assert.True(t, from.Equal(tt.wantFrom), "from = %v", from)
			}
		})
	}
}
```

Guidelines:
- Case names are lowercase, human-readable, and unique (they become sub-test IDs).
- Put a `func(t *testing.T)` field in the struct when cases need bespoke setup or assertions — see
  `TestBatchWriteRawToParsed` in `internal/repository/repository_test.go` for the `verify` pattern.
- A single-behavior test with only one input does not need a table. Don't force it.

## 2. testify: assert vs require vs mock

Use `github.com/stretchr/testify`. Never hand-roll `if got != want { t.Errorf(...) }` in new code.

- **`require`** — use when a failure must stop the test because everything after depends on it:
  setup, error checks, length checks before indexing.
  ```go
  sink, err := repo.NewRawSink(source)
  require.NoError(t, err)          // no point continuing if this failed

  rows := query(t)
  require.Len(t, rows, 1)          // guards rows[0] below
  ```
- **`assert`** — use for the actual value checks that should each be reported independently; the
  test keeps running and collects every mismatch.
  ```go
  got := rows[0]
  assert.Equal(t, "1.2.3.4", got.IP)
  assert.EqualValues(t, 13335, got.ASN)   // EqualValues for cross-int-type compares
  assert.InDelta(t, 0.5, got.BounceRate, 1e-9)  // floats: InDelta, never Equal
  ```

Rule of thumb: **`require` for preconditions, `assert` for the things under test.** A failed
`require` aborts; a failed `assert` continues. For time values compare with `.Equal()`:
`assert.True(t, got.Ts.Equal(want), "ts = %v", got.Ts)`.

### Mocks

Mock collaborators with `github.com/stretchr/testify/mock`. The `RawSink` double already exists as
`collectortest.Sink` — reuse it; add new doubles in a sibling `*test` package the same way.

```go
sink := &collectortest.Sink{}
sink.On("Add", mock.Anything, mock.Anything).Return(nil)
sink.On("Flush").Return(nil)

// ... exercise the collector ...

sink.AssertExpectations(t)          // every On() was satisfied
sink.AssertNotCalled(t, "Add")      // negative assertion for all-or-nothing paths
```

- `mock.Anything` is allowed only for the `ctx` argument. A stable argument is matched exactly:
  `[]string{"docker-compose.yml"}`, `60*time.Second`, a full request struct.
- An unstable argument (an id off the clock, a record carrying `CreatedAt`) is matched with
  `mock.MatchedBy` carrying the comparison logic — never widened to `mock.Anything`.
- Every mock with expectations closes its test with its own `AssertExpectations(t)` line — one per
  mock, no `AssertExpectationsForObjects`. An expectation a case may legitimately skip is marked
  `.Maybe()`.
- Call order is pinned in the expectations themselves: declare the `*Call`s in workflow order and
  chain them with `mock.InOrder(...)` (fan-in via `.NotBefore(a, b)`). NotBefore requires the
  referenced expectation *fully satisfied* — with `.Times(N)` that is barrier semantics. Never
  hand-roll a call journal and read it back post-hoc; a method called twice gets two `.Once()`
  expectations so each firing has its own place in the chain.
- Set expectations you don't always need with `.Maybe()`.
- Assert on captured arguments via helpers on the double (e.g. `sink.Records()`) rather than
  reaching into `mock.Calls` in the test.
- Prefer a real `httptest.Server` when faking an HTTP API boundary (see the collector tests); use
  `mock.Mock` for internal Go interfaces (sinks, repositories).

## 3. AAA structure (Arrange → Act → Assert)

Every test body follows the three phases, in order, separated by a blank line (and a comment when it
aids scanning). Keep them distinct — no assertions mixed into arrange, no new arrange after the act.

```go
func TestCollectAllStreams(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewWithClient(testClient(t, srv), "greenmaskio", "greenmask", zerolog.Nop())
	sink := &collectortest.Sink{}
	sink.On("Add", mock.Anything, mock.Anything).Return(nil)
	sink.On("Flush").Return(nil)

	// Act
	err := c.Collect(context.Background(), time.Time{}, to, sink)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 4, countByKind(sink, "traffic"))
	sink.AssertExpectations(t)
}
```

In a table-driven test the Arrange lives partly in the table row and partly in the `t.Run` body; the
Act and Assert live in the closure. Keep the same ordering inside the closure.

## 4. Conventions that keep tests clean

- Mark helpers with `t.Helper()` so failures point at the caller (see `writeRaw`, `testClient`).
- Use `t.Setenv` for config/env tests — never mutate `os.Setenv` manually.
- Container tests: guard with a `requireDB(t)` skip and keep them behind `testing.Short()`. A green
  container run also proves the migrations, so keep schema assertions there.
- Silence loggers with `zerolog.Nop()` in tests.
- Name tests per **§5** below; sub-tests describe the case, not the function.
- No sleeps for synchronization; drive time explicitly with fixed `time.Date(...)` values.

## 5. Test function naming

Name every test function after the type and method it exercises:

```
Test{{ObjectName}}_{{methodName}}[_{{caseName}}]
```

- `{{ObjectName}}` — the type under test (`Service`, `Collector`, …); omit it for a package-level
  function.
- `{{methodName}}` — the method or function under test, verbatim (keep its original casing).
- `_{{caseName}}` — an **optional** trailing suffix, used **only when the test is not
  table-driven** and you need to distinguish scenarios that live in separate functions. A
  table-driven test carries its cases in the table, so it stops at the method name.

```go
// Table-driven test for Service.CreateSavepoint — cases live in the table, no suffix:
func TestService_CreateSavepoint(t *testing.T) { ... }

// Non-table tests that split scenarios across functions — add a suffix:
func TestService_Rollback_MissingArchive(t *testing.T) { ... }
func TestService_Rollback_RevertsOnUnhealthy(t *testing.T) { ... }
```
