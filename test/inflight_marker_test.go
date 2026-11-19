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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

// newMarkedEnv is a session env with its compose project up, a savepoint v1 of it, and the marker
// of a rollback to it interrupted while restoring, naming safety as the way back.
func newMarkedEnv(t *testing.T) (env *sessionEnv, v1 model.Savepoint) {
	t.Helper()

	env = newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	env.composeProject.write(t, env.composeProject.replicas(t)[0], "v1")
	v1 = decode[model.Savepoint](t, runArcave(t, env.projectRoot, nil, "-p", env.project, "savepoint", "create", "--name", "v1"))
	env.putMarker(t, model.DefaultSession, v1.ID, model.NewSavepointID())
	return env, v1
}

// putMarker writes the marker of a rollback in session to target, interrupted while restoring.
func (e *sessionEnv) putMarker(t *testing.T, session model.SessionID, target, safety model.SavepointID) {
	t.Helper()

	require.NoError(t, e.inflight.Put(e.ctx, model.InflightMarker{
		Phase:          model.InflightRestoring,
		Session:        session,
		ComposeProject: model.DockerComposeProjectName(e.project),
		Target:         target,
		Safety:         safety,
		At:             time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
	}))
}

// newSiblingDirectory is another directory of env's arcave project, over env's store: another
// checkout, or the same one after .arcave/ went.
func newSiblingDirectory(t *testing.T, env *sessionEnv) string {
	t.Helper()

	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", env.arcaveProject)
	writeStorageConfig(t, projectRoot, config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(env.projectRoot, env.storage.Directory.Path)},
	}, composeconfig.SnapshotBackendFullCopy)
	return projectRoot
}

// The marker is the daemon's: another directory of the arcave project, or the same one with its
// .arcave/ gone, is refused a capture under it and shows the rollback pending.
func TestInflightMarker_SeenFromAnyDirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// directory is where the commands run, given the env the marker stands in.
		directory func(t *testing.T, env *sessionEnv) string
	}{
		{name: "another directory", directory: newSiblingDirectory},
		{
			name: "the same directory with .arcave/ gone",
			directory: func(t *testing.T, env *sessionEnv) string {
				require.NoError(t, os.RemoveAll(filepath.Join(env.projectRoot, ".arcave")))
				runArcave(t, env.projectRoot, nil, "init", "--name", env.arcaveProject)
				return env.projectRoot
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			env, v1 := newMarkedEnv(t)
			projectRoot := tt.directory(t, env)

			// Act
			refused := runArcaveRefused(t, projectRoot, "-p", env.project, "savepoint", "create", "--name", "v2")
			status := decode[model.ComposeProjectStatus](t, runArcave(t, projectRoot, nil, "-p", env.project, "status"))

			// Assert
			assert.Contains(t, refused, "was interrupted")
			assert.Equal(t, model.ComposeProjectStateRollbackPending, status.State)
			require.NotNil(t, status.RollbackInFlight)
			assert.Equal(t, v1.ID, status.RollbackInFlight.Target)
		})
	}
}

// A marker volume removed by hand, found by its compose project's label, is a marker gone: the
// gate opens.
func TestInflightMarker_RemovedByHand(t *testing.T) {
	t.Parallel()

	// Arrange
	env, _ := newMarkedEnv(t)
	daemon := env.composeProject.daemon
	listed, err := daemon.VolumeList(env.ctx, client.VolumeListOptions{
		Filters: make(client.Filters).Add("label", "io.arcave.inflight.compose-project="+env.project),
	})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	_, err = daemon.VolumeRemove(env.ctx, listed.Items[0].Name, client.VolumeRemoveOptions{})
	require.NoError(t, err)

	// Act
	created := decode[model.Savepoint](t, runArcave(t, env.projectRoot, nil,
		"-p", env.project, "savepoint", "create", "--name", "v2"))

	// Assert
	assert.Equal(t, "v2", created.Name)
}

// newWayBackEnv is a session env whose marker names feat and its copy of v2 as the way back, v1
// and v2 committed in both sessions.
func newWayBackEnv(t *testing.T) (env *sessionEnv, feat model.Session, v2 model.Savepoint) {
	t.Helper()

	env = newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	app := env.composeProject
	app.write(t, app.replicas(t)[0], "v1")
	runArcave(t, env.projectRoot, nil, "-p", env.project, "savepoint", "create", "--name", "v1")
	app.write(t, app.replicas(t)[0], "v2")
	v2 = decode[model.Savepoint](t, runArcave(t, env.projectRoot, nil, "-p", env.project, "savepoint", "create", "--name", "v2"))
	feat = decode[model.Session](t, runArcave(t, env.projectRoot, nil,
		"session", "create", "--name", "feat", "--from", model.DefaultSessionName))
	env.putMarker(t, feat.ID, v2.ID, v2.ID)
	return env, feat, v2
}

// gc refuses to delete in the marker's session, its dry run reports; savepoint remove and session
// remove read no marker, so under one the way back it names goes.
func TestInflightMarker_StoreCommands(t *testing.T) {
	t.Parallel()

	// Arrange
	env, feat, v2 := newWayBackEnv(t)

	// Act
	refused := runArcaveRefused(t, env.projectRoot, "--session", "feat", "gc")
	// Its session alone: a report's detail is an interface the JSON does not decode back into.
	reported := decode[struct {
		Session model.SessionID `json:"session"`
	}](t, runArcave(t, env.projectRoot, nil, "--session", "feat", "gc", "--dry-run"))
	removed := decode[model.RemoveResult](t, runArcave(t, env.projectRoot, nil,
		"-p", env.project, "--session", "feat", "savepoint", "remove", v2.ID.String()))
	deleted := decode[model.RemoveSessionResponse](t, runArcave(t, env.projectRoot, nil, "session", "remove", "feat"))

	// Assert
	assert.Contains(t, refused, "was interrupted")
	assert.Equal(t, feat.ID, reported.Session)
	require.Len(t, removed.Removed, 1)
	assert.Equal(t, v2.ID, removed.Removed[0].ID)
	assert.True(t, deleted.Removed)
	assert.NoDirExists(t, env.sessionDir(feat.ID))
	_, found, err := env.inflight.Get(env.ctx)
	require.NoError(t, err)
	assert.True(t, found, "the marker stands, naming what is gone")
}

// A marker whose way back is gone still refuses another rollback; a forced down clears it and an
// up brings the environment back.
func TestInflightMarker_WayBackGone(t *testing.T) {
	t.Parallel()

	// Arrange
	env, _, _ := newWayBackEnv(t)
	runArcave(t, env.projectRoot, nil, "session", "remove", "feat")
	app := env.composeProject

	// Act
	refused := runArcaveRefused(t, env.projectRoot, "-p", env.project, "rollback", "v1")
	down := decode[model.DownResult](t, runArcave(t, env.projectRoot, nil, "-p", env.project, "down", "--force"))
	runArcave(t, env.projectRoot, nil, "-p", env.project, "up", "v1")

	// Assert
	assert.Contains(t, refused, "was interrupted")
	require.NotNil(t, down.ClearedInflight)
	assert.Equal(t, "v1", app.read(t, app.replicas(t)[0], daemontest.VolumeMarker))
	_, found, err := env.inflight.Get(env.ctx)
	require.NoError(t, err)
	assert.False(t, found)
}
