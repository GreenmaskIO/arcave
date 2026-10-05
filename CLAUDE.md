# CLAUDE.md

Doctrine for anyone — human or agent — changing this code: what arcave is, how it is cut, and which invariants the compiler will not catch for you. It is reference, not history: every name below exists, and every claim is checkable by `grep`, `go list` or a build. When you change the architecture, change this file in the same commit — a doctrine that lags the code is worse than none, because it is believed.

Verified against the working tree of 2026-10-04, the `feat/docker-compose-assembler` branch.

**`core` names one compose type.** `projectAssembler` returns a `*types.Project` and `composeRunner` takes one; `core` reads no field of it — it is an opaque token passed from the assembler to the runner. That is the one compose-go name below `cmd/arcave`, and it does not weaken the layer rule: the five checks below still hold, and `core` still knows no client.

`internal/service/introspect` and `internal/service/composespec` are **real**: nothing in arcave reads a compose file any more, and neither stands in for something else. How complete the generated spec is — what the daemon reports and the spec still drops — is the open epic; the row is in the knowledge base's `BACKLOG.md`.

**A savepoint is a spec plus data, and a rollback rebuilds the compose project from the spec it recorded.** `savepoint create` commits the assembled `*types.Project` as `<savepoint-id>/project.yaml`; `rollback` stops the compose project, backs it up, takes it **down**, builds the recorded compose project back with compose's `Create`, restores the volumes into it and starts it. That is why the topology a rollback restores is the one the savepoint recorded and not the one that happens to be running.

**A savepoint holds the writable layer, and a rollback puts it back.** `savepoint create` commits every container's layer through `imagecopy`; a rollback loads those images back into the daemon **before it stops anything**, points each service at its first replica's tag, and rebuilds the compose project on them. A compose service names **one** image while a layer is captured **per replica**, so replicas past the first are rebuilt container by container over what compose just created. The `arcave/` tags a rollback leaves in the daemon are not collected yet — the knowledge base's `BACKLOG.md` carries that.

**A savepoint holds the upstream image, apart from the layer.** The upstream is the image the compose file named (`postgres:16`), as opposed to an arcave layer over it. `savepoint create` records one `UpstreamImage` per distinct upstream in `Savepoint.UpstreamImages` and stores its bytes under `shared/oci/images/sha256/<hex>/` through the same deduplicating layout, so one upstream is written once for every savepoint and replica naming it. It is captured in its own loop, not as part of the layer's, because the layer is to become optional while the upstream is not: it is what a container will be rebuilt from without its layer — a restore not built yet. An upstream neither the store nor the daemon holds, or one the daemon fails to hand over, is recorded by identity with `Stored: false` and a warning; it never fails the savepoint. `shared/oci/images/` has no record to commit it, so "already stored" means a manifest that decodes, names the image and lists members; a torn one is written again.

**A layer is deduplicated, always.** `savepoint create` splits each committed image: every member of the save archive goes into `shared/oci/blobs/sha256/` under the digest its own name already carries, and only the archive's metadata and a manifest stay with the savepoint, so the base layer every savepoint carries is written once. A restore re-tars those members byte for byte, in the recorded order — an image's id is the digest of bytes any reassembly would rewrite. That needs a daemon whose `docker save` writes an OCI layout, **Docker 25 or newer**; an older one is refused at the first capture rather than quietly served worse. `internal/service/backend/imagecopy/fullcopy` stores an archive whole and is the other half of a strategy-selection feature that is not built: it compiles, it is tested, and nothing wires it. Deduplicated blobs are never collected — there is no gc, and `savepoint remove` deliberately stays a single `DeleteAll`.

## What arcave is

