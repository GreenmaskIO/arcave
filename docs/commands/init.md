---
title: init
description: Initialise arcave for a Docker Compose project — writes .arcave/config.yaml with the storage tenant name, savepoint directory and default timeouts.
keywords: [arcave init, project initialization, config.yaml, docker compose, Open-Source, Greenmask]
---

# arcave init

Creates the configuration every other arcave command requires.

## Usage

```text
Usage: arcave init [flags]
```

`init` is the one command that runs before arcave is configured. It names no stack, opens no storage, and contacts no Docker daemon — it only writes a file.

## Flags

| Flag | Default | Config key | Description |
|---|---|---|---|
| `--name` | the current directory name | `project.name` | The **storage tenant** name. Scopes the savepoint store so several projects can share one storage path. |
| `--savepoint-dir` | `.arcave/storage` | `storage.directory.path` | Where savepoint data is written. Relative paths resolve against the project root; absolute paths are honoured verbatim. |
| `--override` | `false` | *(none)* | Overwrite an existing arcave configuration. |

Global flags apply — see [Commands](index.md#global-flags).

:::note
`--name` names the storage tenant, **not** the compose project. The compose project is named per invocation with `-p/--project-name`, because one directory may hold several stacks. See [Concepts](../concepts.md#project-versus-stack).
:::

## Behavior

1. Resolves the effective configuration from flags, environment and defaults.
2. Refuses to run if `.arcave/config.yaml` already exists, unless `--override` is passed.
3. Writes `.arcave/config.yaml` under the project root.

Nothing else is created at this point. The store directory materialises on first use — a relative path is created when a savepoint is first written, while an **absolute** path must already exist, so a typo cannot quietly grow a fresh tree somewhere unexpected.

## Output

`init` is the one command that prints **nothing** on stdout. It reports through the log stream on stderr:

```text title="stderr"
INF initialized arcave project name=myapp path=.arcave/config.yaml
```

The file it writes:

```yaml title=".arcave/config.yaml"
project:
    name: myapp
compose:
    wait_timeout: 2m0s
    stop_timeout: 10s
    progress: log
storage:
    type: directory
    directory:
        path: .arcave/storage
snapshot:
    backend: fullcopy
log:
    level: info
    format: text
```

## Errors

| Condition | Message |
|---|---|
| Config already exists, no `--override` | `project already initialized use --override` |
| Project name normalises to nothing | `project.name: … (set project.name explicitly)` — pass `--name` |
| The configured store path is unsafe | The store may not be `/`, a shallow system directory, `$HOME`, or any ancestor of the project. |

An existing config that arcave cannot parse does **not** block `init --override` — overwriting it is exactly the fix. Every other command refuses to run against it.

## Examples

### Initialise with defaults

```shell
arcave init
```

### Name the storage tenant explicitly

```shell
arcave init --name myapp
```

### Keep savepoint data outside the project

```shell
arcave init --savepoint-dir /backups/myapp
```

Useful when volumes are large enough that you do not want them under the working tree, or when the store should sit on a different disk.

### Replace an existing configuration

```shell
arcave init --override
```

:::warning
`--override` rewrites `.arcave/config.yaml`. It does not migrate or delete existing savepoint data — but if the new config points `storage.directory.path` somewhere else, the savepoints under the old path stop being visible to arcave.
:::

## After init

Add `.arcave/` to your `.gitignore`. It holds the config, the lock file, the interrupted-rollback marker, and — by default — the savepoint data itself.

## Related docs

* [Configuration](../configuration.md) — every key in the file above.
* [Getting started](../getting_started.md) — init in context.
* [savepoint create](savepoint_create.md) — the next command you will run.
