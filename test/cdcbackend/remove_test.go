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

package cdcbackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// D1: a removal drops the one snapshot its payload names, and no bytes — the packs
// are shared, and space comes back only from a future `arcave savepoint gc`.
func TestSnapshotter_Remove_DropsOnlyItsOwnSnapshot(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)
	first := env.snapshot(t, backend, idFirst, nil)
	env.mutate(t, vol)
	second := env.snapshot(t, backend, idSecond, nil)
	packsBefore := objectNames(t, env.repoStore(t, vol), dataPrefix)

	// Act
	err := backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idFirst, Payload: first})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{second.SnapshotObjectID}, env.snapshotObjectIDs(t, vol))
	assert.Equal(t, packsBefore, objectNames(t, env.repoStore(t, vol), dataPrefix),
		"a removal is a snapshot object away, never a prune")
}

// D2: a capture that reported no payload has no snapshot to drop, which is the
// ordinary cleanup of a failed create rather than an error.
func TestSnapshotter_Remove_WithoutAPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload any
	}{
		{name: "no payload at all", payload: nil},
		{name: "an empty id", payload: map[string]any{"restic_snapshot_id": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := newEnv(t, wipeOptions())
			vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
			backend := env.backend(t, vol)
			captured := env.snapshot(t, backend, idFirst, nil)

			// Act
			err := backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idSecond, Payload: tt.payload})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []string{captured.SnapshotObjectID}, env.snapshotObjectIDs(t, vol),
				"a removal with nothing to remove must not reach for another capture")
		})
	}
}

// The line D2 does not cross: a remove reaches snapshot objects and nothing
// else. arcave does not judge what a restic id looks like — an id it has never
// seen is simply a snapshot already gone (D3) — but an id that would address its
// way out of snapshots/ is refused, and the config object it aims at is the
// repository itself.
func TestSnapshotter_Remove_RefusesAnIDThatLeavesSnapshots(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)
	captured := env.snapshot(t, backend, idFirst, nil)

	// Act
	err := backend.Remove(env.ctx, model.BackendRemoveRequest{
		SnapshotID: idSecond,
		Payload:    map[string]any{"restic_snapshot_id": "../" + configKey},
	})

	// Assert
	require.Error(t, err)
	assert.Equal(t, []string{captured.SnapshotObjectID}, env.snapshotObjectIDs(t, vol))
	exists, err := env.repoStore(t, vol).Exists(env.ctx, configKey)
	require.NoError(t, err)
	assert.True(t, exists, "a remove must never reach the object the repository is")
}

// D3: a snapshot already gone is not an error — an interrupted remove is re-run.
func TestSnapshotter_Remove_IsIdempotent(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)
	payload := env.snapshot(t, backend, idFirst, nil)
	require.NoError(t, backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idFirst, Payload: payload}))

	// Act
	err := backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idFirst, Payload: payload})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, env.snapshotObjectIDs(t, vol))
}
