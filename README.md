# [arcave](https://www.greenmask.io/docs/arcave/?utm_source=github&utm_medium=repo&utm_campaign=oss)

<a href="https://www.greenmask.io/docs/arcave/?utm_source=github&utm_medium=repo&utm_campaign=oss">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/arcave-lockup-brand-on-dark-safe.svg">
    <img src="docs/assets/arcave-lockup-brand-on-light-safe.svg" alt="arcave" width="520">
  </picture>
</a>

## Ephemeral environments and branching for your Docker Compose project

**arcave** saves the whole live state of a running Compose project — every volume, every container filesystem, and the compose spec itself — and puts it back in one command. Get your environment to the state you want, save it, and come back to it in seconds.

It works on any Compose project, on the Docker you already have.

> **Alpha.** arcave is at `v0.1.0-alpha.1`. Commands, output and the storage format may change between alphas, with no migration path for savepoints taken by an earlier one. It is not ready for production use yet.

[![Discord](https://img.shields.io/discord/1179422525294399488?label=Discord&logo=discord)](https://discord.com/invite/rKBKvDECfd)

[![Documentation](https://img.shields.io/badge/docs-latest-blue)](https://www.greenmask.io/docs/arcave/?utm_source=github&utm_medium=repo&utm_campaign=oss)
[![License](https://img.shields.io/github/license/GreenmaskIO/arcave)](LICENSE)
[![GitHub Release](https://img.shields.io/github/v/release/GreenmaskIO/arcave)](https://github.com/GreenmaskIO/arcave/releases/latest)
[![GitHub Downloads (all assets, all releases)](https://img.shields.io/github/downloads/GreenmaskIO/arcave/total)](https://github.com/GreenmaskIO/arcave/releases)

```shell
arcave savepoint create before-migration
# ...run the migration, the test suite, the agent...
arcave rollback before-migration
```

## Key Features

*   **Whole-project savepoints**: One savepoint holds every stateful volume, every container's writable layer, and the compose spec the project was running under.
*   **[Consistent by construction](https://www.greenmask.io/docs/arcave/how_it_works/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: The services are stopped before a byte is read, so nothing is captured mid-write and a database comes back without a crash-recovery pass on start.
*   **[Reversible rollback](https://www.greenmask.io/docs/arcave/reference/cli/rollback/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: A rollback takes a safety snapshot of what it is about to overwrite and replays it automatically if anything fails. A failed rollback leaves you where you started.
*   **[Sessions and branching](https://www.greenmask.io/docs/arcave/reference/cli/session/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: Independent lines of savepoints inside one project. A fork copies a whole session into a new one, and the two lines never mix.
*   **[Incremental capture](https://www.greenmask.io/docs/arcave/reference/configuration/?utm_source=github&utm_medium=repo&utm_campaign=oss#snapshot)**: The `cdc` backend writes only the chunks that changed since the last savepoint. Container filesystems and images are deduplicated by default, whichever volume backend you pick — the base layer every savepoint shares is stored once.
*   **[Built for agents](https://www.greenmask.io/docs/arcave/reference/mcp_tools/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: `arcave mcp` serves savepoint create, list, remove and rollback — and the session commands — as MCP tools, so an agent checkpoints before it acts, rolls back when validation fails, and can fork a session of its own.
*   **Plain Docker**: No ZFS, no copy-on-write filesystem, no storage appliance, no Docker daemon to reconfigure — the daemon you already run, and the arcave binary.
*   **Any compose project**: Postgres, MySQL, Kafka, Redis, MinIO, your own services. No allowlist — arcave reads what your Docker daemon is running.

## Use Cases

*   **[Testing destructive migrations](https://www.greenmask.io/docs/arcave/use_cases/migration_testing/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: Run the migration against real data, again and again, until it is right. The reset is one command.
*   **[Agent iteration loops](https://www.greenmask.io/docs/arcave/use_cases/agent_loops/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: Give an autonomous agent a checkpoint per attempt, and a failed iteration costs a rollback instead of a rebuild.
*   **[Environment branching](https://www.greenmask.io/docs/arcave/use_cases/environment_branching/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: A session per feature branch, forked off a shared baseline and switched by name.
*   **[Resetting fixtures and demos](https://www.greenmask.io/docs/arcave/use_cases/fixture_reset/?utm_source=github&utm_medium=repo&utm_campaign=oss)**: Seed the environment once, save it, and return to that exact state for the rest of the project's life — object storage and cache included.

## Quick Start

```shell
# 1. Install the binary
curl -fsSL https://greenmask.io/arcave/install.sh | bash

# 2. Bring your compose project up and initialise arcave in it
docker compose up -d
arcave init
export ARCAVE_PROJECT_NAME=myapp     # as `docker compose ls` names it

# 3. Save the state you want to come back to
arcave savepoint create before-migration

# 4. ...run the migration, the experiment, the agent...

# 5. Go back to it
arcave rollback before-migration
```

For more details, visit the [Documentation](https://www.greenmask.io/docs/arcave/?utm_source=github&utm_medium=repo&utm_campaign=oss) — start with [Getting started](https://www.greenmask.io/docs/arcave/getting_started/?utm_source=github&utm_medium=repo&utm_campaign=oss), and read the [Limitations](https://www.greenmask.io/docs/arcave/reference/limitations/?utm_source=github&utm_medium=repo&utm_campaign=oss) before you build a workflow on arcave.

## Requirements

*   **Docker 25 or newer**, with the daemon reachable by the user running arcave.
*   **Linux or macOS.** There is no Windows build.

The full list is in [Requirements](https://www.greenmask.io/docs/arcave/reference/requirements/?utm_source=github&utm_medium=repo&utm_campaign=oss).

## Powered by

[![JetBrains logo.](https://resources.jetbrains.com/storage/products/company/brand/logos/jetbrains.svg)](https://jb.gg/OpenSource)
