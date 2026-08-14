# arcave demo

> **The savepoint walkthrough this demo existed for does not run today.** arcave is
> mid-split into two binaries, and every command except `arcave init` is wired to a
> stub — `savepoint create` prints a plausible savepoint and captures nothing. See
> [the status section of the root README](../../README.md#status-pre-alpha-and-the-cli-does-not-do-the-thing-yet).
>
> What is left here is still useful: a small stateful stack of exactly the shapes
> arcave has to handle, ready to point at once the halves are joined. The arcave
> steps come back with the provider's `Capture`/`Restore`.

A small Go web app reads and writes three stores, each a different kind of mount:

| Store      | Mount in `docker-compose.yaml`     | arcave volume type   |
|------------|------------------------------------|----------------------|
| PostgreSQL | `pgdata:/var/lib/postgresql/data`  | **named volume**     |
| MinIO      | `miniodata:/data`                  | **named volume**     |
| Redis      | `- /data`                          | **anonymous volume** |
| app (Go)   | *(none)*                           | — stateless UI       |

That spread is the point: a named volume, an anonymous volume created per
container, and — when the stack is run on a host where it works — a bind mount are
three different addressing problems for a capture backend, and the demo carries
all of them in one compose file.

## Running the stack

```bash
cd examples/demo
docker compose up -d --wait
open http://localhost:8080        # macOS; otherwise just browse there
```

Each store is pre-seeded with distinct, labelled data, and the UI writes to all
three, so it is easy to see which ones came back after a restore.

```bash
docker compose down -v            # containers and named volumes
```

## Why MinIO no longer uses a bind mount

MinIO mounted `./minio-data` from the host until the demo moved to the cdc
backend. restic sets `O_NOATIME` on every source file through `fcntl`, and Docker
Desktop's VirtioFS answers the next read with `EIO` — so a bind mount on macOS
cannot be captured by cdc, while volumes, living in the VM's own filesystem, are
unaffected. Switching Docker Desktop's file sharing implementation to gRPC FUSE
is the other way out if you need a bind mount here.

The `minio-data/` directory is left in place for that reason: it is what the bind
arm of the demo binds to, and it returns with the fix.

## Notes

- Only the app (`8080`) publishes a host port, to avoid clashing with other local
  stacks. Postgres, Redis and MinIO are reached over the compose network. To use
  the MinIO web console, uncomment the `ports` block for `minio`.
- Configuring a snapshot backend is no longer a step in this directory. The
  backend is a **provider** setting and lives in the control plane's own config
  (`~/.arcave/control-plane.yaml`), not in the project's `.arcave/config.yaml`.