arcave is a CLI utility that manages **savepoints** — consistent snapshots of every stateful volume of a docker compose project — and **rollback** to one. One binary, `arcave`; the workflows stop the compose project, move the volumes' bytes through the Docker API, and start the compose project again. Config lives in the project's `.arcave/config.yaml`.

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
| `internal/model` | The base. Domain types (`Volume`, `Savepoint`, `SnapshotStat`, the introspection result), the segment-gate types (`SavepointID`, `SessionID`, `ArcaveProjectName`) and `DockerComposeProjectName`, the backend seam (`BackendSpec`, `VolumeSnapshotBackend`, `SnapshotBackendFactory`, `SnapshotBackendRegistry`, the three `Backend…Request`s), the request/response types the adapters share, the image types (`ImageDigestID`, `ContainerRef`, `ImageCaptureRequest`, `ImageCapture`, `UpstreamImage`, `UpstreamImageCaptureRequest`, `ImageRestoreRequest`, `ImageRestore`), the inflight marker (`InflightMarker`, its phases, `InterruptedRollbackError`), and the sentinels of `errors.go`. `BaseImageIDLabel` and `BaseImageRefLabel` — the labels a commit stamps with the upstream the layer was taken over. **Imports no arcave package** — it does import the Docker SDK, for the raw `container.InspectResponse`/`network.Inspect` and the `image.InspectResponse`/`volume.Volume`/`system.Info` indexes an introspection result carries, and that stays contained: what a savepoint record holds is `model.Volume`, which names neither |
| `internal/config` | The config value type and `Unmarshal` — unknown-key rejection, `ApplyDefaults`, `Validate`. A data package: no clients, no SDKs |
| `internal/repository/config` | `.arcave/config.yaml`, through `os` — it is bootstrap, so it cannot be read through a store configured from it |
| `internal/repository/savepoint` | One record per savepoint, through a `storages.Storager` |
| `internal/repository/inflight` | `.arcave/inflight.json` — the interrupted-rollback marker, through `os`: host-local state, beside the lock |
| `internal/storefactory` | The one place a configured path becomes a store: `New(cfg, projectRoot, arcaveProject, session, composeProject)` returns a `storages.Storager` rooted at `<path>/arcave/projects/<arcave-project>/sessions/<session>/compose/<compose-project>` |
| `internal/service/core` | The business logic every adapter calls. `core.Service` (built from `core.Deps`) runs the savepoint/rollback/remove workflows; `core.InitService` runs `arcave init`; `registry.go` builds one backend per volume and `images.go` carries the layers both ways — commit per container, and load, retarget and per-replica rebuild on the way back — and keeps the upstreams; `upstream.go` resolves each container's upstream |
| `internal/service/backend/fullcopy` | The wholesale backend: one `data.tar` per volume per snapshot, streamed through the Docker engine. Its consumer-owned seam is `fullcopy.VolumeStreamer` |
| `internal/service/backend/imagecopy` | The vocabulary two image backends stand on, and nothing else: the `ContainerImager` seam — which addresses an image by `model.ImageDigestID`, so what a capture stores and a restore looks up is content and never a name the daemon would resolve — `imageStorage` — the key both must agree on or a savepoint becomes unfindable, and the gate a capture and a restore alike address it through — `upstreamImageStorage`, the content-addressed key an upstream image is kept under, `Copier.CaptureUpstream` that keeps it — the `Manifest` an artifact describes itself with, and `ArchiveLayout`, the seam the part each backend does differently sits behind. Its subpackages are the backends and know nothing of each other: `fullcopy.Layout` stores the save archive whole; `dedupcopy.Layout` splits it between the shared content-addressed blob store and the capture's metadata, refusing an archive whose member names carry no digest. Built at the DI root, not through the registry |
| `internal/service/backend/restic/cdc` | Content-defined chunking over stock `restic` containers: one repository per volume, only changed chunks written. Seams: `cdc.VolumeRunner` (the engine half) and the `Dependencies` struct carrying `RepoURLBase` — a value, not a live server |
| `internal/service/backend/restic/rest` | The restic REST protocol over a `storages.Storager` — echo plus generated routes (`api/`), `rest.NewEcho`/`rest.Serve` at the top. See its `README.md`; the only package of that tree naming echo |
| `internal/service/introspect` | Reads the *live* compose project in **one pass**, by the labels compose stamps on what it creates: services, their mounts — options and a bind's host path read from `HostConfig.Binds`/`Mounts`, which `.Mounts` drops or, on Docker Desktop, rewrites — the containers behind them, the project's volumes and networks, plus the daemon's answers kept raw — an index of images (what every replica runs on, and the base `BaseImageIDLabel` names), an index of volumes (owned, and every one any replica mounts), and `docker info`. Decides nothing. Its seam is `dockerAPI`; it reads no file |
| `internal/service/composespec` | Builds the `*types.Project` compose acts on out of an introspection result, and moves one through a savepoint's artifact: `Assemble` reads each service's first replica's `Container.Inspect` into a create-capable spec, `Marshal` renders it as a compose document, `Parse` loads one back through compose's own loader. Pure computation, reads no file |
| `internal/service/lock` | The non-blocking flock on `.arcave/lock`, bound to its project root |
| `internal/service/printer/json` | The production printer: indented JSON to stdout |
| `internal/client/compose` | `Client` runs the compose project's lifecycle via `compose/v2` as a library — `Stop`, `Down`, `Create`, `Start` — taking the `*types.Project` the workflow handed it, and stamping compose's own labels on the way into `Create`, which the library leaves to its CLI; `progress.go` re-emits compose's progress as log records. **Imports no arcave package** |
| `internal/client/docker` | `Engine`: volume I/O and the one-shot container mechanism (`RunOneShot`, `ResolveMountSource`, `OpenVolume`, `WriteVolume`, `WipeVolume`, `RemoveVolume`), the image half (`CommitContainer` — with a comment and the upstream labels as `LABEL` changes — `SaveImage`/`RemoveImage` out, `LoadImage`/`TagImage`/`ImageExists` back), and `RecreateWithImage` — one built container rebuilt from another image, `container.go`. Speaks the Docker API; shells out to nothing |
| `internal/cli` | The adapters, one handler per command, each declaring its own `<command>Service` seam and printing through the injected `printer`. `mcp.go` is the second adapter in the same package; it writes nothing to stdout — that is its JSON-RPC channel |
| `internal/utils` | Logging setup and its defaults, the pointer helpers (`New`, `Value`, `Must`) and `CountingReader` — the counter every backend that hands a stream to `PutObject` reports its size through. A leaf |
| `cmd/arcave` | The composition root. One file per command, named `cmd_<x>.go`; `dependencies.go` wires everything |
| `test/…` | Cross-package integration tests, self-skipping without a reachable docker daemon |

