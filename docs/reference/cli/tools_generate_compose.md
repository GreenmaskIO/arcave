---
title: arcave tools generate-compose
description: Generate a docker compose file from a running compose project, as the Docker daemon reports it.
keywords: [arcave tools generate-compose, docker compose file, generate compose, introspection]
---

# arcave tools generate-compose

Writes a docker compose file for the running compose project, built from what the Docker daemon reports about its containers, volumes and networks. It is for a project whose compose file was lost, or was never written.

```text title="Synopsis"
arcave tools generate-compose -p <compose-project> -o <file> [flags]
```

This is the spec `savepoint create` records in a savepoint's `project.yaml`, minus arcave's own labels and extensions. It is read-only: it stops nothing, takes no lock, opens no store and needs no `arcave init`. It needs only a reachable daemon, the one `docker.*` names.

## Flags

| Flag | Default | Description |
|---|---|---|
| `-o`, `--output` | *(required)* | The file to write. A new file is created `0600`, since it carries secrets; an existing one keeps its mode, and a symlink is written through. |
| `--force` | `false` | Overwrite the output file if it exists. Without it, an existing file is refused, before the daemon is read. |
| `--absolute-paths` | `false` | Keep every bind source absolute. |

Global flags apply — see [the CLI reference](index.md#global-flags).

## Bind paths

A bind source under the output file's directory becomes `./<path>`, so the file finds it from where it lands. A source outside that directory stays absolute. `--absolute-paths` keeps every source absolute.

## What the file holds

The file declares what the containers run with, not what the original file said: an image's own environment, entrypoint and command appear spelled out, and so does every environment value — **secrets included**. Treat the file as you would the container's `docker inspect`. A `$` is written as `$$`, as `docker compose config` writes it, so compose reads every value back as the container has it rather than interpolating it.

The top-level `volumes:` and `networks:` keep the names the daemon holds (`demo_pgdata`, `demo_default`). Bring the file up as the same compose project and it finds its data; brought up under another `-p`, it would share the original's volumes.

## Output

stdout carries a summary:

```json title="stdout"
{
  "compose_project": "demo",
  "services": ["db", "web"],
  "path": "/srv/demo/docker-compose.yaml"
}
```

## Errors

| Condition | What happens |
|---|---|
| No compose project named | `no compose project named: pass -p/--project-name, or set ARCAVE_PROJECT_NAME` |
| The compose project has no container | `compose project "demo": no containers found: bring the compose project up once, or name another project with -p` |
| The output file exists | `… already exists: pass --force to overwrite it` |

## Examples

```shell title="Recover a lost compose file"
arcave tools generate-compose -p demo -o docker-compose.yaml
docker compose -p demo -f docker-compose.yaml config
```
