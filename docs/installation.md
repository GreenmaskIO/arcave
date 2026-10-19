---
title: Installation
description: Install the arcave CLI via the install script, a pre-built binary from GitHub releases, or from source. Requires Docker 25 or newer; no Compose plugin or docker CLI.
keywords: [install arcave, arcave binary, arcave cli, docker compose, go install, Open-Source, Greenmask]
---

# Installation

## Prerequisites

* **Docker 25 or newer**, with the daemon reachable by the user running arcave. See [Requirements](reference/requirements.md).
* Nothing else from Docker: arcave talks to the daemon and drives compose as libraries, so it needs neither the `docker` CLI nor the Compose plugin.
* For the [`cdc` backend](reference/configuration.md#snapshot) only: the ability to pull and run `restic/restic` containers, and a daemon that can reach a loopback port on your host.

arcave needs no compose file of its own and no changes to your project. It reads the running project from the daemon.

## Via install script

```shell title="Latest release into /usr/local/bin"
curl -fsSL https://greenmask.io/arcave/install.sh | bash
```

```shell title="A specific release, or a different directory"
curl -fsSL https://greenmask.io/arcave/install.sh | bash -s -- v1.2.3
curl -fsSL https://greenmask.io/arcave/install.sh | bash -s -- --version v1.2.3
curl -fsSL https://greenmask.io/arcave/install.sh | bash -s -- --dir ~/.local/bin
```

The script resolves the release, downloads the binary for your platform, verifies it against the release's `checksum.txt`, and installs it. It escalates with `sudo` only if the target directory is not writable.

| Environment variable | Effect |
|---|---|
| `ARCAVE_VERSION` | Release tag to install, instead of the latest; `-v/--version` overrides it. |
| `ARCAVE_INSTALL_DIR` | Default install directory, instead of `/usr/local/bin`; `-d/--dir` overrides it. |
| `ARCAVE_BASE_URL` | Base URL of the release host. Default `https://github.com`. |

## Via a pre-built binary

Releases publish one **bare executable** per platform — there is no archive to unpack.

```shell title="Download and install manually"
VERSION=1.2.3
PLATFORM=darwin_arm64          # or linux_amd64, linux_arm64, ...
curl -fsSLo arcave \
  "https://github.com/GreenmaskIO/arcave/releases/download/v${VERSION}/arcave_${VERSION}_${PLATFORM}"
chmod +x arcave
sudo install -m 0755 arcave /usr/local/bin/arcave
```

Published targets:

```text title="Supported platforms"
darwin_amd64     linux_amd64      linux_armv6      linux_ppc64le
darwin_arm64     linux_arm64      linux_armv7      linux_riscv64
                                                   linux_s390x
```

Checksums for the whole release live in a single `checksum.txt` beside the binaries.

## From source

Requires Go and a checkout of the repository.

```shell title="Build the binary"
git clone https://github.com/GreenmaskIO/arcave.git
cd arcave
make build          # produces ./bin/arcave
```

```shell title="Run the test suite"
make test-unit      # unit tests, no Docker needed
make test           # unit tests, then the integration tests against a live Docker daemon
make lint           # golangci-lint
```

:::note
`make build` stamps the version (from `git describe`), the commit and the build date into the binary. A plain `go build` or `go run` reports `dev`.
:::

## Verify the installation

```shell
arcave --version
```

## Next steps

* [Getting started](getting_started.md) — initialise a project and take your first savepoint.
* [Commands](reference/cli/index.md) — the full command reference.
