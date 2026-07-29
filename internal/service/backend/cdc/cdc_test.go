package cdc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const testObjectID = "volume/demo_pgdata"

func newStore(t *testing.T, name string) storages.Storager {
	t.Helper()

	root := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(root, 0o755))
	store, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })

	return store
}

// The stub reports the tier it was built for and writes nothing: a cdc capture
// lives in a repository, not in the stores the spec hands over.
func TestSnapshotter_Snapshot(t *testing.T) {
	// Arrange
	session := newStore(t, "session")
	safety := newStore(t, "safety")
	newSnapshotter := func(tier model.BackendTier) model.VolumeSnapshotBackend {
		snapshotter, err := NewFactory().New(model.BackendSpec{
			SessionStore: session,
			SafetyStore:  safety,
			Volume:       model.Volume{ObjectID: testObjectID, Backend: model.SnapshotBackendCDC},
			Tier:         tier,
		})
		require.NoError(t, err)
		return snapshotter
	}
	ctx := context.Background()

	// Act
	main, mainErr := newSnapshotter(model.BackendTierSavepoint).Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "100"})
	safe, safeErr := newSnapshotter(model.BackendTierSafety).Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "200"})

	// Assert
	require.NoError(t, mainErr)
	require.NoError(t, safeErr)
	assert.Equal(t, model.SnapshotBackendCDC, main.Type)
	assert.Equal(t, Payload{ObjectID: testObjectID, SnapshotID: "100", Root: session.GetCwd()}, main.Payload)
	assert.Equal(t, Payload{ObjectID: testObjectID, SnapshotID: "200", Root: safety.GetCwd()}, safe.Payload)

	for _, store := range []storages.Storager{session, safety} {
		written, err := store.List(ctx, "")
		require.NoError(t, err)
		assert.Empty(t, written, "a cdc capture lives in its repository, not in the store the spec hands over")
	}
}
