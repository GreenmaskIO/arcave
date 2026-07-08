package snapshot

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/storages/directory"
)

func newStore(t *testing.T) (*Snapshot, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(&directory.Config{Path: root})
	require.NoError(t, err)
	return New(st), root
}

func TestMountDir(t *testing.T) {
	tests := []struct {
		name  string
		mount string
		want  string
	}{
		{name: "single segment", mount: "/data", want: "data"},
		{name: "nested path stays nested", mount: "/export/data", want: "export/data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MountDir(tt.mount))
		})
	}
}

func TestWriterProducesKeyAndRef(t *testing.T) {
	// Arrange
	store, root := newStore(t)

	// Act
	w, ref := store.Writer("100", "db", "/export/data")
	err := w.Write(context.Background(), strings.NewReader("payload"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, Name, ref.Backend)
	assert.Equal(t, "100/db/export/data/data.tar", ref.Opaque)
	data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(ref.Opaque)))
	require.NoError(t, readErr)
	assert.Equal(t, "payload", string(data))
}

func TestWriterReaderRoundTrip(t *testing.T) {
	// Arrange
	store, _ := newStore(t)
	ctx := context.Background()
	w, ref := store.Writer("100", "db", "/data")
	require.NoError(t, w.Write(ctx, strings.NewReader("hello")))

	// Act
	rc, err := store.Reader(ref).Open(ctx)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestExists(t *testing.T) {
	// Arrange
	store, _ := newStore(t)
	ctx := context.Background()
	w, ref := store.Writer("100", "db", "/data")
	require.NoError(t, w.Write(ctx, strings.NewReader("hi")))

	// Act + Assert
	exists, err := store.Exists(ctx, ref)
	require.NoError(t, err)
	assert.True(t, exists)

	missing, err := store.Exists(ctx, model.SnapshotRef{Backend: Name, Opaque: "999/db/data/data.tar"})
	require.NoError(t, err)
	assert.False(t, missing)
}

func TestRemove(t *testing.T) {
	// Arrange
	store, root := newStore(t)
	ctx := context.Background()
	w, ref := store.Writer("100", "db", "/data")
	require.NoError(t, w.Write(ctx, strings.NewReader("hi")))

	// Act
	require.NoError(t, store.Remove(ctx, "100"))

	// Assert
	_, statErr := os.Stat(filepath.Join(root, "100"))
	assert.True(t, os.IsNotExist(statErr), "savepoint directory must be gone")
	exists, err := store.Exists(ctx, ref)
	require.NoError(t, err)
	assert.False(t, exists)

	// Remove of an already-absent savepoint is a no-op, not an error.
	assert.NoError(t, store.Remove(ctx, "100"))
}
