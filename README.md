# [arcave](https://greenmask.io/arcave?utm_source=github&utm_medium=repo&utm_campaign=oss)

<a href="https://greenmask.io/arcave?utm_source=github&utm_medium=repo&utm_campaign=oss">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/arcave-lockup-brand-on-dark-safe.svg">
    <img src="docs/assets/arcave-lockup-brand-on-light-safe.svg" alt="arcave" width="520">
  </picture>
</a>

## Ephemeral environments and branching for your Docker Compose project

**arcave** saves the whole live state of a running Compose project — every volume, every container filesystem, and the compose spec itself — and puts it back in one command. Get your environment to the state you want, save it, and come back to it in seconds.

It works on any Compose project, on the Docker you already have.

[![Discord](https://img.shields.io/discord/1179422525294399488?label=Discord&logo=discord)](https://discord.com/invite/rKBKvDECfd)

[![Documentation](https://img.shields.io/badge/docs-latest-blue)](docs/index.md)
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
*   **[Consistent by construction](docs/how_it_works.md)**: The services are stopped before a byte is read, so nothing is captured mid-write and a database comes back without a crash-recovery pass on start.
*   **[Reversible rollback](docs/reference/cli/rollback.md)**: A rollback takes a safety snapshot of what it is about to overwrite and replays it automatically if anything fails. A failed rollback leaves you where you started.
*   **[Sessions and branching](docs/reference/cli/session.md)**: Independent lines of savepoints inside one project. Fork a session off any savepoint and the two lines never mix.
*   **[Incremental capture](docs/reference/configuration.md#snapshot)**: The `cdc` backend writes only the chunks that changed since the last savepoint. Container filesystems are deduplicated whichever backend you pick — the base layer every savepoint shares is stored once.
*   **[Built for agents](docs/reference/mcp_tools.md)**: `arcave mcp` serves savepoint create, list, remove and rollback as MCP tools, so an agent checkpoints before it acts and rolls back when validation fails.
*   **Plain Docker**: No ZFS, no copy-on-write filesystem, no storage appliance, no Docker daemon to reconfigure. Docker 25 or newer, and the arcave binary.
*   **Any compose project**: Postgres, MySQL, Kafka, Redis, MinIO, your own services. No allowlist — arcave reads what your Docker daemon is running.

## Use Cases

*   **[Testing destructive migrations](docs/use_cases/migration_testing.md)**: Run the migration against real data, again and again, until it is right. The reset is one command.
*   **[Agent iteration loops](docs/use_cases/agent_loops.md)**: Give an autonomous agent a checkpoint per attempt, and a failed iteration costs a rollback instead of a rebuild.
*   **[Environment branching](docs/use_cases/environment_branching.md)**: A session per feature branch, forked off a shared baseline and switched by name.
*   **[Resetting fixtures and demos](docs/use_cases/fixture_reset.md)**: Seed the environment once, save it, and return to that exact state for the rest of the project's life — object storage and cache included.

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

For more details, visit the [Documentation](docs/index.md) — start with [Getting started](docs/getting_started.md), and read the [Limitations](docs/reference/limitations.md) before you build a workflow on arcave.

## Requirements

*   **Docker 25 or newer**, with the daemon reachable by the user running arcave.
*   **Linux or macOS.** There is no Windows build.

The full list is in [Requirements](docs/reference/requirements.md).

## Powered by

[![JetBrains logo.](https://resources.jetbrains.com/storage/products/company/brand/logos/jetbrains.svg)](https://jb.gg/OpenSource)
