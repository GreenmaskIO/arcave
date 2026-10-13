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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// A fork copies the whole session, byte for byte and without its record, and leaves the
// running compose project exactly as it was.
func TestSessionFork_ClonesTheSessionAndLeavesTheDaemonAlone(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	baseline := env.in(t, model.DefaultSession)
	containerID := baseline.replicas(t)[0]
	baseline.write(t, containerID, "v1")
	v1, err := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "v1"})
	require.NoError(t, err)
	baseline.write(t, containerID, "v2")
	v2, err := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "v2"})
	require.NoError(t, err)
	baseline.write(t, containerID, "unsaved")

	// Act
	clone, err := env.sessions.CreateSession(env.ctx, model.CreateSessionRequest{Name: "feat", From: "baseline"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, &model.SessionOrigin{Session: model.DefaultSession}, clone.From)

	list, err := env.in(t, clone.ID).svc.ListSavepoints(env.ctx, model.ListRequest{})
	require.NoError(t, err)
	got := map[model.SavepointID]string{}
	for _, sp := range list {
		got[sp.ID] = sp.Name
	}
	assert.Equal(t, map[model.SavepointID]string{v1.ID: "v1", v2.ID: "v2"}, got, "the same savepoints, ids and names")

	source := relativeSizes(t, env.sessionDir(model.DefaultSession))
	copied := relativeSizes(t, env.sessionDir(clone.ID))
	assert.Contains(t, copied, "session.json", "the clone has its own record")
	hb := readHeartbeat(t, env.sessionDir(clone.ID))
	assert.Equal(t, heartbeat.StatusDone, hb.Status)
	from, err := heartbeat.DecodeData[model.ForkHeartbeatData](hb)
	require.NoError(t, err)
	assert.Equal(t, model.DefaultSession, from.From)
	delete(copied, "session.json")
	delete(copied, "heartbeat.json")
	assert.Equal(t, source, copied, "the clone's tree is the source's")
	assert.Equal(t, env.summary(t, model.DefaultSession).SizeBytes+fileSize(t, env.sessionDir(clone.ID), "session.json")+
		fileSize(t, env.sessionDir(clone.ID), "heartbeat.json"),
		env.summary(t, clone.ID).SizeBytes, "size_bytes is the source's plus the record and the heartbeat")

	assert.Equal(t, []string{containerID}, baseline.replicas(t), "no container was touched")
	assert.Equal(t, "unsaved", baseline.read(t, containerID, volumeMarker), "nor any volume")
}

// A rollback is what deploys a fork, and it needs nothing of the source: blobs and upstreams
// travel with the clone. Layers live only in the store until a rollback loads them.
func TestSessionFork_RollbackInTheForkDeploys(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	src := env.create(t, "src")
	source := env.in(t, src.ID)
	source.write(t, source.replicas(t)[0], "original")
	sp, err := source.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)
	clone, err := env.sessions.CreateSession(env.ctx, model.CreateSessionRequest{Name: "feat", From: "src"})
	require.NoError(t, err)
	_, err = env.sessions.DeleteSession(env.ctx, "src")
	require.NoError(t, err)
	require.NoDirExists(t, env.sessionDir(src.ID))
	source.write(t, source.replicas(t)[0], "drifted")
	fork := env.in(t, clone.ID)

	// Act
	_, err = fork.svc.Rollback(env.ctx, model.RollbackRequest{ID: &sp.ID})

	// Assert
	require.NoError(t, err)
	replica := fork.replicas(t)[0]
	assert.Equal(t, "original", fork.read(t, replica, volumeMarker), "the volume")
	assert.Equal(t, "original", fork.read(t, replica, layerMarker), "the writable layer")
	list, err := fork.svc.ListSavepoints(env.ctx, model.ListRequest{IncludeSafetySnapshots: true})
	require.NoError(t, err)
	assert.Len(t, list, 1, "the rollback dropped its safety snapshot")
}

// A fork that fails mid-copy creates no session and leaves its tree, marked failed, to a gc.
func TestSessionFork_FailureLeavesTheTreeMarkedFailed(t *testing.T) {
	// Arrange: the second object of the source cannot be read.
	if os.Geteuid() == 0 {
		t.Skip("root reads an unreadable file")
	}
	env := newSessionEnv(t)
	baseline := env.sessionDir(model.DefaultSession)
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(baseline, "compose", "shop", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "data.tar"), []byte(name), 0o644))
	}
	unreadable := filepath.Join(baseline, "compose", "shop", "b", "data.tar")
	require.NoError(t, os.Chmod(unreadable, 0))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	// Act
	_, createErr := env.sessions.CreateSession(env.ctx, model.CreateSessionRequest{Name: "feat", From: "baseline"})

	// Assert
	require.Error(t, createErr)
	assert.Equal(t, []string{"baseline"}, env.names(t), "no session was created")
	entries, err := os.ReadDir(filepath.Dir(baseline))
	require.NoError(t, err)
	var trees []string
	for _, entry := range entries {
		if entry.Name() != model.DefaultSession.String() {
			trees = append(trees, entry.Name())
		}
	}
	require.Len(t, trees, 1, "the fork's tree is left in the store")
	tree := filepath.Join(filepath.Dir(baseline), trees[0])
	assert.NoFileExists(t, filepath.Join(tree, "session.json"))
	hb := readHeartbeat(t, tree)
	assert.Equal(t, heartbeat.StatusFailed, hb.Status)
	assert.Equal(t, heartbeat.Operation(model.HeartbeatOperationFork), hb.Operation)
	assert.Equal(t, createErr.Error(), hb.Error)
	assert.False(t, hb.FinishedAt.IsZero())
}

// readHeartbeat decodes the heartbeat.json of the session tree at dir.
func readHeartbeat(t *testing.T, dir string) heartbeat.Heartbeat {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "heartbeat.json"))
	require.NoError(t, err)
	var hb heartbeat.Heartbeat
	require.NoError(t, json.Unmarshal(data, &hb))
	return hb
}

// fileSize is the size of the file name under dir.
func fileSize(t *testing.T, dir, name string) int64 {
	t.Helper()

	info, err := os.Stat(filepath.Join(dir, name))
	require.NoError(t, err)
	return info.Size()
}

// relativeSizes maps every regular file under dir, by its path relative to dir, to its size.
func relativeSizes(t *testing.T, dir string) map[string]int64 {
	t.Helper()

	sizes := map[string]int64{}
	for name, size := range fileSizes(t, dir) {
		rel, err := filepath.Rel(dir, name)
		require.NoError(t, err)
		sizes[filepath.ToSlash(rel)] = size
	}
	return sizes
}
