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
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// safetySnapshotGarbage is the part of `arcave gc`'s JSON these tests read.
type safetySnapshotGarbage struct {
	Reports []struct {
		DataKind model.GarbageDataKind `json:"data_kind"`
		Detail   struct {
			Ops []model.GarbageOp `json:"ops"`
		} `json:"detail"`
	} `json:"reports"`
}

// ops is every op of dataKind's reports.
func (g safetySnapshotGarbage) ops(dataKind model.GarbageDataKind) []model.GarbageOp {
	var ops []model.GarbageOp
	for _, report := range g.Reports {
		if report.DataKind == dataKind {
			ops = append(ops, report.Detail.Ops...)
		}
	}
	return ops
}

// safetySnapshots is every safety snapshot savepoints holds, newest first as the listing orders it.
func safetySnapshots(savepoints []model.Savepoint) []model.SavepointID {
	var ids []model.SavepointID
	for _, savepoint := range savepoints {
		if savepoint.IsSafetySnapshot() {
			ids = append(ids, savepoint.ID)
		}
	}
	return ids
}

// Three rollbacks leave three undos until a gc retires all but the newest, which still undoes the
// third; what the retired two alone held, restic's snapshots and blobs, goes on the run after.
func TestGC_RetainsTheNewestSafetySnapshot(t *testing.T) {
	t.Parallel()

	// Arrange: cdc, for the restic snapshots a retired safety snapshot leaves.
	env := newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	writeStorageConfig(t, env.projectRoot, env.storage, config.SnapshotBackendCDC)
	compose := env.in(t, model.DefaultSession)
	arcave := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, env.projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	gc := func(args ...string) safetySnapshotGarbage {
		t.Helper()
		return decode[safetySnapshotGarbage](t, runArcave(t, env.projectRoot, nil, append([]string{"gc"}, args...)...))
	}
	listed := func() []model.SavepointID {
		t.Helper()
		return safetySnapshots(decode[[]model.Savepoint](t, arcave("savepoint", "list", "--include-safety-snapshots")))
	}
	compose.write(t, compose.replicas(t)[0], "v1")
	arcave("savepoint", "create", "v1")
	compose.write(t, compose.replicas(t)[0], "v2")
	arcave("savepoint", "create", "v2")

	// Act: the first undo holds a layer no other savepoint does, so its blobs are its own.
	compose.write(t, compose.replicas(t)[0], "first")
	first := decode[model.RollbackResult](t, arcave("rollback", "v1"))
	second := decode[model.RollbackResult](t, arcave("rollback", "v2"))
	compose.write(t, compose.replicas(t)[0], "third")
	third := decode[model.RollbackResult](t, arcave("rollback", "v1"))
	beforeGC := listed()
	preview := gc("--dry-run", "--kind", string(model.GarbageDataKindSafetySnapshots))
	afterPreview := listed()
	retiring := gc()
	afterGC := listed()
	undone := decode[model.RollbackResult](t, arcave("rollback", third.SafetySnapshot.String()))
	replica := compose.replicas(t)[0]
	volumeUndone, layerUndone := compose.read(t, replica, volumeMarker), compose.read(t, replica, layerMarker)
	freeing := gc()
	afterFreeing := listed()

	// Assert
	assert.Equal(t, []model.SavepointID{third.SafetySnapshot, second.SafetySnapshot, first.SafetySnapshot}, beforeGC,
		"every rollback leaves its undo, and only a gc retires it")
	wantRetired := []string{second.SafetySnapshot.String(), first.SafetySnapshot.String()}
	assert.Equal(t, wantRetired, targetsOf(preview.ops(model.GarbageDataKindSafetySnapshots)))
	assert.Equal(t, beforeGC, afterPreview, "a dry run retires nothing")
	assert.Equal(t, wantRetired, targetsOf(retiring.ops(model.GarbageDataKindSafetySnapshots)))
	assert.Empty(t, retiring.ops(model.GarbageDataKindDeduplicatedVolumes),
		"the run that retires them still read their restic snapshots as live")
	assert.Equal(t, []model.SavepointID{third.SafetySnapshot}, afterGC)
	assert.Equal(t, third.SafetySnapshot, undone.RolledBackTo.ID, "the kept undo is rolled back to by id")
	assert.Equal(t, "third", volumeUndone, "the volume is back from third rollback")
	assert.Equal(t, "third", layerUndone, "and the writable layer")
	var forgotten []string
	for _, op := range freeing.ops(model.GarbageDataKindDeduplicatedVolumes) {
		forgotten = append(forgotten, op.Savepoint.String())
	}
	assert.Subset(t, forgotten, wantRetired, "the next run forgets the retired snapshots' restic snapshots")
	assert.NotContains(t, forgotten, undone.SafetySnapshot.String())
	assert.NotEmpty(t, freeing.ops(model.GarbageDataKindImageLayers), "and frees the blobs only they listed")
	assert.Equal(t, []model.SavepointID{undone.SafetySnapshot}, afterFreeing,
		"the undo of the rollback to the kept one is the newest now")
}

