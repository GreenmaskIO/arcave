# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What GM Is

GM is a local environment lifecycle manager for stateful Docker Compose applications. It introduces **savepoints** — consistent snapshots of all registered stateful service volumes — so developers and AI agents can safely iterate on schema migrations, data transformations, and application changes without manually recreating environments.

The two core workflows are:
- **Create savepoint** → stop services → copy volume data → restart services → wait for health checks.
- **Rollback** → stop services → delete volume data → copy savepoint data back → restart services → wait for health checks.

GM is a CLI today (`gm savepoint create`, `gm rollback`). `gm mcp` — an MCP server exposing these
operations as native tools for AI agents — is the design target and **not yet implemented**: the
command exists and returns "not implemented", as does `gm status`. Wherever this file argues that a
decision was made "so `gm mcp` gets it too", it is protecting that seam in advance, not describing a
shipped adapter.

## Commands

```bash
make build            # Compile to bin/gm
make test             # Run all tests
make test-verbose     # Run tests with -v
make lint             # Run golangci-lint
go test ./internal/service/core/... # Run tests for a specific package
go run ./cmd/gm -- <args>           # Run without building
```

## Architecture

Four layers — each only imports the layers to its left:

```
model  ←  repository  ←  service  ←┐
       ←  client      ↗            ├─  cmd/gm
       ←  storages    ↗            │
       ←  lock        ↗            │
                          cli   ←──┘
config (leaf) ← repository, service, cli, cmd/gm
utils  (leaf) ← cmd/gm
```

`cli` is drawn beside `service`, not above it: it declares its own consumer-side service interfaces
and imports **no** `internal/service/*` package. `cmd/gm` is the one place the two meet — it is the
composition root, and the only package that imports every layer.

`model.Storager` is the object-CRUD seam every byte of snapshot data and savepoint
metadata passes through. `service/snapshot` sits *above* it: it is a service-layer
collaborator that happens to be named for what it produces.

`internal/service/` holds no Go files of its own — it is a directory of two packages:
`core/`, the business logic every adapter calls, and `snapshot/`, an implementation of
the `snapshotBackend` seam `core` declares. Adding a third only pays off under the same
test: a seam with more than one mintable implementation, or a cut in the import graph.

| Package | Role |
|---|---|
| `internal/model/` | Shared data types, one per file, each file named for its type: domain types (`Mount`, `SkippedMount`, `Savepoint`, `SavepointRecord`, `VolumeRef`), the storage-key helper (`SnapshotKey`) and id gate (`ValidSavepointID`), the request types (`CreateSavepointRequest`, `RollbackRequest`, `RemoveRequest`), the JSON response envelopes (`RollbackResponse`, `RemoveResponse`, `RestoredMount`) the adapters marshal, and the sentinel errors of `errors.go`. It also owns GM's only two exported interfaces, `model.Storager` (storager.go) and `model.Printer` (printer.go) |
| `internal/repository/` | **CRUD only.** `config/` writes `.gm/config.yaml` through `os`; `savepoint/` reads/writes one record per savepoint through a `model.Storager`. No repository owns a client or streams data |
| `internal/storages/` | `storages.New` — the factory that turns a `config.Storage` union into a `model.Storager` rooted at the store, and the only place a store root is validated |
| `internal/storages/directory/` | The `model.Storager` implementation — local-filesystem object storage, vendored from greenmask. Every key it takes passes through `resolve` |
| `internal/lock/` | `lock.Locker` — a non-blocking flock on `.gm/lock`, bound to its project root. It is the implementation of the `projectLocker` seam `core` declares |
| `internal/service/snapshot/` | `snapshot.CopyBackend` — the MVP snapshot backend. It holds a Docker engine *and* a `Storager` and runs the whole copy/restore pipeline, so it is processing, not CRUD; that is why it lives under `service/` and not `repository/` |
| `internal/service/core/` | Business logic; embeds repositories and clients; owns all orchestration. `core.Service` runs the savepoint/rollback workflows; `core.InitService` runs `gm init` |
| `internal/config/` | The config value type (`config.go`) and `Unmarshal` (`unmarshal.go`) — the single home of the config *policy*: version gate, unknown-key rejection, `ApplyDefaults`, `Validate`. It takes the loaded `*viper.Viper` and decodes it: the `mapstructure` tags on `Config` *are* the schema viper fills, so the decode belongs with the type. A **data package**: it reaches for no client and no SDK, so `gm init` never drags a cloud SDK into its import graph |
| `internal/client/compose/` | `Inspector` — reads the project's *declared* state: parses `docker-compose.yml` via `compose-go` to enumerate every service's persistent mounts and the project name. Touches no daemon |
| `internal/client/docker/` | Acts on the *running* stack. `Engine` moves volume bytes in/out of service containers (Docker SDK); `ComposeRunner` stops and starts services (`docker compose stop/up --wait`, via the CLI). They share the unexported exec seam `newRunner` builds, and both honour the same `docker.Options` — that shared helper, and that shared contract, is why they are one package |
| `internal/cli/` | One handler per command (`ConfigInitHandler`, `SavepointCreateHandler`, …); each calls a service method and renders the result through the injected `model.Printer`. `errors.go` holds the cross-handler `storeError`, which rephrases a store sentinel as a remedy. Knows nothing of cobra or viper |
| `internal/printer/json/` | The production `model.Printer`: indented JSON to stdout |
| `internal/utils/` | Logging setup and the log-level/format defaults `cmd/gm` uses for its global flags. A leaf |
| `cmd/gm/` | The composition root. One file per command — the cobra definition, its flags, and its `RunE`. Resolves config via viper and builds per-command dependencies |

