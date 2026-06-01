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
make build          # Compile to bin/gm
make test           # Run all tests
make test-verbose   # Run tests with -v
make lint           # Run golangci-lint
go test ./internal/compose/...   # Run tests for a specific package
go run ./cmd/gm -- <args>        # Run without building
```

## Architecture

```
cmd/gm/main.go              # Entry point — delegates to internal/cmd
internal/cmd/               # Cobra command definitions (one file per command)
internal/config/            # .gm/config.yaml and .gm/manifest.json types and I/O
internal/compose/           # Docker Compose file parsing and service interaction
internal/snapshot/          # Snapshot backend — MVP is copy-based; designed for pluggable backends
internal/doctor/            # Static analysis checks for gm doctor
internal/mcp/               # MCP server implementation
```

### Key design decisions

**Pluggable snapshot backends.** `internal/snapshot` is the critical extensibility point. The MVP implementation stops containers, copies volume directories, and restarts. Future backends (rsync, filesystem COW, storage-native snapshots) will implement the same interface without changing the user-facing workflow. When adding snapshot logic, keep the interface clean — no copy-specific assumptions should leak into `internal/cmd`.

**All output is JSON.** Every command writes JSON to stdout, including `gm doctor` and `gm status`. Errors should also be machine-readable where possible, to support agent-driven workflows.

**`.gm/` directory layout.** At runtime GM reads and writes:
- `.gm/config.yaml` — registered stateful services and their volume paths
- `.gm/manifest.json` — savepoint index (id, name, description, created_at)
- `.gm/snapshots/<savepoint-id>/` — one subdirectory per savepoint, containing volume copies

**Savepoint IDs** are Unix timestamp integers (e.g. `1780335122`) used as both the unique identifier and the snapshot directory name. The `manifest.json` stores this alongside the human name and the RFC3339Nano `created_at` timestamp.

**`gm doctor` is read-only.** It performs static analysis of the Docker Compose file and `.gm/` state and never modifies any files. Suggested fixes are consumed by `gm fix` (not yet implemented).

**Stateful service detection** in `gm doctor` and `gm init` recognises: PostgreSQL, MySQL, MariaDB, Redis, MongoDB, Kafka, Elasticsearch — by image name matching.

## Spec

Full CLI UX specification (commands, flags, JSON output schemas, exit codes) is in `doc/spec.md`.
