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
	"slices"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
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

var (
	errNoTransport   = errors.New("cdc: no transport to the repositories")
	errNoServer      = errors.New("cdc stdio transport: no repository server")
	errNoRepoURLBase = errors.New("cdc tcp transport: no repository URL base")
)

// RepositoryServer serves one repository's REST protocol over stream until it ends.
type RepositoryServer interface {
	ServeRepository(ctx context.Context, objectID string, stream io.ReadWriteCloser) error
}

// Transport is how restic reaches a repository: a StdioTransport or a TCPTransport.
type Transport interface {
	validate() error
	// newOneShotSpec is the container running run against objectID's repository.
	newOneShotSpec(objectID string, run resticRun) model.OneShotSpec
}

// resticRun is one restic command's container, whatever the transport.
type resticRun struct {
	image string
	cmd   resticCmd
	// log takes what a transport runs beside restic has to say.
	log    *zerolog.Logger
	stdout io.Writer
	stderr io.Writer
}

// StdioTransport serves each repository over a docker exec stream beside restic
// (backends.cdc.transport: stdio).
type StdioTransport struct {
	Server RepositoryServer
}

func (s StdioTransport) validate() error {
	if s.Server == nil {
		return errNoServer
	}
	return nil
}

func (s StdioTransport) newOneShotSpec(objectID string, run resticRun) model.OneShotSpec {
	return model.OneShotSpec{
		Image:      run.image,
		Entrypoint: stdioEntrypointArgs,
		Cmd:        append(slices.Clone(stdioArgs), run.cmd.args...),
		Env: []string{
			"RESTIC_REPOSITORY=" + stdioRepository,
			"RESTIC_PASSWORD=" + resticPassword,
		},
		Binds:  run.cmd.binds,
		Stdout: run.stdout,
		Stderr: run.stderr,
		Exec: &model.OneShotExec{
			Cmd:    bridgeCmd,
			Stderr: bridgeLog{log: run.log},
			Serve: func(ctx context.Context, stream io.ReadWriteCloser) error {
				return s.Server.ServeRepository(ctx, objectID, stream)
			},
		},
	}
}

// bridgeLog writes the bridge's stderr to the debug log, a write at a time: it is
// diagnosis, and a write needs no flush to reach the log.
type bridgeLog struct {
	log *zerolog.Logger
}

func (w bridgeLog) Write(p []byte) (int, error) {
	if line := strings.TrimRight(string(p), "\r\n"); line != "" {
		w.log.Debug().Str("bridge", line).Msg("restic bridge stderr")
	}
	return len(p), nil
}

// TCPTransport has restic dial the TCP server (backends.cdc.transport: tcp).
type TCPTransport struct {
	// RepoURLBase is the server's "rest:http://host:port".
	RepoURLBase string
	// HostNetwork runs every restic container in the host's network namespace
	// (backends.cdc.network_mode).
	HostNetwork bool
}

func (s TCPTransport) validate() error {
	if s.RepoURLBase == "" {
		return errNoRepoURLBase
	}
	return nil
}

func (s TCPTransport) newOneShotSpec(objectID string, run resticRun) model.OneShotSpec {
	return model.OneShotSpec{
		Image: run.image,
		Cmd:   run.cmd.args,
		Env: []string{
			"RESTIC_REPOSITORY=" + s.newRepositoryURL(objectID),
			"RESTIC_PASSWORD=" + resticPassword,
		},
		Binds:       run.cmd.binds,
		HostNetwork: s.HostNetwork,
		Stdout:      run.stdout,
		Stderr:      run.stderr,
	}
}

// newRepositoryURL is the base plus the repository's directory, objectID. The trailing
// slash is the restic protocol's: without it restic talks to the wrong path.
func (s TCPTransport) newRepositoryURL(objectID string) string {
	return s.RepoURLBase + "/" + objectID + "/"
}