// Two rollbacks to one savepoint leave two safety snapshots, told apart by id: neither has a name to
// mistake for the target, both name it in taken_before_rollback_to, and rolling back to one says it
// restored the state before that rollback.
func TestRollback_SafetySnapshotNamesItsRollback(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	writeStorageConfig(t, env.projectRoot, env.storage, config.SnapshotBackendFullCopy)
	arcave := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, env.projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	sp1 := decode[model.Savepoint](t, arcave("savepoint", "create", "sp1"))

	// Act
	first := decode[model.RollbackResult](t, arcave("rollback", "sp1"))
	second := decode[model.RollbackResult](t, arcave("rollback", "sp1"))
	listed := decode[[]model.Savepoint](t, arcave("savepoint", "list", "--include-safety-snapshots"))
	undone := string(arcave("--format", "text", "rollback", second.SafetySnapshot.String()))

	// Assert
	want := &model.SavepointRef{ID: sp1.ID, Name: "sp1"}
	require.Equal(t, []model.SavepointID{second.SafetySnapshot, first.SafetySnapshot}, safetySnapshots(listed))
	for _, savepoint := range listed {
		if !savepoint.IsSafetySnapshot() {
			continue
		}
		assert.Empty(t, savepoint.Name, "a safety snapshot has no name")
		assert.Equal(t, want, savepoint.TakenBeforeRollbackTo, "it names the rollback it was taken before")
	}
	assert.True(t, strings.HasPrefix(undone, "Rolled back to the state before rollback to sp1 · "+
		second.SafetySnapshot.String()), "the title names the state, not the target: %q", undone)
}

// --no-safety-snapshot and rollback.safety_snapshot: false both take none: no record stays, the
// result names none, and the flag wins over the config. A failure is core's to test: no daemon
// fails on demand here.
func TestRollback_WithoutASafetySnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config string
		flags  []string
		want   bool
	}{
		{name: "the default takes one", want: true},
		{name: "the flag skips it", flags: []string{"--no-safety-snapshot"}},
		{name: "the config skips it", config: "ARCAVE_ROLLBACK_SAFETY_SNAPSHOT=false"},
		{
			name:   "the flag wins over the config",
			config: "ARCAVE_ROLLBACK_SAFETY_SNAPSHOT=false",
			flags:  []string{"--safety-snapshot"},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			env := newSessionEnv(t)
			env.withComposeProject(t)
			initArcave(t, env)
			writeStorageConfig(t, env.projectRoot, env.storage, config.SnapshotBackendFullCopy)
			compose := env.in(t, model.DefaultSession)
			var environment []string
			if tt.config != "" {
				environment = []string{tt.config}
			}
			arcave := func(args ...string) []byte {
				t.Helper()
				return runArcave(t, env.projectRoot, environment, append([]string{"-p", env.project}, args...)...)
			}
			compose.write(t, compose.replicas(t)[0], "v1")
			arcave("savepoint", "create", "v1")
			compose.write(t, compose.replicas(t)[0], "v2")

			// Act
			rolledBack := arcave(append([]string{"rollback"}, append(tt.flags, "v1")...)...)
			listed := decode[[]model.Savepoint](t, arcave("savepoint", "list", "--include-safety-snapshots"))
			replica := compose.replicas(t)[0]
			volumeAfter, layerAfter := compose.read(t, replica, volumeMarker), compose.read(t, replica, layerMarker)

			// Assert
			var response map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rolledBack, &response))
			assert.Equal(t, "v1", volumeAfter)
			assert.Equal(t, "v1", layerAfter)
			if tt.want {
				assert.Contains(t, response, "safety_snapshot")
				assert.Len(t, safetySnapshots(listed), 1)
				return
			}
			assert.NotContains(t, response, "safety_snapshot", "an empty id is left out")
			assert.Empty(t, safetySnapshots(listed), "no record carries the label")
		})
	}
}

