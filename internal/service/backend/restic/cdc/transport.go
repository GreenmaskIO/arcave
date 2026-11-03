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
	_ "embed"
	"errors"
	"io"
	"io/fs"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// The stdio transport's layout in every restic container. stdioDir is
// world-writable and sticky, like /tmp, so the FIFOs stdio.sh makes in it can be
// made by whichever user the image runs restic as.
const (
	stdioDir        = "/run/arcave"
	stdioScriptPath = stdioDir + "/stdio.sh"
	// passwordPath is RESTIC_PASSWORD_FILE under either transport: a file, unlike the
	// environment, is not what docker inspect shows. Readable by whichever user runs restic.
	passwordPath = stdioDir + "/password"
	// stdioRepository is RESTIC_REPOSITORY under stdio: restic's rclone backend,
	// whose remote name stdio.sh ignores.
	stdioRepository = "rclone:arcave"
)

// stdioScript is the shell half of the stdio transport; its header says what
// runs where, and when.
//
//go:embed stdio.sh
var stdioScript []byte

var errNoTransport = errors.New("cdc: no transport to the repositories")

// RepositoryServer serves one repository's REST protocol over stream until it ends.
type RepositoryServer interface {
	ServeRepository(ctx context.Context, objectID string, stream io.ReadWriteCloser) error
}

// Transport is how restic reaches a repository: a StdioTransport or a TCPTransport.
type Transport interface {
	// newOneShotSpec is the container running run against objectID's repository.
	newOneShotSpec(objectID string, run resticRun) model.OneShotSpec
}

// resticRun is one restic command's container, whatever the transport.
type resticRun struct {
	image    string
	password string
	cmd      resticCmd
	// log takes what a transport runs beside restic has to say.
	log    *zerolog.Logger
	stdout io.Writer
	stderr io.Writer
}

// StdioTransport serves each repository over a docker exec stream beside restic
// (backends.cdc.transport: stdio): restic runs behind stdio.sh, copied into its
// container, and Server serves the stream of the script's bridge.
type StdioTransport struct {
	Server RepositoryServer
}

func (s StdioTransport) newOneShotSpec(objectID string, run resticRun) model.OneShotSpec {
	return model.OneShotSpec{
		Image:      run.image,
		Files:      newStdioFiles(run.password),
		Entrypoint: []string{stdioScriptPath, "entrypoint"},
		Cmd:        run.cmd.args,
		Env: []string{
			"RESTIC_REPOSITORY=" + stdioRepository,
			"RESTIC_PASSWORD_FILE=" + passwordPath,
		},
		Binds:  run.cmd.binds,
		Stdout: run.stdout,
		Stderr: run.stderr,
		Exec: &model.OneShotExec{
			Cmd:    []string{stdioScriptPath, "bridge"},
			Stderr: bridgeLog{log: run.log},
			Serve: func(ctx context.Context, stream io.ReadWriteCloser) error {
				return s.Server.ServeRepository(ctx, objectID, stream)
			},
		},
	}
}

// newStdioFiles is what every stdio container is created with: stdio.sh beside the password.
func newStdioFiles(password string) []model.OneShotFile {
	return append(newPasswordFiles(password), model.OneShotFile{Path: stdioScriptPath, Mode: 0o755, Content: stdioScript})
}

// newPasswordFiles is what every restic container is created with: the repository's password.
func newPasswordFiles(password string) []model.OneShotFile {
	return []model.OneShotFile{
		{Path: stdioDir, Mode: fs.ModeDir | fs.ModeSticky | 0o777},
		{Path: passwordPath, Mode: 0o444, Content: []byte(password)},
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

func (s TCPTransport) newOneShotSpec(objectID string, run resticRun) model.OneShotSpec {
	return model.OneShotSpec{
		Image: run.image,
		Files: newPasswordFiles(run.password),
		Cmd:   run.cmd.args,
		Env: []string{
			"RESTIC_REPOSITORY=" + s.newRepositoryURL(objectID),
			"RESTIC_PASSWORD_FILE=" + passwordPath,
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