`internal/service/core` and `internal/cli` import **no** `internal/client/*` package, and `internal/cli`
imports no `internal/service/*` package either. Anything they need from a package below them that
carries no behaviour is data, and lives in `model`: `model.Inspection` (what
`compose.Inspector.Inspect` returns) and the sentinel errors of `model/errors.go`. Four checks pin
this, and all four must stay empty:

```bash
go list -deps ./internal/service/core | grep client/    # core knows no client
go list -deps ./internal/cli | grep gm-tools/internal/service
go list -deps ./internal/cli | grep client/
go list -f '{{join .Imports "\n"}}' ./internal/model | grep gm-tools   # model is the base
```

**A sentinel error that crosses a seam lives in `model`.** When an adapter matches a service's error
with `errors.Is`, the error is part of the contract between them — and a contract that lives in the
implementation drags the implementation into every consumer's import graph. `ErrRemoveSelectorConflict`,
`ErrRemoveNoSelector` and `ErrRetainRecentNegative` are therefore `model` vars: `core` returns them,
`cli.removeUsageError` matches them, and neither one imports the other. The message names the domain
condition; the adapter rephrases it in terms of the flags the user typed. They keep their domain names
rather than a `ErrSavepointService…` prefix — the package qualifier already namespaces them, and naming
them for a service would re-couple `model` to the very layer it is meant to sit below.

**A default that only one implementation applies belongs to that implementation.** `docker.DefaultUpWaitTimeout`
is the bound `ComposeRunner.Up` substitutes when the caller passes `<= 0`; it exists because that method
shells out to `docker compose up --wait`, not because the domain says a savepoint has a timeout. It is not
`model.DefaultUpWaitTimeout`: `core` never reads it (it forwards `req.WaitTimeout` verbatim), and the only
other consumer is `cmd/gm`, which is free to import `client/docker` for a flag default. A constant reaches
`model` only when a layer that *cannot* import its owner has to name it.

### The `cmd/gm` command layer

Every file defining a cobra command is named `cmd_<x>.go`, mirroring the command path
(`cmd_savepoint_create.go` → `gm savepoint create`). The prefix sorts all command definitions
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
  unbound: it would collide with `gm init --name` on the shared `name` key.