// gc.safety_snapshots.retain_recent reaches the gc the binary runs: 0 leaves none.
func TestGC_RetainRecentFromTheEnvironment(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	writeStorageConfig(t, env.projectRoot, env.storage, config.SnapshotBackendFullCopy)
	compose := env.in(t, model.DefaultSession)
	arcave := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, env.projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	compose.write(t, compose.replicas(t)[0], "v1")
	arcave("savepoint", "create", "v1")
	rolledBack := decode[model.RollbackResult](t, arcave("rollback", "v1"))

	// Act
	collected := decode[safetySnapshotGarbage](t, runArcave(t, env.projectRoot,
		[]string{"ARCAVE_GC_SAFETY_SNAPSHOTS_RETAIN_RECENT=0"}, "gc", "--kind", string(model.GarbageDataKindSafetySnapshots)))
	listed := decode[[]model.Savepoint](t, arcave("savepoint", "list", "--include-safety-snapshots"))

	// Assert
	assert.Equal(t, []string{rolledBack.SafetySnapshot.String()}, targetsOf(collected.ops(model.GarbageDataKindSafetySnapshots)))
	assert.Empty(t, safetySnapshots(listed))
	assert.Equal(t, []string{"v1"}, savepointNames(listed))
}

// savepoint remove --retain-recent 0 deletes every savepoint and still leaves the undo: the scan never sees it.
func TestSavepointRemove_RetainRecentZero(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	writeStorageConfig(t, env.projectRoot, env.storage, config.SnapshotBackendFullCopy)
	compose := env.in(t, model.DefaultSession)
	arcave := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, env.projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	compose.write(t, compose.replicas(t)[0], "v1")
	arcave("savepoint", "create", "v1")
	rolledBack := decode[model.RollbackResult](t, arcave("rollback", "v1"))
	require.NotEmpty(t, rolledBack.SafetySnapshot)

	// Act
	planned := decode[model.RemoveResult](t, arcave("savepoint", "remove", "--retain-recent", "0", "--dry-run"))
	removed := decode[model.RemoveResult](t, arcave("savepoint", "remove", "--retain-recent", "0"))
	listed := decode[[]model.Savepoint](t, arcave("savepoint", "list", "--include-safety-snapshots"))

	// Assert
	assert.True(t, planned.DryRun)
	assert.Equal(t, []string{"v1"}, savepointNames(planned.Removed))
	assert.Equal(t, []string{"v1"}, savepointNames(removed.Removed))
	assert.Equal(t, []model.SavepointID{rolledBack.SafetySnapshot}, safetySnapshots(listed))
	assert.Len(t, listed, 1, "only the safety snapshot is left")
}

// The MCP server takes rollback.safety_snapshot for its default, and the tool's argument wins over it.
func TestMCP_RollbackSafetySnapshot(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	initArcave(t, env)
	writeStorageConfig(t, env.projectRoot, env.storage, config.SnapshotBackendFullCopy)
	command := arcaveCommand(t, env, "mcp")
	command.Env = append(command.Env, "ARCAVE_ROLLBACK_SAFETY_SNAPSHOT=false")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "arcave-it", Version: "0"}, nil)
	cs, err := client.Connect(env.ctx, &mcpsdk.CommandTransport{Command: command}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	callTool[model.Savepoint](t, cs, "savepoint_create", map[string]any{"compose_project": env.project, "name": "v1"})

	// Act
	byDefault := callTool[model.RollbackResult](t, cs, "rollback", map[string]any{"compose_project": env.project, "ref": "v1"})
	asked := callTool[model.RollbackResult](t, cs, "rollback", map[string]any{
		"compose_project": env.project, "ref": "v1", "safety_snapshot": true,
	})

	// Assert
	assert.Empty(t, byDefault.SafetySnapshot)
	assert.NotEmpty(t, asked.SafetySnapshot)
}
