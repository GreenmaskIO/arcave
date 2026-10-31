---
title: arcave init
description: Write .arcave/config.yaml for a project — the storage tenant name and the defaults every other command runs on, savepoint data under .arcave/storage.
keywords: [arcave init, config.yaml, project initialization, docker compose]
---

# arcave init

Writes the configuration every other command requires.

```text title="Synopsis"
arcave init [flags]
```

`init` names no compose project, opens no storage and contacts no Docker daemon. It writes one file: a template of the defaults plus `--name`. It reads neither the environment nor an existing file, so nothing set elsewhere — an `ARCAVE_*` variable, a value in the old file — is carried into the new one.

## Flags

| Flag | Default | Config key | Description |
|---|---|---|---|
| `--name` | the project directory's name, normalised | `project.name` in the file written | The **storage tenant** the store is scoped by. |
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
common:
    concurrency: 5
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
writable_layers:
    backend: dedupcopy
capture:
    writable_layers: true
    images: true
    volumes:
        named: true
        bind: true
        anonymous: true
rollback:
    leftover_volumes: overwrite
log:
    format: text
```

No `backends.cdc` block is written: a project that does not name `cdc` carries no section for it. Every key is in [Configuration](../configuration.md). The `capture` defaults say what a savepoint takes by kind; `arcave.*` labels in the compose file refine them per service and per volume — see [Capture labels](../capture_labels.md).

Savepoint data goes to the `directory` storage at `.arcave/storage`. Another directory, a bucket, a container or an SFTP server is set by editing `storage` in the file — see [Configuration](../configuration.md#storage). No credential is ever written: the secret keys are left out of the file `init` writes.

Nothing else is created here. A relative store path materialises when the first savepoint is written; an **absolute** one must already exist, so a typo cannot quietly grow a fresh tree somewhere unexpected.

## Errors

| Condition | Message |
|---|---|
| A config is already there, without `--override` | `project already initialized use --override` |
| The project name normalises to nothing, and none was given | `invalid config: project.name: invalid arcave project name "": must not be empty (set project.name explicitly)` |
| A name outside the charset | `invalid config: project.name: invalid arcave project name "My App": must start with [a-z0-9] and continue with [a-z0-9_-] (set project.name explicitly)` |

Nothing is written when either is raised. A file whose `project.name` is invalid fails every other command the same way, before it runs.

An existing file is never read, so one arcave cannot parse — or one naming a `${VAR}` that is not set — does **not** block `init --override`: overwriting it is the fix. Every other command refuses to run against it.

## Examples

```shell title="Initialise with defaults"
arcave init
```

```shell title="Name the storage tenant explicitly"
arcave init --name myapp
```

```yaml title="Keep savepoint data outside the working tree — .arcave/config.yaml after init"
storage:
    type: directory
    directory:
        path: /srv/arcave/myapp
```

An absolute path must exist before the first savepoint: `mkdir -p /srv/arcave/myapp` first.

```shell title="Replace an existing configuration"
arcave init --override
```

`--override` rewrites the file rather than merging into it: a `storage` section you edited goes back to the default directory, and savepoints under the old location stop being visible until you set it again.

## After init

Add `.arcave/` to `.gitignore`. It holds the config, the lock file (`lock`), the interrupted-rollback marker (`inflight.json`), the current-session pointer (`current_session`) and — by default — the savepoint data itself (`storage/`).

## Next step

[`arcave savepoint create`](savepoint_create.md) — take the first savepoint.
