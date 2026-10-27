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

// D1: a removal leaves its snapshot and every pack in the repository: the snapshot stays
// tagged with a savepoint no record commits, and arcave gc forgets it and prunes.
func TestSnapshotter_Remove_LeavesTheSnapshotToGC(t *testing.T) {
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
	assert.ElementsMatch(t, []string{first.SnapshotObjectID, second.SnapshotObjectID}, env.snapshotObjectIDs(t, vol))
	assert.Equal(t, packsBefore, objectNames(t, env.repoStore(t, vol), dataPrefix))
}

// D2: a capture that reported no payload is the ordinary cleanup of a failed create, not
// an error.
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
				"a removal never reaches another capture")
		})
	}
}