- **The resolved config is package state, not a type.** `cmd_root.go` holds the five variables every
  command reads — `projectRoot`, `projectCfg`, `configFound`, `configErr` (a config file that exists
  but GM cannot speak, held for `requireInitialized` to raise), and `rootConfigFile` (the `--config`
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
  down before the command ran. `gm init` deliberately does **not** use it: it builds an `InitService`
  via `initServiceDeps` and so never opens storage or contacts the daemon.

### Key design decisions

**Interfaces belong to the consumer (dependency inversion).** Each service file declares its own private interfaces for the repositories and clients it needs (`savepointRepository`, `projectLocker`, `composeInspector`, `composeRunner`, `snapshotBackend` in `service/core/service.go`; `configWriter` in `init.go`). Each `cli` handler declares one for the service method it calls. `service/snapshot` declares `DockerVolume` — the two engine methods a backend needs. Concrete types in `repository/`, `client/`, `lock/` and `service/` satisfy them implicitly — no interface definitions live there.

Exported or not is a separate axis from *where it lives*: `snapshot.DockerVolume` is exported and still consumer-owned, because it is `NewCopyBackend`'s parameter and godoc should name the contract at the constructor.

**Only two interfaces live outside their consumer: `model.Storager` and `model.Printer`.** Both have several consumers *and* a factory that must name them in a signature — `storages.New` **returns** a `Storager`, and `cmd/gm`'s `dependencies` holds a `Printer` field it hands to every handler. A consumer-private interface can be a parameter, but never a return type or a struct field outside its package, so these two contracts live where every side already looks. A third interface reaches `model` only under that same argument, not merely because it has two implementations.

**A client is named for its role, never "Client" and never for its transport.** The package name already qualifies the identifier, so a type never repeats it: `compose.Inspector`, not `compose.Client` or `compose.DockerComposeInspector`; `docker.Engine`, not `docker.Docker`. And the name states the contract, not the mechanism: `ComposeRunner` runs the project's lifecycle — it happens to shell out to the CLI today and could move to the SDK tomorrow without the name going stale. `Client` says nothing at all, and two of them in one binary (`compose.Client` + `docker.ComposeClient`) say it twice. The consumer-side interfaces mirror these: `composeInspector`, `composeRunner` — no `docker` prefix, since the inspector never reaches the daemon.

**Repositories speak the domain model.** A repository's method contract accepts and returns the shared `internal/model` types; how it translates them to its on-disk representation is private to the repository. The savepoint repository's `List`/`Save`/`Delete` deal in `model.SavepointRecord`; the `schema_version` envelope is an unexported `file` struct that never leaves the package. Nothing outside `repository/` depends on a repository-specific persistence type.

**A repository is CRUD, never processing.** It reads and writes one thing, and it owns no client. `repository/snapshot` used to violate this — it held a Docker engine and streamed `OpenMount → PutObject` — so it moved to `service/snapshot`. Use the test: *would this type still make sense if the only collaborator it had were storage?* If not, it is a service.

**The savepoint records live in the store they describe.** `savepoint.New` takes a `model.Storager`, not a project path, and the DI root hands it the **savepoint store** — the very Storager the savepoint backend writes archives into. Records and data therefore travel together: point `storage.directory.path` at S3 and the records go to S3 with the archives. Absence is decided via `Storager.Exists`, never by matching `os.ErrNotExist` — that error is the directory backend's, and an object store would report a missing key differently. Every method takes a `ctx`, because reaching the store may cross a network.

`repository/config` is the deliberate exception and stays on `os`: `config.yaml` is *bootstrap*, the thing that tells GM where the savepoint store is, so it cannot be read through a Storager configured from it. `gm init` opens no storage at all, and should keep that property.

**One record per savepoint, and the record is the commit marker.** `<id>/savepoint.json` is a *reserved key at the root of the savepoint's own directory* — that directory belongs to the savepoint layer, and the backend writes archives beneath it (`<service>/<mount>/data.tar`, always ≥2 segments deep) but never at the reserved key. This replaced a monolithic `manifest.json`, for two reasons that are not the same reason:

- **Blast radius.** `PutObject` is deliberately non-atomic, so a torn write to a single index destroyed the *whole* savepoint index, not the one savepoint being written.
- **The race.** `CreateSavepoint` loaded the index, stopped the services, fanned out over every volume, and only then saved `append(records, record)` — silently clobbering anything written in that multi-minute window. With one record per savepoint there is nothing to append to, and the window is gone.

**The ordering is the crash-safety story, and nothing in the type system enforces it.** Create: `savepointBackend.Remove(ns)` (wipe an orphan tree a crashed run left under this id, which `List` cannot see and `uniqueSavepointID` therefore cannot avoid) → write every archive → `savepointRepo.Save` **last**. Remove: `savepointRepo.Delete` **first** → `savepointBackend.Remove(ns)`.

Uncommit-first *inverts* what a monolith wanted (data first, "so the index never points at data that is gone") — that was safe only because rewriting the whole index was a single swap. With a per-savepoint marker, data-first leaves a savepoint that still lists and still resolves but whose archives are half gone. Uncommit-first also is what lets the record live *inside* the savepoint directory: `Remove` is `os.RemoveAll("<id>")`, which deletes children in readdir order and could never be relied on to take the marker last — but by then step 1 already deleted it, as its own operation on its own object. **Reversing either pair compiles clean**, so both orders are pinned by a call-order unit test. The cost of uncommit-first is an orphaned directory when the data delete fails; it is logged and reported, and a future `gm savepoint gc` sweeps directories with no record.

**A legacy store is refused, never silently skipped.** Every namespace of a pre-record store holds archives but no `savepoint.json`, so `List` would classify each as *incomplete* and skip it — reporting "no savepoints" to a user whose data is sitting on disk. That reads as data loss. `List` therefore checks `Exists(ctx, "manifest.json")` at the store root and returns `model.ErrLegacyManifest` **before** it skips anything.

**Storage writes are not atomic, on purpose.** `Storager` has no `Rename`, and the directory backend copies straight into the target file, so an interrupted `PutObject` leaves a partial object. GM does not pay for a temp-file commit protocol to avoid that: the *record* is the commit, so a savepoint that fails mid-write is invisible, dropped by namespace (`Remove`) and re-created, never repaired. Don't add atomicity here without a failure this actually costs us.

**No key may name the store root, or anything outside it.** `directory.Storage.resolve` is the single gate every object-addressing method routes through: it rejects `""` and `"."` (they resolve to the store root, so a `DeleteAll` would wipe the store rather than one object in it), an absolute key, and any key that climbs out via `..`. Its sentinel `directory.ErrUnsafeKey` stays package-local — nothing `errors.Is`-matches it across a seam, so it does not belong in `model`. This is why `removeSafetyBackup` no longer prunes its own store with `DeleteAll(".")`: an empty `.gm/tmp` costs nothing, and the one caller that wanted `"."` was the one thing the gate had to ban.

Three more gates stand in front of it, each at the layer that owns the value: `model.ValidSavepointID` (digits only — a savepoint id is Unix seconds), which `snapshot.CopyBackend.Create`/`Remove` call before rendering the id into a storage key; the savepoint repository validating before it mints a key; and `storages.validateStoreRoot` refusing a store root of `/`, a shallow system directory, `$HOME`, or an ancestor of the project.

**Three addressing levels, each owned by exactly one layer, and no layer names another's mechanism.** The *project* is the DI root's concern: it is a `model.Storager` rooted at the store, never a field in a request. The *savepoint* is `service/core`'s: a plain `savepointID string` (Unix seconds), which doubles as the storage directory. The *volume* is `model.Mount{Service, Target, ...}`. There is no `Namespace` type and no `NamespaceKind`: a savepoint and a rollback's safety backup live in **separate stores** (`cfg.Storage` vs `.gm/tmp`), so nothing needs a `rollback-` prefix to tell them apart within one store — the id alone addresses either. Killing that prefix removed the one place `String()` branched on a kind.

**`ComposeProject`, never `ProjectName`.** The name `docker compose` knows the stack by (used to locate containers) and the project as a *storage tenant* are different concepts, and conflating them is exactly what the old field names invited. The backend calls therefore keep them apart: a `savepointID` says where the data lands (the storage directory), `composeProject` says which stack, and each `model.Mount` says which mount.

**The backend is savepoint-scoped, and owns the per-mount fan-out.** Every method operates on a whole savepoint's worth of volumes, because that is how every caller uses it — so the errgroup that snapshots/restores each mount concurrently lives *once* inside the backend, not copied into four service methods. `internal/service/core/service.go` declares the consumer-side `snapshotBackend` interface: `Snapshot(ctx, savepointID, composeProject, []Mount) ([]int64, error)` (bytes stored per mount, in order), `Restore(ctx, savepointID, composeProject, []Mount)`, `Missing(ctx, savepointID, []Mount) ([]Mount, error)` (pre-flight: which archives are absent), `Remove(ctx, savepointID)`. `Remove` is savepoint-scoped for a *different* reason than the rest — it must drop a directory whose half-written archives no record names yet — but keeping the whole interface at that one level is what removed the granularity mismatch (three per-mount methods next to one per-savepoint `Remove`).

**One explicit snapshot backend, addressed by a shared key — no opaque handle.** The storage layout is not private to the backend: `model.SnapshotKey(savepointID, service, mountPath)` (`model/snapshot_key.go`) is the single owner of the on-disk key, `"<savepointID>/<service>/<mount-path>/data.tar"`, computed identically on the write and read sides. So the record stores no `{backend, opaque}` handle — a `VolumeRef` is just `{service, mount_path, type, source, size_bytes}` and the archive is found by recomputing the key. The backend **owns the whole read→write pipeline end to end** — it holds the Docker engine and the Storager of the store it writes into, so given a savepoint id and its mounts it opens each source and writes storage, and on restore reads storage and writes each mount. The one implementation is `service/snapshot/` (`snapshot.CopyBackend`), storing each mount's tar stream verbatim under the `SnapshotKey` and counting the bytes it streams. (This deliberately traded the earlier pluggable-backend seam — a `SnapshotRef.Opaque` a btrfs/CAS/cloud backend would reinterpret — for a layout you can read straight off the call.)

**No path ever crosses the backend seam, but the layout is legible.** A backend is bound to its store at construction (`NewCopyBackend(engine, store)`, where `store` is already rooted by `storages.New`), so the calls mention only a `savepointID` and `[]Mount`, never a directory. `Snapshot` and `Remove` validate the id (`model.ValidSavepointID`) before rendering it into a key, because they are the write/destructive paths.

**The backends are injected at the DI root; the engine's lifecycle stays there.** The Docker engine is created once per command in `cmd/gm/dependencies.go` (`docker.NewEngine(opts)` only builds the SDK client — API-version negotiation is deferred to the first call, so docker-free commands like `savepoint list` create it without contacting the daemon) and closed by `(*dependencies).Close`, which `withDeps` defers. `initAllDependencies` builds each store through `storages.New` and builds **two** backends over the shared engine — `savepointBackend` over `cfg.Storage`, `safetyBackupBackend` over `.gm/tmp` — and hands both to `core.New`. There is no per-request backend factory and no `Close` on a backend or on the service.

**`storages.New` is the only place a configured path becomes a store.** It resolves a relative path against the project root, validates the result (`validateStoreRoot`), creates it, and returns a Storager **already rooted at the store** — which is why `filepath.IsAbs` no longer appears in `dependencies.go`, an `IsAbs` check no object store could honour. It is also where `directory.NewStorage`'s stat-and-fail is reconciled with `SubStorage`'s never-stat, by `MkdirAll`-ing first.

**`internal/config` is a data package: no clients, no SDKs.** Its pluggable sections declare their own plain union arms rather than embedding `directory.Config` or a future `s3.Config` — embedding would drag the AWS SDK into the import graph of `repository/config`, and therefore of `gm init`. `cmd/gm` translates each section into its package's own options struct (`dockerOptions`, `storages.New`). `storage` is a tagged union: `Type` names the arm, `Validate()` requires that arm to be present, and the arms are pointers so an unset one is omitted on marshal.

**The config *policy* is `config.Unmarshal`'s; the *sources* are `cmd/gm`'s.** `resolveConfig` owns only the merge: reading the file and letting viper layer flag > env > file > default. It then hands the loaded `*viper.Viper` to `config.Unmarshal`, which decodes and judges it. The decode lives with the type it fills — the `mapstructure` tags on `Config` are its schema, and re-implementing that decode over an `AllSettings()` map one layer up would only be viper's own `Unmarshal` spelled by hand. Everything a *value* means therefore lives in `config`: a test feeds it a viper built from a yaml literal.

**GM speaks exactly one config version, and unknown keys are an error.** The real decode passes `rejectUnknownKeys` — a `viper.DecoderConfigOption` setting `mapstructure`'s `ErrorUnused` — so a key no field claims fails it. But that decode runs **only after the version gate**, and the gate reads the version through a **probe struct carrying nothing but `Version`**, never through `Config`: decoding the whole `Config` leniently just to read `version` would fail on a *type*, not a version, the day a schema GM does not speak spells `storage` as a string. The probe cannot — it claims one key and ignores the rest.

The probe is **seeded with the current `Version`**, so a source naming no version at all reads as current. That is what lets a fresh project — no config file, resolved from flags and defaults alone — through the gate; `gm init` stamps the version when it writes. Only a version that is *present and different* (`version: 0`, `version: 2`) is stale, and `model.ErrUnsupportedConfigVersion` reports it. GM never migrates such a file in place: that would clobber a `savepoint_dir` the user set deliberately.

The consequence is that the pre-v1 flat layout is **not** caught by the version gate — it carries no `version`, so it reaches the strict decode and is rejected by the keys it uses (`invalid keys: name, savepoint_dir`). Naming the foreign keys is the diagnosis; there is no second supported version for the gate to route it to.

**Both halves of `client/docker` honour the same `docker.Options`.** `Engine` reaches the daemon through the SDK; `ComposeRunner` and `wipeMount` (the package-level helper `Engine.WriteMount` calls to clear a mount before extracting into it) shell out to the `docker` CLI. If only the SDK obeyed a configured host, the two would silently target *different daemons* — a savepoint exported from one and restored into the other. So `newRunner(opts)` appends `DOCKER_HOST`/`DOCKER_CONTEXT`/`DOCKER_TLS_VERIFY`/`DOCKER_CERT_PATH` to `os.Environ()` (only when set, so an unset option never clobbers an inherited one with `""`). `docker.context` is **refused without `docker.host`**: resolving a CLI context needs `docker/cli`'s context store, which this package does not depend on, so the SDK client cannot honour it and the halves would diverge.

**The project lock belongs to the workflow, not to the command layer.** "A savepoint is never created while a rollback is running" is a property of the workflow, so `core.Service` claims the lock itself: `CreateSavepoint`, `Rollback`, and a non-dry-run `RemoveSavepoints` each open with `s.lockProject(ctx)` and `defer unlock()`. `ListSavepoints` and `--dry-run` take no lock, so a read never blocks on a running rollback.

A lock taken in `cmd/gm` (a `withLockedDeps` helper) would have been **invisible to `gm mcp`**, which calls `core.Service` directly — every agent-driven rollback would have run unserialised. The same argument rules out `internal/cli`. The lock is taken **first**, before the store is read and before `docker compose stop`: a caller that cannot have the project must not touch it.

The seam is `core.projectLocker` — `Acquire(ctx) (release func() error, err error)` — and `lock.Locker` is bound to its project root at construction, exactly as a snapshot backend is bound to its store, so no path crosses it. `Acquire` takes a `ctx` it does not use today, because the seam has to survive a locker that reaches a network: a lease in the shared store is the only kind of lock that could serialise two *hosts*. The flock cannot: it is **host-local** and does not protect a store shared between machines.

The sentinel is **`model.ErrProjectLocked`, not `lock.ErrHeld`** — `core` returns it and must not import `internal/lock`, so the contract lives in `model` with `ErrRemoveSelectorConflict` and friends. Releasing is best-effort: `lockProject` returns an `unlock func()` that logs a release failure rather than handing the caller a second error to reconcile with the one it is already returning.

**Neither backend is called `backend`.** They share a type and a method set, and `rollback.go` calls `Restore` on each within twenty lines of the other (`applySavepoint` restores the savepoint, `revert` restores the safety backup); swapping them would restore the wrong data and compile clean. So the *names* carry the distinction the type system cannot: `savepointBackend` and `safetyBackupBackend`. Never reintroduce a bare `backend`, and never let one name be a prefix of the other.

**The clock is a field, not a constructor argument.** `core.New` takes its collaborators positionally and hardcodes `now: time.Now` — production never wants anything else, so it is not worth a parameter. `now func() time.Time` is a func rather than an interface (idiomatic Go for a single method), and a test, living in the same package, overrides the field directly: `svc.now = func() time.Time { return fixedNow }`. That is what makes `--retain-for` and the safety backup's id (its `.gm/tmp/<ts>` directory) deterministic. A test that exercises one method passes `nil` for the dependencies that method never touches — `New(repo, freeLocker(), nil, nil, savepointBackend, nil)` for `RemoveSavepoints`, which reads records, takes the lock, and drops directories, but never inspects or runs compose.

**`gm init` is its own service.** `core.InitService` (`service/core/init.go`) holds only the project root, a `configWriter`, and a `dockerComposeInspector` — it shares nothing else with the savepoint workflows, so `gm init` runs without storage, a snapshot backend, or the Docker engine. Splitting further was rejected: `Rollback`'s dependencies are a strict superset of `Create`/`List`/`Remove`'s, so separating those would only duplicate five wiring fields.

**`Init` takes the `config.Config` it persists, not a request type.** It is the one service method whose input *is* a config — verifying the compose project and writing the file is the whole of it — so a `model.InitRequest` mirroring a subset of the sections could only ever be a lossy copy: it dropped `docker` and `snapshot`, which meant `gm init --override` silently erased a configured `docker.host` from the file it rewrote. And it would have had to re-flatten the `storage` union into request fields (`SavepointDir` today, `S3Bucket`/`S3Prefix` tomorrow) — naming one arm's mechanism at a layer that must not know the arms, the same smell the snapshot backend avoids by taking a plain `Mount` and a `savepointID` rather than a path. `override` stays an argument rather than a field: it steers the run and is never persisted. `Init` creates no savepoint directory, because there is nothing to create — the directory is a `storage` section here and nothing more, and `storages.New` brings it into existence at the first savepoint.

`ApplyDefaults` therefore has to be safe on a value: `Config` reaches `Init` by copy, and the storage arm is a **pointer**, so filling `Directory.Path` through it would reach across that copy and mutate the caller's own config. It fills a copy of the arm and re-points at it.

**Safety backup is a service method, not a separate package.** A safety backup is just a snapshot under its own id in the safety-backup store (`.gm/tmp`, a store distinct from the savepoint store — which is why the id needs no `rollback-` prefix to stay clear of a savepoint of the same second), so the rollback flow calls `s.safetyBackupBackend.Create`/`Restore`/`Remove` directly (see `s.createSafetyBackup`, `s.revert`, `s.removeSafetyBackup` in `rollback.go`) rather than through a dedicated repository. `removeSafetyBackup` deletes the id's directory and stops there: pruning the now-empty `.gm/tmp` would mean asking a Storager to delete its own root, which is the one key `directory.resolve` refuses. The service holds no Storager at all — every byte it moves goes through a backend.

**One backup concept, one name: `safetyBackup`.** `rollback.go` takes exactly one kind of backup, and every symbol in the flow spells it out: `createSafetyBackup`, `removeSafetyBackup`, the `safetyBackupID` variable, and the `safety_backup_id` log field. `createSafetyBackup` returns just that id and `revert` re-addresses each mount from it — since the storage key is recomputed from `SnapshotKey(safetyBackupID, service, mountPath)`, there is no per-mount handle to carry, so the old `safetyBackupMount` pair is gone. A bare `backup…` name is banned here *precisely because* nothing else in the file is a backup — the reader cannot know that, so `backupID` sitting next to `safetyBackup` reads as two different things. Same rule as the `savepointBackend`/`safetyBackupBackend` pair above, in the other direction: when the type system cannot separate two concepts the names must, and when there is only *one* concept the names must not imply two.

**`VolumeRef` in the savepoint record.** `model.SavepointRecord` pairs a `model.Savepoint` (the metadata, and the single-savepoint wire contract) with `[]model.VolumeRef` — one per volume, each `{service, mount_path, type, source, size_bytes}`. There is no stored archive handle: rollback recomputes the location with `model.SnapshotKey(record.ID, v.Service, v.MountPath)` and passes `{record.ID, mount}` to `Restore`.

**All output is JSON.** Every command writes JSON to stdout. Single-savepoint payloads marshal `model.Savepoint` directly — its `json` tags are the wire contract, so no duplicate type or identity mapper. The multi-part response envelopes that have no other domain equivalent (`model.RollbackResponse`, `model.RemoveResponse`, `model.RestoredMount`) live in `internal/model/` alongside the domain types; both CLI and MCP adapters share them. There is no separate `dto` package.

**`.gm/` directory layout.**
- `.gm/config.yaml` — project config (`config.Config`): `version: 1` plus one section per subsystem (`project`, `docker`, `storage`, `snapshot`, `log`). The one file read through `os` rather than a Storager, because it says where the Storager points
- `.gm/lock` — the flock target `core.Service` holds for the duration of a mutating workflow. Created empty and never unlinked (flock guards the inode, not the name)
- snapshot data lives under the configured savepoint dir (`cfg.Storage.Directory.Path`, default `.gm/savepoints/<project-name>/`), one self-contained directory per savepoint:
  - `<savepoint-id>/savepoint.json` — the record, a **reserved key** at the directory root, owned by `repository/savepoint`. Written last on create, deleted first on remove. Holds `schema_version` (currently `2`) plus the `model.SavepointRecord`; each volume entry stores `service`, `mount_path`, `type`, `source`, and `size_bytes` — no archive handle, since the location is `model.SnapshotKey(<savepoint-id>, service, mount_path)`
  - `<savepoint-id>/<service-name>/<mount-path>/data.tar` — the archive `model.SnapshotKey` names, where `<mount-path>` is the container mount path nested verbatim with its leading `/` stripped (so `/export/data` becomes `export/data/data.tar`) — one `data.tar` archive per mount. Nesting the real path keeps the layout collision-free, since the filesystem distinguishes `/export/data` from `/export_data` (unlike flattening `/` to `_`).
  - a directory with **no** `savepoint.json` is an incomplete savepoint: invisible to `list`, safe to GC
- `.gm/tmp/<ts>/…` — a rollback's safety backup, same layout, in its own store (so no `rollback-`
  prefix is needed to tell it from a savepoint). It carries no record: a backup is addressed by its
  id alone. Created and deleted within one `gm rollback`; the now-empty parent is left in place.

