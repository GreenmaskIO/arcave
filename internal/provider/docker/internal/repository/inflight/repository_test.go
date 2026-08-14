package inflight

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

func TestRepository_PutGetClear(t *testing.T) {
	// Arrange
	repo := New(t.TempDir())
	ctx := context.Background()
	marker := dockermodel.InflightMarker{
		Version: dockermodel.InflightMarkerVersion,
		Phase:   dockermodel.InflightRestoring,
		Target:  "1780335122",
		Safety:  "1780335999",
		At:      time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}

	// Act & Assert: absent, written, replaced, cleared, cleared again.
	_, found, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, repo.Put(ctx, marker))
	got, found, err := repo.Get(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, marker.Phase, got.Phase)
	assert.Equal(t, marker.Target, got.Target)
	assert.Equal(t, marker.Safety, got.Safety)
	assert.True(t, got.At.Equal(marker.At), "at = %v", got.At)

	marker.Phase = dockermodel.InflightRestoreDone
	require.NoError(t, repo.Put(ctx, marker))
	got, found, err = repo.Get(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, dockermodel.InflightRestoreDone, got.Phase)

	require.NoError(t, repo.Clear(ctx))
	_, found, err = repo.Get(ctx)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, repo.Clear(ctx), "clearing an absent marker is not an error")
}

func TestRepository_Get_CorruptMarker(t *testing.T) {
	// Arrange
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".arcave"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".arcave", markerFile), []byte("{torn"), 0o644))

	// Act
	_, _, err := New(root).Get(context.Background())

	// Assert: a marker that cannot be judged refuses rather than reads as absent.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding the inflight marker")
}
