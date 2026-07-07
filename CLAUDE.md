# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What GM Is

GM is a local environment lifecycle manager for stateful Docker Compose applications. It introduces **savepoints** — consistent snapshots of all registered stateful service volumes — so developers and AI agents can safely iterate on schema migrations, data transformations, and application changes without manually recreating environments.

The two core workflows are:
- **Create savepoint** → stop services → copy volume data → restart services → wait for health checks.
- **Rollback** → stop services → delete volume data → copy savepoint data back → restart services → wait for health checks.

GM can be used as a CLI (`gm savepoint create`, `gm rollback`) or as an MCP server (`gm mcp`) that exposes these operations as native tools for AI agents.

## Commands

```bash
make build            # Compile to bin/gm
make test             # Run all tests
make test-verbose     # Run tests with -v
make lint             # Run golangci-lint
go test ./internal/service/...      # Run tests for a specific package
go run ./cmd/gm -- <args>           # Run without building
```

## Architecture

Four layers — each only imports the layers to its left:

```
model  ←  repository  ←  service  ←  adapter/cli
       ←  client      ↗            ←  adapter/mcpserver
```

| Package | Role |
|---|---|
| `internal/model/` | Shared data types (one per file): domain types (`Mount`, `SkippedMount`, `Savepoint`) plus the JSON response envelopes (`RollbackResponse`, `RemoveResponse`, `RestoredMount`) both adapters marshal |
| `internal/repository/` | Reads/writes `.gm` storage — defines interfaces + YAML/JSON implementations for config, manifest, and snapshot backends |
| `internal/client/compose/` | Parses `docker-compose.yml` to enumerate every service's persistent mounts and the project name |
| `internal/client/docker/` | Wraps `docker compose stop/up -d` and polls health via `docker inspect` |
| `internal/service/` | Business logic; embeds repositories and clients; owns all orchestration |
| `internal/adapter/cli/` | Cobra `RunE` handlers; marshals `model` types to JSON; maps errors to exit codes |
| `internal/adapter/mcpserver/` | `mark3labs/mcp-go` server; registers 4 MCP tools; calls services |
| `internal/cmd/` | Cobra command definitions (flags, `Use`, `Short`); `RunE` delegates to `adapter/cli` |
| `cmd/gm/main.go` | DI root — constructs all repositories, clients, services, and wires adapters |

### Key design decisions

**Interfaces belong to the consumer (dependency inversion).** Each service file declares its own private interfaces for the repositories and clients it needs. Concrete types in `repository/` and `client/` satisfy those interfaces implicitly — no interface definitions live in `repository/` or `client/`.

**Repositories speak the domain model.** A repository's method contract accepts and returns the shared `internal/model` types; how it translates them to its on-disk representation is private to the repository. The manifest repository's `Load`/`Save` deal in `[]model.SavepointRecord`; the `{"savepoints": [...]}` envelope is an unexported `file` struct that never leaves the package. Nothing outside `repository/` depends on a repository-specific persistence type.

**Pluggable snapshot backends.** `SnapshotRef` and `VolumeRef` are shared value types in `internal/model` (`model/volume.go`), so they can flow through the repository contract. Each service that needs snapshotting declares its own `snapshotBackend` interface locally. `SnapshotRef.Opaque` is an opaque string only the creating backend interprets (filesystem path for copy, subvol UUID for btrfs, resource ID for cloud). Service code never inspects it. The MVP implementation is `repository/snapshot/` (package `snapshot`), a Storager-backed tar-copy backend; a second backend would sit beside it (e.g. `repository/snapshotbtrfs/`) or behind a backend seam.

**`VolumeRef` in the manifest record.** `model.SavepointRecord` pairs a `model.Savepoint` (the metadata, and the single-savepoint wire contract) with `[]model.VolumeRef` — one per volume, each carrying its `SnapshotRef`. This is how rollback knows which backend handle to pass to `Restore`.

**All output is JSON.** Every command writes JSON to stdout. Single-savepoint payloads marshal `model.Savepoint` directly — its `json` tags are the wire contract, so no duplicate type or identity mapper. The multi-part response envelopes that have no other domain equivalent (`model.RollbackResponse`, `model.RemoveResponse`, `model.RestoredMount`) live in `internal/model/` alongside the domain types; both CLI and MCP adapters share them. There is no separate `dto` package.