## Layers and import invariants

`model` ← `repository`/`client`/`storefactory` ← `service` ← `cmd/arcave`, with `cli` beside `service`: it declares its own consumer-side service interfaces and meets the services only in `cmd/arcave`, the one package that imports every layer.

**Interfaces belong to the consumer.** Each service file declares the private interfaces it needs (`savepointRepository`, `projectLocker`, `composeIntrospector`, `projectAssembler`, `composeRunner`, `volumeRemover`, `imageBackend`, `containerRebuilder`, `inflightMarkers` in `core`; one `<command>Service` per handler, plus `printer`, in `cli`). Concrete types satisfy them implicitly. The exceptions live in `model` because a factory must name them: `VolumeSnapshotBackend`, `SnapshotBackendFactory`, `SnapshotBackendRegistry` — a backend package *returns* one and `core` *holds* one, so both sides would otherwise import each other. Exported consumer-owned seams (`fullcopy.VolumeStreamer`, `cdc.VolumeRunner`, `imagecopy.ContainerImager`) are constructor parameters, named at the constructor so godoc states the contract.

**A sentinel error that crosses a seam lives in `model`.** `ErrRemoveSelectorConflict`, `ErrRemoveNoSelector`, `ErrRetainRecentNegative`, `ErrProjectLocked`, `ErrInvalidSavepointID`, `ErrRollbackNoSelector`, `ErrInvalidDigestID`, `ErrSavepointNoSpec`, `ErrSavepointImageMissing`, and the typed `InterruptedRollbackError`: a service returns it, an adapter matches with `errors.Is`/`errors.As`, and neither imports the other.

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

**Speak compose's vocabulary, and prefix the two projects.** What compose calls a project is a *compose project* — `model.DockerComposeProjectName`, `composeProject`, `composeProjectStore` — never a "stack", which is Swarm's word. arcave's own `project.name` is the *arcave project* — `model.ArcaveProjectName`, `arcaveProject`. An identifier is never a bare `project` when either could be meant. A step named for a compose operation uses compose's verb: `composeDown`, `composeCreate`, not "tear down" or "build".