**Savepoint IDs** are Unix timestamp integers (e.g. `1780335122`) used as both identifier and storage directory. `model.ValidSavepointID` enforces "a non-empty run of digits" — that is the first path-safety gate, and it is why a hand-planted `"id": ""` or `"../.."` can never reach a syscall.

**The unimplemented commands are registered stubs, not absent.** `gm status` and `gm mcp` exist in `cmd/gm` and return `not implemented`; they carry no flags, no handler, and no dependencies yet. Registering them early keeps the command tree honest about what GM intends to be — but do not cite them as existing behaviour. Neither `gm doctor` nor `gm fix` exists in any form; when `doctor` lands it must be strictly read-only, using only `compose.Inspector` and the config repository.

**Mount detection captures every service.** GM snapshots the persistent mounts of *all* compose services — there is no image-name allowlist. Named volumes, bind mounts, and anonymous volumes are all captured (keyed by service + container target, so the scheme is uniform across mount types); only tmpfs and other non-persistent mount types are skipped, with a warning.

**`gm rollback <savepoint-id|name|latest>`** restores the environment to a savepoint. Resolution order: `latest` (newest by `CreatedAt`) → exact ID (IDs are unique, so they win) → name. A name matching several savepoints is an *error* listing the candidate IDs, never a silent guess. Flow:

