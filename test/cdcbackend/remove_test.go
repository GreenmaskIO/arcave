package cdcbackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// E1: Exists answers true without asking the repository anything — the record is
// the commit marker. Observably: a volume with no repository at all still says yes.
func TestSnapshotter_Exists(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol, model.BackendTierSavepoint)
	require.Empty(t, env.repoKeys(t, vol), "nothing has been captured, on purpose")

	// Act
	exists, err := backend.Exists(env.ctx, model.BackendExistsRequest{SnapshotID: idFirst})

	// Assert
	require.NoError(t, err)
	assert.True(t, exists)
}

// D1: a removal drops the one snapshot its payload names, and no bytes — the packs
// are shared, and space comes back only from a future `arcave savepoint gc`.
func TestSnapshotter_Remove_DropsOnlyItsOwnSnapshot(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol, model.BackendTierSavepoint)
	first := env.snapshot(t, backend, idFirst, nil)
	env.mutate(t, vol)
	second := env.snapshot(t, backend, idSecond, nil)
	packsBefore := objectNames(t, env.repoStore(t, vol), dataPrefix)

	// Act
	err := backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idFirst, Payload: first})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{second.SnapshotRef}, env.snapshotRefs(t, vol))
	assert.Equal(t, packsBefore, objectNames(t, env.repoStore(t, vol), dataPrefix),
		"a removal is a snapshot object away, never a prune")
}

// D2: a capture that reported no payload has no snapshot to drop, which is the
// ordinary cleanup of a failed create rather than an error.
func TestSnapshotter_Remove_WithoutAPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload any
	}{
		{name: "no payload at all", payload: nil},
		{name: "an empty ref", payload: map[string]any{"restic_snapshot_id": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := newEnv(t, wipeOptions())
			vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
			backend := env.backend(t, vol, model.BackendTierSavepoint)
			captured := env.snapshot(t, backend, idFirst, nil)

			// Act
			err := backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idSecond, Payload: tt.payload})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []string{captured.SnapshotRef}, env.snapshotRefs(t, vol),
				"a removal with nothing to remove must not reach for another capture")
		})
	}
}

// The line D2 does not cross: a ref that decodes but cannot name a snapshot is
// reported rather than swallowed.
func TestSnapshotter_Remove_RefusesAnUnusableRef(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol, model.BackendTierSavepoint)
	captured := env.snapshot(t, backend, idFirst, nil)

	// Act
	err := backend.Remove(env.ctx, model.BackendRemoveRequest{
		SnapshotID: idSecond,
		Payload:    map[string]any{"restic_snapshot_id": "NOT-HEX"},
	})

	// Assert
	require.Error(t, err)
	assert.Equal(t, []string{captured.SnapshotRef}, env.snapshotRefs(t, vol))
}

// D3: a snapshot already gone is not an error — an interrupted remove is re-run.
func TestSnapshotter_Remove_IsIdempotent(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol, model.BackendTierSavepoint)
	payload := env.snapshot(t, backend, idFirst, nil)
	require.NoError(t, backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idFirst, Payload: payload}))

	// Act
	err := backend.Remove(env.ctx, model.BackendRemoveRequest{SnapshotID: idFirst, Payload: payload})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, env.snapshotRefs(t, vol))
}
