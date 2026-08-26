---
title: arcave init
description: Write .arcave/config.yaml for a project — the storage tenant name, the savepoint directory, and the defaults every other command runs on.
keywords: [arcave init, config.yaml, project initialization, docker compose]
---

# arcave init

Writes the configuration every other command requires.

```text title="Synopsis"
arcave init [flags]
```

`init` names no compose project, opens no storage and contacts no Docker daemon. It resolves the effective config from flags, environment and defaults, and writes one file.

## Flags

| Flag | Default | Config key | Description |
|---|---|---|---|
| `--name` | the project directory's name, normalised | `project.name` | The **storage tenant** the store is scoped by. |
| `--savepoint-dir` | `.arcave/storage` | `storage.directory.path` | Where savepoint data goes. A relative path resolves against the project root; an absolute one is taken verbatim. |
| `--override` | `false` | *(none)* | Overwrite an existing configuration. |

Global flags apply — see [the CLI reference](index.md#global-flags).

`--name` names the storage tenant, not the compose project. The compose project is named per invocation with `-p/--project-name`, because one directory may hold several. The directory name is normalised into the tenant charset — `MyApp` becomes `myapp`, and characters outside `[a-z0-9_-]` are dropped.

## Output

`init` is the one command that prints nothing on stdout. It reports on the log stream:

```text title="stderr"
INF initialized arcave project name=myapp path=/home/you/myapp/.arcave/config.yaml
```

The file it writes for a default project:

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

No `backends.cdc` block is written: a project that does not name `cdc` carries no section for it. Every key is in [Configuration](../configuration.md).

Nothing else is created here. A relative store path materialises when the first savepoint is written; an **absolute** one must already exist, so a typo cannot quietly grow a fresh tree somewhere unexpected.

## Errors

| Condition | Message |
|---|---|
| A config is already there, without `--override` | `project already initialized use --override` |
| The project name normalises to nothing, and none was given | `resolve config: unmarshal config: project.name: invalid project name "": must not be empty (set project.name explicitly)` |
| A name outside the charset | `resolve config: unmarshal config: project.name: invalid project name "My App": must start with [a-z0-9] and continue with [a-z0-9_-] (set project.name explicitly)` |

Both name errors are raised while the config is resolved, which happens before any command runs — so every command reports them, not only `init`.

A config file arcave cannot parse does **not** block `init --override` — overwriting it is the fix that error names. Every other command refuses to run against it.

## Examples

```shell title="Initialise with defaults"
arcave init
```

```shell title="Name the storage tenant explicitly"
arcave init --name myapp
```

```shell title="Keep savepoint data outside the working tree"
mkdir -p /srv/arcave/myapp
arcave init --savepoint-dir /srv/arcave/myapp
```

An absolute path must exist before the first savepoint, which is why the `mkdir` is part of the example.

```shell title="Replace an existing configuration"
arcave init --override
```

`--override` rewrites the file rather than merging into it. If `storage.directory.path` was pointing somewhere non-default, set it again in the same command — savepoints under the old path stop being visible otherwise.

## After init

Add `.arcave/` to `.gitignore`. It holds the config, the lock file, the interrupted-rollback marker and — by default — the savepoint data itself.

## Next step

[`arcave savepoint create`](savepoint_create.md) — take the first savepoint.
