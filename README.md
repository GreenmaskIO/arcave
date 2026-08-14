# arcave

arcave is a lifecycle manager for stateful local environments. It adds **savepoints** — consistent snapshots of every stateful volume in a stack — so you can run a migration, a data transformation or an agent loose on a project and **roll back** to a known-good state in one command.

A stack is driven through a **provider**. There is one today, for Docker Compose; the seam exists so a second runtime can be added without the savepoint layer learning about it.

## Status: pre-alpha, and the CLI does not do the thing yet

**Read this before trying the commands below.** arcave is mid-way through a split from one binary into two — a thin CLI and a control plane service — and the halves are not joined yet.

| What | State |
|---|---|
| `arcave init` | works — writes `.arcave/config.yaml` and nothing else |
| `control-plane serve` | works — builds the configured providers, listens, serves `GET /api/v1/healthcheck` |
| every other `arcave` command | **wired to a stub**: prints a canned JSON response and touches nothing |
| the compose provider's `Capture`/`Restore`/`Discard` | **not implemented** — they return an error |
| the capture engine (compose, docker, restic, fullcopy, the stores) | built, tested, and currently parked with no caller |

So `arcave savepoint create` will print a plausible savepoint and take none. The capture machinery underneath is real — it has integration tests against a live daemon — but nothing calls it while the two binaries are being joined up.

Consider this repository readable, not runnable. There is no released version and no compatibility promise: the config schema, the command tree and the on-disk layout all still move.

## Build

```bash
make build        # -> bin/arcave and bin/control-plane
make test
make lint
```

Requirements: Go, and Docker for anything that reaches a daemon. arcave shells out to nothing — it speaks the Docker API and uses `compose/v2` as a library — so no `docker` CLI or compose plugin has to be installed.

Two packages (`internal/service/stack`, `internal/service/sandbox`) do not compile; they are skeletons for a later increment and are excluded from `make build`, which builds the two commands rather than `./...`.

## The two binaries

**`arcave`** — the client. It resolves the addressing, builds a request, sends it, and prints the JSON answer. It knows nothing of Docker, compose or storage. Its config is the project's own `.arcave/config.yaml`: which project, which control plane to reach, log settings.

**`control-plane`** — the service. It reads a host-level config (`~/.arcave/control-plane.yaml`), builds one provider per registered entry, and serves the API. This is the half that owns the daemon connection, the capture backends and the stores.

Every command addresses its target the same way, through flags on the root: `--project`, `--session-id` (or `$ARCAVE_SESSION_ID`), `--provider`, `--stack`. All output is JSON on stdout; logs go to stderr.

## Command tree

```
arcave init                                  write .arcave/config.yaml
arcave project apply | list | remove <name>
arcave session create | list | status
arcave savepoint create [name] | list | remove [uuid...]
arcave rollback <savepoint-uuid>
arcave recommend                             report volumes a service should mount but does not
arcave provider list
arcave status                                not implemented
arcave mcp                                   MCP server over stdio for AI agents

control-plane serve
```

`arcave recommend` is read-only and reports a finding through its **exit code**: 1 when it has advice, 0 when it does not. The JSON still goes to stdout and nothing goes to stderr — a finding is not a failure.

Full UX contract, flags and JSON shapes: [`doc/spec.md`](doc/spec.md).

## How a savepoint is meant to work

Create: stop the services → capture each volume → write the record. The record is written **last**, and it is the commit marker: a savepoint whose data was written but whose record was not is invisible to `list` and safe to collect.

Rollback: stop the services → capture the live state as a safety savepoint → wipe and restore each volume → start and wait for health checks. If a restore fails or the services come up unhealthy, arcave replays the safety savepoint to put the environment back where it started. A marker at `.arcave/inflight.json` stands for the length of the destructive window, so a run that was killed inside it is refused by the next command rather than silently continued.

Two capture backends exist. **fullcopy** stores each volume's tar stream verbatim, one file per volume per savepoint. **cdc** captures into a restic repository that outlives any one savepoint, writing only the chunks that changed — over stock `restic` containers, reaching their repository through a REST server arcave runs itself.

## Where things live

```
.arcave/
├── config.yaml        the project's own config
├── lock               the flock a mutating workflow holds
└── inflight.json      the interrupted-rollback marker, while one stands

<configured path>/arcave/projects/<project>/sessions/<session>/compose/<stack>/
├── <savepoint-id>/savepoint.json      the record — written last, deleted first
├── <savepoint-id>/<object-id>/…       what a backend wrote for that volume
└── restic/<object-id>/                one restic repository per volume
```

The static `arcave` segment is not configurable: everything a delete can reach is arcave's own data, wherever the path points.

## Repository layout

| Path | What |
|---|---|
| `cmd/arcave`, `cmd/control-plane` | the two binaries |
| `internal/model` | arcave's own types; imports no other arcave package |
| `internal/provider` | the provider seam |
| `internal/provider/docker` | everything docker: the `stack` (compose) provider, the `sandbox` slot, and under `internal/` the machinery they share — the daemon client, the capture backends, the restic REST server, the stores and the repositories |
| `internal/ng/cli` | one handler per command; the client half |
| `doc/spec.md` | the CLI UX contract |
| `CLAUDE.md` | the code doctrine, for humans and agents alike |

## License

Apache 2.0.
