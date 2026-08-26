---
title: Environment variables
description: The ARCAVE_ variables arcave reads — which config keys they reach, and the two different things ARCAVE_PROJECT_NAME names.
keywords: [arcave environment variables, ARCAVE_PROJECT_NAME, config keys, docker compose]
---

# Environment variables

A config key is set in the environment as `ARCAVE_<KEY>`, with dots replaced by underscores: `ARCAVE_LOG_LEVEL` feeds `log.level`. An environment variable beats `.arcave/config.yaml` and loses to a flag you actually passed.

## Which keys a variable reaches

A variable is read only for a key arcave already knows about — one a flag binds, or one your `.arcave/config.yaml` carries. A key that is in neither place is not read from the environment at all.

These are bound to flags, so they work in any project:

| Variable | Config key |
|---|---|
| `ARCAVE_LOG_LEVEL` | `log.level` |
| `ARCAVE_LOG_FORMAT` | `log.format` |
| `ARCAVE_COMPOSE_WAIT_TIMEOUT` | `compose.wait_timeout` |
| `ARCAVE_COMPOSE_STOP_TIMEOUT` | `compose.stop_timeout` |
| `ARCAVE_COMPOSE_PROGRESS` | `compose.progress` |
| `ARCAVE_PROJECT_NAME` | `project.name` — **and** the compose project, see below |
| `ARCAVE_STORAGE_DIRECTORY_PATH` | `storage.directory.path` |

Any other key works when the file declares it. `arcave init` writes `storage.type`, `snapshot.backend` and the `project`, `compose` and `log` sections, so `ARCAVE_SNAPSHOT_BACKEND` overrides a default config out of the box:

```shell
ARCAVE_SNAPSHOT_BACKEND=cdc arcave -p myapp savepoint create nightly
```

A `backends.cdc` block, which `init` does not write, is the case to watch: until the file carries the section, `ARCAVE_BACKENDS_CDC_RESTORE_MODE` is ignored silently — the run neither takes the value nor reports it. Write the section into the file, and the variable overrides it from then on.

## `ARCAVE_PROJECT_NAME`

This one variable names two different things.

* **The compose project**, which every command but `init` acts on — the same thing `-p/--project-name` names, as `docker compose ls` shows it.
* **The storage tenant** `project.name`, the segment of the store path that keeps two projects sharing one storage location apart.

They are usually the same string, and then this is invisible. When they must differ, pass `-p` on the command line and leave the variable unset:

```shell title="One compose project, one tenant — the ordinary case"
export ARCAVE_PROJECT_NAME=myapp
arcave savepoint create nightly
```

```shell title="Two compose projects under one tenant"
arcave -p myapp-blue savepoint create nightly
arcave -p myapp-green savepoint create nightly
```

Both compose projects store under the tenant `project.name` from the config file, in [store roots](storage_layout.md) that differ in their last segment. Exporting `ARCAVE_PROJECT_NAME=myapp-blue` instead would have moved the tenant too, and the green project's savepoints would have gone somewhere else.

## Variables that are not arcave's

`DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CERT_PATH` and `DOCKER_TLS_VERIFY` are read by the Docker client when the [`docker` section](configuration.md#docker) leaves the matching key empty, which is the default.

`ARCAVE_VERSION`, `ARCAVE_INSTALL_DIR` and `ARCAVE_BASE_URL` belong to the install script, not to the CLI.

## Next step

[Output and exit codes](output.md) — what a command writes, and where.
