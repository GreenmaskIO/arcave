package cdcbackend

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// S1: the first capture initialises the repository and ships the volume whole,
// and leaves the volume itself untouched — a backup mounts it read-only.
func TestSnapshotter_Snapshot_InitialisesTheRepository(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)
	require.Empty(t, env.repoKeys(t, vol), "the repository must not exist before the first capture")
	before := env.hash(t, vol)

	// Act
	payload := env.snapshot(t, backend, idFirst, nil)

	// Assert
	assert.Regexp(t, "^[0-9a-f]{64}$", payload.SnapshotObjectID)
	assert.Positive(t, payload.DataAdded, "the first capture ships the volume whole")
	assert.Positive(t, payload.TotalBytesProcessed)

	assert.Equal(t, []string{payload.SnapshotObjectID}, env.snapshotObjectIDs(t, vol),
		"one capture is one snapshot object, named by the id the payload carries")
	exists, err := env.repoStore(t, vol).Exists(env.ctx, configKey)
	require.NoError(t, err)
	assert.True(t, exists, "the first capture initialises the repository")
	assert.NotEmpty(t, objectNames(t, env.repoStore(t, vol), dataPrefix), "the volume's chunks must be in the repository")
	assert.Empty(t, objectNames(t, env.repoStore(t, vol), locksPrefix), "restic releases its lock when it is done")

	assert.Equal(t, before, env.hash(t, vol), "a capture must not write to the data it reads")
}

// S2: the second capture ships only what changed — the reason this backend exists.
func TestSnapshotter_Snapshot_ShipsAnIncrement(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)
	first := env.snapshot(t, backend, idFirst, nil)
	env.mutate(t, vol)

	// Act
	second := env.snapshot(t, backend, idSecond, nil)

	// Assert
	t.Logf("data added: first %d bytes, second %d bytes", first.DataAdded, second.DataAdded)
	assert.Less(t, second.DataAdded, first.DataAdded/4,
		"a capture after a ~%d KiB change must not reship the volume", mutateKiB)
	assert.ElementsMatch(t, []string{first.SnapshotObjectID, second.SnapshotObjectID}, env.snapshotObjectIDs(t, vol),
		"the two captures are two snapshots in one repository")
}

// S3: the savepoint id and every label reach the repository as restic tags, which
// is what makes a capture findable with a plain restic CLI.
func TestSnapshotter_Snapshot_RendersLabelsAsTags(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)

	// Act
	payload := env.snapshot(t, backend, idFirst, map[string]string{
		"arcave-safety-snapshot": "true",
		"name":                   "before-migration",
	})

	// Assert
	snapshots := env.oracleSnapshots(t, vol)
	require.Len(t, snapshots, 1)
	assert.Equal(t, payload.SnapshotObjectID, snapshots[0].ID, "the payload names the snapshot restic reports")
	assert.ElementsMatch(t, []string{
		"arcave-savepoint=" + idFirst.String(),
		"arcave-safety-snapshot=true",
		"name=before-migration",
	}, snapshots[0].Tags)
	// Host and path are what restic picks a parent by; S5 proves both captures agree.
	assert.Equal(t, itProject, snapshots[0].Hostname)
	assert.Equal(t, []string{"/m/" + vol.ObjectID}, snapshots[0].Paths)
}

// S4: an id that could name something outside its own capture is refused before
// anything happens — no repository, no publish, no container.
func TestSnapshotter_Snapshot_RefusesAnInvalidID(t *testing.T) {
	tests := []struct {
		name string
		id   model.SavepointID
	}{
		{name: "empty", id: ""},
		{name: "escaping the store root", id: "../escape"},
		{name: "not a unix second", id: "deadbeef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := newEnv(t, wipeOptions())
			vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
			backend := env.backend(t, vol)

			// Act
			_, err := backend.Snapshot(env.ctx, dockermodel.BackendSnapshotRequest{SnapshotID: tt.id})

			// Assert
			require.ErrorIs(t, err, model.ErrInvalidSavepointID)
			assert.Empty(t, env.repoKeys(t, vol), "the gate must stand in front of the repository")
		})
	}
}

// S5: a savepoint and an undo share one repository, so a safety snapshot is an increment over
// the savepoint it is undoing rather than a second full copy.
func TestSnapshotter_Snapshot_BothTiersShareTheRepository(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	savepoint := env.snapshot(t, env.backend(t, vol), idFirst, nil)

	// Act: the same volume, unchanged, captured by the undo's own instance.
	safety := env.snapshot(t, env.backend(t, vol), idSecond, nil)

	// Assert
	t.Logf("data added: savepoint %d bytes, safety %d bytes", savepoint.DataAdded, safety.DataAdded)
	assert.Less(t, safety.DataAdded, savepoint.DataAdded/4,
		"the safety snapshot must find the savepoint as its parent, not reship the volume")
	assert.ElementsMatch(t, []string{savepoint.SnapshotObjectID, safety.SnapshotObjectID}, env.snapshotObjectIDs(t, vol))
	assert.Contains(t, env.repoKeys(t, vol), configKey, "one repository, so one config object")
}

// S6: a lock a killed run left behind is cleared before restic sees it. Every
// standing lock is an orphan by construction, and `restic unlock` could not pick
// this one anyway — it carries a dead container's hostname.
func TestSnapshotter_Snapshot_ClearsAnOrphanedLock(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)
	env.snapshot(t, backend, idFirst, nil)
	orphan := path.Join(locksPrefix, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	putObject(t, env.repoStore(t, vol), orphan, "a lock a killed container left behind")

	// Act
	env.snapshot(t, backend, idSecond, nil)

	// Assert
	assert.Empty(t, objectNames(t, env.repoStore(t, vol), locksPrefix),
		"the orphan must be gone, and restic must have released its own")
	assert.Len(t, env.snapshotObjectIDs(t, vol), 2, "the capture itself must have gone through")
}