**A client is named for its role, never "Client" and never for its transport.** `docker.Engine`, not `docker.Docker`; `introspect.Inspector`, not `introspect.DockerComposeInspector`. `compose.Client` runs the project's lifecycle — it shelled out to the CLI once and calls `compose/v2` as a library now, and the name never went stale across that move.

**Repositories speak the domain model, and a repository is CRUD, never processing.** It reads and writes one thing and owns no client. Use the test: *would this type still make sense if its only collaborator were storage?* If not, it is a service — which is why the capture backends live under `service/`, not `repository/`.

**Prefer plain `err :=` over a bespoke name.** A distinct name is worth it only when the error outlives the branch that produced it — when a revert's own failure must not overwrite the rollback failure still being reported, `revertErr` earns its name. Otherwise reuse `err` and return immediately.

**Return the primary error; log secondary ones.** A failure that happens *while* a primary error is in flight — a best-effort restart, a cleanup, a compensating write — does not change the outcome. Log it and drop it. Never fold a second cause in with `%v`. Cross-error precedence is the **orchestrator's** decision, made where both operations are visible; behaviour that genuinely must reach the caller is a typed error matched with `errors.Is`/`errors.As`, never free text.

**Order declarations headline-first.** Package constants, then the types the file is about, each type's `New…` immediately under it with its methods grouped; then the exported entry point the file exists for; then private helpers **in call order**. `gofmt` does not enforce this.

**A default only one implementation applies belongs to that implementation.** `compose.Client`'s unexported `defaultUpWaitTimeout` is not `model.DefaultUpWaitTimeout`: nothing that cannot import its owner has to name it.

**Doc comments are terse — one to three lines.** This file's prose style is not the code's.

## Storage invariants

**One store, rooted by `storefactory.New`.** `<path>/arcave/projects/<arcave-project>/sessions/<session>/compose/<compose-project>` — the arcave project, the lifetime, and the compose project. The static `arcave` segment is not configurable and is the whole safety story: point the path at `/etc` and arcave operates in `/etc/arcave`; the store library's own key guard closes the other direction. In front of it stand the segment gates — `ProjectName`, `SessionID` (`global` or a canonical UUID), the compose project name, and `SavepointID` (digits only — Unix seconds) — every one an **allowlist**, because a list of what a segment must not be is never provably complete. The compose project name's charset is compose's, not arcave's: `storefactory` accepts a name compose-go's own `loader.NormalizeProjectName` leaves unchanged, so the rule follows compose, and adds only that it is one path segment. `compose/` has a reserved sibling, `sandbox/`, for the second timeline when it is built.

**`shared/` is the one sibling of the savepoints.** `shared/oci/blobs/sha256/` holds the deduplicated image blobs and `shared/oci/images/sha256/<hex>/` each upstream image's metadata and manifest, keyed by the image id; both belong to no savepoint; it cannot be mistaken for one because a `SavepointID` is digits only, and `ListSavepoints` skips every directory that fails that gate. It sits inside the compose project root rather than above it, so the session stays self-contained and dies whole — at the price of no deduplication across sessions, which is also the scope the measured saving was taken over.

**The session is self-contained.** Everything lives inside the compose project root and dies with the session — cdc's restic repositories included, under `<compose project root>/restic/<object-id>` (`cdc.RepoNamespace`), beside the savepoint directories — at the price of a repository that cannot be shared across sessions.

**The record is the commit marker.** `<savepoint-id>/savepoint.json` is a reserved key at the root of that savepoint's own directory, `<savepoint-id>/project.yaml` — the compose spec the savepoint was taken under — is the second, and `<savepoint-id>/images/` — one committed writable layer per container, whose own commit marker is the `image.json` a backend writes **last** — is the third, which cannot collide with a volume's directory because every volume ObjectID opens with `volume-`; a backend writes beneath them (fullcopy: `<object-id>/data.tar`) and never at either key. Create writes the spec, then the record **last**; remove deletes the record **first**. Reverse either and it still compiles — both orders are pinned by call-order tests. Uncommit-first is what makes the sweep safe: `DeleteAll("<id>")` deletes children in whatever order the store reports and could never be relied on to take the marker last.

