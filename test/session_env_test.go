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

package test

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/config/storageconfig"
	"github.com/greenmaskio/arcave/internal/model"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
)

// sessionArcaveProject is the arcave project every session test stores under.
const sessionArcaveProject = "arcave-session-tenant"

// sessionEnv is one arcave project directory the built binary works in, and the compose project
// withComposeProject brings up beside it.
type sessionEnv struct {
	ctx         context.Context
	projectRoot string
	storage     storageconfig.Storage
	inflight    *inflightrepo.Repository
	// composeProject is the compose project, project its name; nil and empty until
	// withComposeProject.
	composeProject *composeProject
	project        string
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()

	projectRoot := t.TempDir()
	return &sessionEnv{
		ctx:         zerolog.New(io.Discard).WithContext(context.Background()),
		projectRoot: projectRoot,
		storage: storageconfig.Storage{
			Type:      storageconfig.StorageTypeDirectory,
			Directory: &storageconfig.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
		},
		inflight: inflightrepo.New(projectRoot),
	}
}

// storeDir is the store root on disk, every arcave project beneath it.
func (e *sessionEnv) storeDir() string {
	return filepath.Join(e.projectRoot, e.storage.Directory.Path, "arcave")
}

// sessionDir is where a session's tree lives on disk.
func (e *sessionEnv) sessionDir(id model.SessionID) string {
	return filepath.Join(e.storeDir(), "projects", sessionArcaveProject, "sessions", id.String())
}

// create makes a session named name through the binary and fails the test if it cannot.
func (e *sessionEnv) create(t *testing.T, name string) model.Session {
	t.Helper()

	return decode[model.Session](t, runArcave(t, e.projectRoot, nil, "session", "create", "--name", name))
}

// withComposeProject brings a compose project of one replica up beside the arcave project.
func (e *sessionEnv) withComposeProject(t *testing.T) {
	t.Helper()

	e.composeProject = newComposeProject(t, "session-it")
	e.project = e.composeProject.project
	e.composeProject.up(t, 1)
}

// listSavepoints lists the compose project's savepoints in session through the binary, newest
// first; includeSafety lists the safety snapshots too.
func (e *sessionEnv) listSavepoints(t *testing.T, session string, includeSafety bool) []model.Savepoint {
	t.Helper()

	args := []string{"-p", e.project, "--session", session, "savepoint", "list"}
	if includeSafety {
		args = append(args, "--include-safety-snapshots")
	}
	return decode[[]model.Savepoint](t, runArcave(t, e.projectRoot, nil, args...))
}