0. Acquire the project lock (`.gm/lock`); a second concurrent `gm` fails fast.
1. Pre-flight: verify every savepoint archive exists; abort untouched if any is missing.
2. `docker compose stop` (containers kept, never `down`, so mounts stay resolvable).
3. **Safety backup** of the *current* live state of every mount to `.gm/tmp/<unix-ts>/` (same layout/format as a savepoint), so a failed rollback can be undone.
4. For each savepoint mount: **wipe** then **restore**. Wipe runs a throwaway root `busybox` container that mounts the volume/bind path and clears it — the only host-agnostic way to delete data the service wrote as root (`CopyToContainer` only merges, never deletes). Restore extracts `data.tar` via `CopyToContainer` into the *parent* of the mount path (the tar's top entry is the mount basename).
5. `docker compose up -d --wait --wait-timeout <n>` (default `docker.DefaultUpWaitTimeout` = 120s, applied by the compose runner when the request carries none; overridable via `--wait-timeout`, which `savepoint create` carries too). The bound is required: without it an unhealthy `restart: always` container would hang `up --wait` forever.

**Rollback auto-reverts on failure.** If any restore step fails *or* the services come up unhealthy (the `up --wait` bound elapses), GM replays the safety backup to return the environment to its pre-rollback state, restarts services, and returns an error. The backup is deleted on full success and retained (with its id reported) on failure. Restore is destructive — bind mounts (the user's own host folders) are wiped too, by design.

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
if err := s.applySavepoint(ctx, record, req.ComposeFiles, composeProject, req.WaitTimeout); err != nil {
    revertErr := s.revert(ctx, safetyBackup, req.ComposeFiles, composeProject, req.WaitTimeout)
    if revertErr != nil {
        log.Error().Err(revertErr).Str("safety_backup_id", safetyBackupID).
            Msg("revert failed; environment left in an inconsistent state, safety backup retained")
    }
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
// snapshot error is primary, so the failure path restarts best-effort, logs what
// went wrong there, and returns the snapshot error; the success path returns the
// restart's own error. The orchestrator, not a helper, decides precedence:
if err := s.snapshotWhileStopped(ctx, insp, record); err != nil {
    log.Info().Msg("starting services")
    if err := s.composeRunner.Up(ctx, req.WaitTimeout, req.ComposeFiles...); err != nil {
        log.Error().Err(err).Msg("services failed to restart after snapshot failure")
    }
    return model.Savepoint{}, fmt.Errorf("snapshotting mounts: %w", err)
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
   whole file is bounded by leads it (`maxConcurrentVolumeOperations` in `service/snapshot/snapshot.go`);
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
`resolveSavepoint`, `preflightArchives`, `createSafetyBackup`, `abortBeforeRestore`,
`applySavepoint`, `revert`, `removeSafetyBackup`, and the projection helpers `restoredMounts` /
`mountsOf` — the exact order the entry point reaches them. `gofmt` does not enforce this, so it is
on you.

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