**On error the record is zero.** `captureVolumes`/`performSavepoint` return a zero `Savepoint` on any failure: an uncommitted capture is garbage no caller may address. What a half-finished fan-out wrote is logged — the one place that knows the id — and left for a later gc; a failure path is the worst moment to delete data. This is also why there is no per-backend `Exists` pre-flight: a record naming a capture already *is* the statement that the capture completed.

**An orphan tree is refused, never swept.** `requireEmptyTree` opens a create by listing the directory the new id names and failing if anything is there. Only an interrupted run can have left it, and those objects are the last copy of whatever that run captured — so they are named, not deleted.

**Storage writes are not atomic, on purpose.** `Storager` has no `Rename`, and the directory backend copies straight into the target file. arcave does not pay for a temp-file commit protocol: the record is the commit, so a savepoint that failed mid-write is invisible, dropped whole and re-created, never repaired.

**The record names its spec.** `Savepoint.Spec` is `{Format}`, and the artifact is read and parsed before anything is stopped — an unloadable one fails while the project is still running rather than halfway through a rebuild. A record carrying no `Spec` at all predates the artifact and a rollback refuses it with `model.ErrSavepointNoSpec`: there is nothing honest to rebuild the compose project from.

**A safety snapshot is an ordinary savepoint.** It commits a record in the same store, marked `model.SafetySnapshotLabel` (`arcave-safety-snapshot`): hidden from `savepoint list` by default (`--include-safety-snapshots` shows it), findable and rollable-back-to by id. It captures exactly what an ordinary savepoint captures, volumes and container layers alike, and that parity is structural: `performSavepoint` takes the whole `workingSet`, so the two call sites cannot drift — only name, description and labels are theirs to differ in. There is no second store and no tier — `BackendSpec` is `{Store, Volume}`, and a bare `store` is the right name in `core` again.

**`.arcave/inflight.json` is the interrupted-rollback marker.** Written before the first destructive write, cleared when the environment is consistent again, and carrying the phase whose remedy differs (`restoring`, `restore-done`, `inconsistent`). A standing marker refuses the next mutating workflow with `model.InterruptedRollbackError` — except a rollback to the marker's own safety snapshot, which is the remedy and passes. The marker is cleared **before** the safety snapshot is dropped, so a failed clear never leaves a marker naming a deleted remedy.

## The rollback flow

