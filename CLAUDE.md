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
| `internal/model/` | Shared data types: `Savepoint`, `StatefulService`, `Volume`, `doctor.Report` |
| `internal/repository/` | Reads/writes `.gm` storage — defines interfaces + YAML/JSON implementations for config, manifest, and snapshot backends |
| `internal/client/compose/` | Parses `docker-compose.yml` to enumerate every service's persistent mounts and the project name |
| `internal/client/docker/` | Wraps `docker compose stop/up -d` and polls health via `docker inspect` |
| `internal/service/` | Business logic; embeds repositories and clients; owns all orchestration |
| `internal/adapter/cli/` | Cobra `RunE` handlers; marshals DTOs; maps errors to exit codes |
| `internal/adapter/mcpserver/` | `mark3labs/mcp-go` server; registers 4 MCP tools; calls services |
| `internal/adapter/dto/` | Shared JSON response structs used by both adapters |
| `internal/cmd/` | Cobra command definitions (flags, `Use`, `Short`); `RunE` delegates to `adapter/cli` |
| `cmd/gm/main.go` | DI root — constructs all repositories, clients, services, and wires adapters |

### Key design decisions

**Interfaces belong to the consumer (dependency inversion).** Each service file declares its own private interfaces for the repositories and clients it needs. Concrete types in `repository/` and `client/` satisfy those interfaces implicitly — no interface definitions live in `repository/` or `client/`.

**Pluggable snapshot backends.** `SnapshotRef` and `VolumeRef` live in `repository/snapshot/ref.go` as shared value types. Each service that needs snapshotting declares its own `snapshotBackend` interface locally. `SnapshotRef.Opaque` is an opaque string only the creating backend interprets (filesystem path for copy, subvol UUID for btrfs, resource ID for cloud). Service code never inspects it. The MVP implementation is `repository/snapshot/copy/`.

**`VolumeRef` in the manifest.** Each savepoint record in `manifest.json` stores a `[]VolumeRef` — one per volume — pairing a volume name with its `SnapshotRef`. This is how rollback knows which backend handle to pass to `Restore`.

**All output is JSON.** Every command writes JSON to stdout. `adapter/dto/` is the single source of truth for JSON shapes — both CLI and MCP adapters use the same DTOs and mapping functions.

**`.gm/` directory layout.**
- `.gm/config.yaml` — project config (`ProjectConfig`): name, compose files, savepoint dir
- `.gm/manifest.json` — savepoint index; each volume entry stores `service`, `mount_path`, `type`, `source`, and `path`
- snapshot data lives under the configured savepoint dir (default `.gm/savepoints/<project-name>/`):
  `<savepoint-id>/<service-name>/<mount-path>/data.tar`, where `<mount-path>` is the container mount path with each `/` replaced by `_` (so `/export/data` becomes `_export_data`) — one `data.tar` archive per mount. The manifest's `mount_path` keeps the original unflattened path.

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

## Spec and Implementation Plan

- Full CLI UX spec (commands, flags, JSON output schemas, exit codes): `doc/spec.md`
- Detailed implementation plan with interface signatures: `doc/proposed-impl.md`