**`.gm/` directory layout.**
- `.gm/config.yaml` — project config (`ProjectConfig`): name, compose files, savepoint dir
- `.gm/manifest.json` — savepoint index; each volume entry stores `service`, `mount_path`, `type`, `source`, and a `snapshot` ref (`backend` name + an opaque handle the backend interprets — for the copy backend, the archive path relative to the project root)
- snapshot data lives under the configured savepoint dir (default `.gm/savepoints/<project-name>/`):
  `<savepoint-id>/<service-name>/<mount-path>/data.tar`, where `<mount-path>` is the container mount path nested verbatim with its leading `/` stripped (so `/export/data` becomes `export/data/data.tar`) — one `data.tar` archive per mount. Nesting the real path keeps the layout collision-free, since the filesystem distinguishes `/export/data` from `/export_data` (unlike flattening `/` to `_`).

**Savepoint IDs** are Unix timestamp integers (e.g. `1780335122`) used as both identifier and snapshot directory name.

**`gm doctor` is strictly read-only.** Uses only `ComposeClient` and `ConfigRepository`; never writes. `gm fix` (future) will consume its output.

**Mount detection captures every service.** GM snapshots the persistent mounts of *all* compose services — there is no image-name allowlist. Named volumes, bind mounts, and anonymous volumes are all captured (keyed by service + container target, so the scheme is uniform across mount types); only tmpfs and other non-persistent mount types are skipped, with a warning.

**`gm rollback <savepoint-id|name|latest>`** restores the environment to a savepoint. Resolution order: `latest` (newest by `CreatedAt`) → exact ID (IDs are unique, so they win) → name. A name matching several savepoints is an *error* listing the candidate IDs, never a silent guess. Flow:

1. Pre-flight: verify every savepoint archive exists; abort untouched if any is missing.
2. `docker compose stop` (containers kept, never `down`, so mounts stay resolvable).
3. **Safety backup** of the *current* live state of every mount to `.gm/tmp/rollback-<unix-ts>/` (same layout/format as a savepoint), so a failed rollback can be undone.
4. For each savepoint mount: **wipe** then **restore**. Wipe runs a throwaway root `busybox` container that mounts the volume/bind path and clears it — the only host-agnostic way to delete data the service wrote as root (`CopyToContainer` only merges, never deletes). Restore extracts `data.tar` via `CopyToContainer` into the *parent* of the mount path (the tar's top entry is the mount basename).
5. `docker compose up -d --wait --wait-timeout <n>` (default `docker.DefaultUpWaitTimeout` = 120s; overridable via `--wait-timeout`). The bound is required: without it an unhealthy `restart: always` container would hang `up --wait` forever.

**Rollback auto-reverts on failure.** If any restore step fails *or* the services come up unhealthy (the `up --wait` bound elapses), GM replays the safety backup to return the environment to its pre-rollback state, restarts services, and returns an error. The backup is deleted on full success and retained (with its path reported) on failure. Restore is destructive — bind mounts (the user's own host folders) are wiped too, by design.

### Error-variable naming

Prefer the plain `err :=` over a bespoke named variable (`upErr`, `revertErr`, …). A distinct name is only worth it when the error outlives the branch that produced it — e.g. it's compared, wrapped later, or coexists with another live `err` in the same scope. When the error is used only inside the `if` that guards it and that branch exits immediately, reuse `err`:

```go
// Prefer this:
if err := dc.Up(ctx, waitTimeout); err != nil {
    return RollbackResult{}, revert(ctx, backup, backupRel, dc, eng, projectName, waitTimeout,
        fmt.Errorf("services did not become healthy after restore: %w", err))
}

// Over an unnecessary alias:
if upErr := dc.Up(ctx, waitTimeout); upErr != nil {
    return RollbackResult{}, revert(ctx, backup, backupRel, dc, eng, projectName, waitTimeout,
        fmt.Errorf("services did not become healthy after restore: %w", upErr))
}
```

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
- Name tests `Test<Unit><Behavior>`; sub-tests describe the case, not the function.
- No sleeps for synchronization; drive time explicitly with fixed `time.Date(...)` values.