`Rollback` holds the project flock for the whole workflow and runs: resolve the record → gate on the inflight marker → build both backend sets (the savepoint's from `record.Stats`, the live set from one introspection of the running compose project), load and parse the savepoint's own spec, **load every recorded writable layer back into the daemon and point each captured service at its first replica's tag**, and assemble the live introspection into a second spec — all ahead of the stop, so nothing that cannot be built stops the project → stop services → safety snapshot of the live state, carrying the live spec as its own artifact → write the inflight marker (`recreating`) → `Down` the live compose project → `Create` the recorded one → **rebuild the replicas past the first on their own layer** → phase the marker to `restoring` → wipe-and-restore every volume → phase to `restore-done` → start services → reap the anonymous volumes the teardown orphaned → clear the marker, drop the safety snapshot.

**The layer load is the pre-flight, and the per-replica rebuild is what compose cannot express.** `ServiceConfig.Image` is one string per service while a layer is captured per replica, so `retargetImages` puts replica 1's tag on the service (with `PullPolicy: never` — the tag lives only in this daemon) and `rebuildDivergentReplicas` inspects the freshly created compose project and hands every other replica to `Engine.RecreateWithImage`. That rebuild stands **between `Create` and the volume restore**: `ResolveMountSource` finds an anonymous volume by (service, target) among the created containers, so the container set has to be final before anything is written into it. The rebuild carries the container's own name, labels and declared endpoints over and takes `RemoveVolumes: true` with it — the anonymous volumes it drops are the empty ones compose made a moment earlier.

**After a rollback a container runs on arcave's own tag, not on the image its compose file named.** A layer is put back the only way docker gives a container a filesystem — by creating it from an image — so `restoredImageRef` tags each loaded layer `arcave/<project>-<service>-<replica>-<base image>:sp-<savepoint-id>` (e.g. `arcave/demo-db-1-postgres-16:sp-1759480000`): compose's `image:` needs a name, and the image exists only in this daemon. `Config.Image` therefore no longer names the upstream, so every commit stamps it forward: `model.BaseImageIDLabel` and `model.BaseImageRefLabel` carry the upstream's id and reference onto the layer, and every container created from it inherits them. `core.capturableContainers` resolves the upstream before the stop, from the introspection alone: the container's own image while `Config.Image` is no `arcave/…:sp-` tag, the layer's labels once it is, and nothing — with a warning — for a layer carrying none. The tag is built from `ImageCapture.UpstreamImageRef`, which is never an arcave tag, so the name cannot nest. Records written by earlier arcave versions are not read back: there is no backward compatibility to keep.

**`compose/v2` as a library does not label what it creates** — `com.docker.compose.project` and its siblings come from `ServiceConfig.CustomLabels`, which the docker CLI fills and the library never does. `compose.Client.Create` fills them instead, on a copy of the project: without them the rebuilt containers belong to no project, and neither compose's own `Start` nor arcave's introspector would ever find them again. It is also what `Project.WorkingDir` and `ComposeFiles` are carried for — two of those labels are made of them, and a compose document has no field for either, so the artifact carries them in an `x-arcave-origin` extension.

The rebuild comes before the restore because the restore needs somewhere to write: an anonymous volume exists only once the container mounting it does, and `Engine.ResolveMountSource` finds the fresh one by (service, target) against the created containers. `Down` never removes volumes — a named one arcave does not capture has no copy anywhere — so the anonymous volumes it orphans are reaped afterwards, and only those a capture was actually put back into.

Any failure past the marker replays the safety snapshot through the very instances that took it: load its layers back, down, create the live compose project again on them, restore, start. The revert runs on `context.WithoutCancel` with its own 10-minute budget (`revertBudget`): the Ctrl-C that aborted the rollback must not tear the revert meant to save the environment. A failed revert marks the phase `inconsistent` and retains the safety snapshot — it is now the last copy of the pre-rollback state.

The `recreating` → `restoring` phase step is the one marker write that is **mandatory**: every other only relaxes the phase, but a marker still reading `recreating` while the restore writes would tell the next command the data is untouched.

The cdc backend's safety capture is an increment in the same per-volume repository (same host, same path — what restic picks a parent by), so the undo costs almost nothing; the label rides along as a restic tag.

## CLI conventions

Every file defining a cobra command is `cmd_<x>.go`, mirroring the command path. Package `main` is flat, so every symbol carries its command's prefix; the command variable is **never** named `cmd` — it would shadow the `cmd *cobra.Command` parameter in `RunE`. Registration lives in that file's `init()`, which is order-independent.

Root uses **`PersistentPreRunE`**, not `PreRunE`: cobra runs `PreRunE` only for the command that executes, so a plain one would never fire for a subcommand.

**The resolved config is package state, not a type.** `cmd_root.go` holds it, `config.go` holds the resolution that fills it. The seam is the `*viper.Viper` **parameter** those functions take, so a test passes a fresh viper per case instead of calling `viper.Reset()`. The global is `projectCfg`, never `cfg` — a bare `cfg` is shadowed silently by any local `cfg :=`.

**Only bound flags feed the config.** `viperBind` is what binds one; transient flags (`--override`, `--dry-run`) stay out. `savepoint create --name` is deliberately unbound: it would collide with `init --name` on the shared `name` key.

**The compose project is named per invocation, the arcave project is config.** `-p/--project-name` (or `ARCAVE_PROJECT_NAME`) is a persistent flag on root, transient and unbound: one project directory may hold several compose projects. `project.name` in the config is the arcave project (`model.ArcaveProjectName`), a different segment of the store root. `withDeps` guards the flag with `requireProjectName`, so no command bound to a compose project can forget it.

A command that touches storage or Docker wraps its body in `withDeps`, which builds `initAllDependencies` for the named compose project and defers its `Close`; half-finished wiring is released newest-first by the `partial` unwind. `arcave init` deliberately does not use it — it names no compose project, opens no storage and contacts no daemon. `arcave mcp` does not either: it serves many compose projects from one process, so it builds one set per tool call through `cli.MCPServiceFactory` and releases it when the call returns. The store root is addressable from the flag alone, so nothing is parsed before it — which is why a broken compose file no longer fails `savepoint list`.

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
