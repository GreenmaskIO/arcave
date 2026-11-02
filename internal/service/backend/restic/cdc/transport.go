// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cdc

import (
	"context"
	"errors"
	"io"
)

// stdioEntrypoint is restic's PID1 under stdio: it makes the bridge's FIFOs, writes the relay
// (one printf argument per line, past restic's argument splitter) and execs restic.
const stdioEntrypoint = `set -e
mkdir -p /run/arcave
mkfifo /run/arcave/req /run/arcave/resp
printf '%s\n' '#!/bin/sh' 'cat /run/arcave/resp 2>/dev/null &' 'exec cat > /run/arcave/req' > /run/arcave/relay
chmod +x /run/arcave/relay
exec restic "$@"`

// stdioRepository is RESTIC_REPOSITORY under the stdio transport; the remote
// name is an ignored argument of the relay.
const stdioRepository = "rclone:arcave"

// stdioEntrypointArgs runs stdioEntrypoint with "restic" as its $0, so the
// container's Cmd becomes its "$@".
var stdioEntrypointArgs = []string{"sh", "-c", stdioEntrypoint, "restic"}

// stdioArgs point restic's rclone backend at the relay; global flags, so they
// precede the subcommand.
var stdioArgs = []string{"-o", "rclone.program=/run/arcave/relay"}

// bridgeCmd is the exec beside restic, started once the entrypoint has made the
// FIFOs (resp is the later one): the relay's requests out on its stdout,
// the server's responses in on its stdin.
var bridgeCmd = []string{"sh", "-c",
	"until [ -p /run/arcave/resp ]; do sleep 0.01; done; cat /run/arcave/req & exec cat > /run/arcave/resp"}

// RepositoryServer serves one repository's REST protocol over stream until it ends.
type RepositoryServer interface {
	ServeRepository(ctx context.Context, objectID string, stream io.ReadWriteCloser) error
}

// Transport is how restic reaches the repositories: exactly one of the two is set.
type Transport struct {
	// Server serves each repository over a docker exec stream (backends.cdc.transport: stdio).
	Server RepositoryServer
	// RepoURLBase is the TCP server's "rest:http://host:port"; a caller appends
	// "/<object-id>/" (backends.cdc.transport: tcp).
	RepoURLBase string
}

// Validate refuses a transport naming neither way to the repositories, or both.
func (m Transport) Validate() error {
	switch {
	case m.Server == nil && m.RepoURLBase == "":
		return errors.New("cdc transport: neither a repository server nor a repository URL base")
	case m.Server != nil && m.RepoURLBase != "":
		return errors.New("cdc transport: both a repository server and a repository URL base")
	}
	return nil
}
