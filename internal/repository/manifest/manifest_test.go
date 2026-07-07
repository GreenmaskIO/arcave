package manifest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/model"
)

func TestManifestRoundTrip(t *testing.T) {
	// Arrange
	repo := New(t.TempDir())
	records := []model.SavepointRecord{{
		Savepoint: model.Savepoint{
			ID:        "1780335122",
			Name:      "before-migration",
			CreatedAt: time.Date(2026, 6, 1, 19, 41, 12, 0, time.UTC),
		},
		Volumes: []model.VolumeRef{{
			Service:   "db",
			MountPath: "/var/lib/postgresql/data",
			Type:      "volume",
			Source:    "myapp_pgdata",
			Snapshot: model.SnapshotRef{
				Backend: "copy",
				Opaque:  ".gm/snapshots/1780335122/db/var/lib/postgresql/data/data.tar",
			},
		}},
	}}

	// Act: a missing manifest reads empty, then the records survive a save/load round trip.
	empty, loadErr := repo.Load()
	saveErr := repo.Save(records)
	got, gotErr := repo.Load()

	// Assert
	require.NoError(t, loadErr)
	assert.Empty(t, empty, "missing manifest should read as empty")
	require.NoError(t, saveErr)
	require.NoError(t, gotErr)
	require.Len(t, got, 1)
	assert.Equal(t, "1780335122", got[0].ID)

	vol := got[0].Volumes[0]
	assert.Equal(t, "myapp_pgdata", vol.Source)
	assert.Equal(t, "copy", vol.Snapshot.Backend)
	assert.NotEmpty(t, vol.Snapshot.Opaque)
}
