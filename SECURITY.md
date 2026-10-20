# Security Policy

## Supported Versions

| Version               | Support Status |
|-----------------------|----------------|
| Latest stable release | Supported      |
| Older releases        | Unsupported    |

We recommend always running the latest release of arcave.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Report a vulnerability in arcave by emailing **security@greenmask.io**, or through
[GitHub's private vulnerability reporting](https://github.com/GreenmaskIO/arcave/security/advisories/new).

### What to include in your report

- A description of the vulnerability and its impact
- The affected version(s)
- Steps to reproduce, or a proof of concept
- Relevant configuration (redact credentials)
- Your suggested severity (Critical / High / Medium / Low)

### What to expect

- **Acknowledgement** within **72 hours**.
- **Initial assessment** within **7 days**.
- **Fix and disclosure**: we fix critical issues as quickly as we can and agree the
  disclosure timeline with the reporter.

We follow [coordinated vulnerability disclosure](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure)
and ask that you don't disclose the issue publicly until a fix is available.

## Security Model

arcave runs on a developer's machine against a local Docker daemon. A savepoint is a full
copy of a docker compose project's state:

- the bytes of every stateful volume;
- each container's writable layer and the image it was built on;
- the compose spec the project ran with, including its environment variables.

Savepoints are stored **unencrypted** in the configured storage path. Anyone who can read
that path can read the project's data and any secrets it holds.

## Security Considerations

### Docker daemon access

arcave needs the Docker API to stop, copy and recreate containers. Access to the Docker
socket is equivalent to root on the host. Run arcave only on machines and daemons you
trust.

### Stored data

- Point arcave only at development data. Don't take savepoints of production data unless
  it has been anonymized first, for example with [Greenmask](https://www.greenmask.io/).
- Restrict the storage path to your user (`chmod 700`).
- Keep secrets out of the compose file's `environment:`, or treat every savepoint as a
  secret.
- `arcave session remove` and `arcave savepoint remove` delete records, but image blobs
  shared between savepoints are not garbage-collected yet. Delete the storage path to
  remove all data.

### Rollback

A rollback overwrites the project's volumes and recreates its containers. On a running
project, arcave takes a safety snapshot first and reverts to it if the rollback fails.

### Local network services

The `cdc` backend runs a restic REST server for the duration of a command. By default it
binds to `127.0.0.1` on a random port. Don't change `backends.cdc.server_address` to a
public interface.

### MCP server

`arcave mcp` lets an AI agent create savepoints, roll back and delete sessions. It
communicates over stdio only and opens no network port. Give it to an agent only on a
project whose data you can afford to lose.

## Scope

In scope:

- Vulnerabilities in the arcave binary and its Go packages
- Writes or deletes outside the configured storage path
- Credential or secret leakage in logs, error messages or command output
- Insecure defaults that expose data
- Dependency vulnerabilities with a clear exploitation path in arcave

Out of scope:

- Vulnerabilities in Docker, docker compose or restic themselves
- Attacks by a user who already controls the Docker daemon
- Issues requiring physical access to the machine
- Social engineering

## Dependency Scanning

Audit the dependency tree locally with:

```bash
govulncheck ./...
```

Security fixes are documented in release notes and, where appropriate, in security
advisories.
