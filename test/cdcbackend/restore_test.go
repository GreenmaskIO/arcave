package cdcbackend

import (
	"encoding/json"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
)

// R1/R2: both modes converge on the snapshot — the mutation is rolled back and the
// file it never carried is gone.
func TestSnapshotter_Restore(t *testing.T) {
	tests := []struct {
		name string
		opts cdc.Options
	}{
		{name: "wipe clears the mount and restores it whole", opts: wipeOptions()},
		{name: "incremental restores over the live data", opts: incrementalOptions()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := newEnv(t, tt.opts)
			vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
			backend := env.backend(t, vol, model.BackendTierSavepoint)
			payload := env.snapshot(t, backend, idFirst, nil)
			captured := env.hash(t, vol)

			env.mutate(t, vol)
			env.addFile(t, vol, "extra")
			require.NotEqual(t, captured, env.hash(t, vol), "the volume must actually diverge first")

			// Act
			err := backend.Restore(env.ctx, model.BackendRestoreRequest{SnapshotID: idFirst, Payload: payload})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, captured, env.hash(t, vol),
				"the volume must hash as it did at capture time, extra file and all")
		})
	}
}

// R3: a payload out of a stored record arrives as a map, not the struct the
// capture returned. The round trip through JSON is the decode, not a fallback.
func TestSnapshotter_Restore_DecodesAStoredPayload(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol, model.BackendTierSavepoint)
	payload := env.snapshot(t, backend, idFirst, nil)
	captured := env.hash(t, vol)
	env.mutate(t, vol)

	stored := storedPayload(t, payload)
	require.IsType(t, map[string]any{}, stored, "the point of this case is the map, not the struct")

	// Act
	err := backend.Restore(env.ctx, model.BackendRestoreRequest{SnapshotID: idFirst, Payload: stored})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, captured, env.hash(t, vol))
}

// R4: the decode stands in front of the wipe, so a payload the backend cannot read
// costs nothing — a refusal that had already cleared the mount would destroy the
// live data for nothing.
func TestSnapshotter_Restore_RefusesAnUnusablePayload(t *testing.T) {
	tests := []struct {
		name    string
		payload any
	}{
		{name: "no payload at all", payload: nil},
		{name: "a ref that is not hex", payload: map[string]any{"restic_snapshot_id": "NOT-HEX"}},
		{name: "an empty ref", payload: map[string]any{"restic_snapshot_id": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := newEnv(t, wipeOptions())
			vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
			backend := env.backend(t, vol, model.BackendTierSavepoint)
			env.snapshot(t, backend, idFirst, nil)
			live := env.hash(t, vol)

			// Act
			err := backend.Restore(env.ctx, model.BackendRestoreRequest{SnapshotID: idFirst, Payload: tt.payload})

			// Assert
			require.Error(t, err)
			assert.Equal(t, live, env.hash(t, vol), "a refused restore must leave the volume alone")
		})
	}
}

// R5: a snapshot deleted behind arcave's back surfaces as an honest failure. The
// mount may already be wiped by then — the backend owes only the truth, and the
// service layer's revert owes the volume.
func TestSnapshotter_Restore_SnapshotDeletedBehindArcave(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol, model.BackendTierSavepoint)
	payload := env.snapshot(t, backend, idFirst, nil)
	require.NoError(t, env.repoStore(t, vol).Delete(env.ctx, path.Join(snapshotsPath, payload.SnapshotRef)))

	// Act
	err := backend.Restore(env.ctx, model.BackendRestoreRequest{SnapshotID: idFirst, Payload: payload})

	// Assert
	require.Error(t, err)
	var exit *model.ContainerExitError
	require.ErrorAs(t, err, &exit, "restic itself must be what refused: %v", err)
	assert.Contains(t, err.Error(), payload.SnapshotRef, "the failure must name the snapshot restic could not find")
	t.Logf("restic refused with: %v", err)
}

// storedPayload is a payload on the way back out of a record: JSON in, map out.
func storedPayload(t *testing.T, payload cdc.Payload) any {
	t.Helper()

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var stored any
	require.NoError(t, json.Unmarshal(raw, &stored))
	return stored
}
