package snapshot

import (
	"context"
	"errors"
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

// snapshotsDir is the store the backend is bound to. It is a sub-storage of the
// temp project root, so the tests exercise the same re-rooting the DI root does.
const snapshotsDir = "snapshots"

// newStore returns a Storager rooted at snapshotsDir inside a fresh temp
// directory, plus that temp directory, so a test can assert on the real paths.
func newStore(t *testing.T) (model.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(&directory.Config{Path: root})
	require.NoError(t, err)
	return st.SubStorage(snapshotsDir, true), root
}

// fakeDockerVolume stands in for the Docker engine: OpenMount hands out a fixed
// payload as every mount's tar stream, and WriteMount captures whatever is
// extracted back. Errors let a test drive the failure paths.
type fakeDockerVolume struct {
	payload  string
	written  strings.Builder
	openErr  error
	writeErr error
}

func (f *fakeDockerVolume) OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

func (f *fakeDockerVolume) WriteMount(ctx context.Context, project, service, target string, r io.Reader) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	_, err := io.Copy(&f.written, r)
	return err
}

func TestCopyBackend_Snapshot(t *testing.T) {
	// Arrange
	st, root := newStore(t)
	docker := &fakeDockerVolume{payload: "payload"}
	backend := NewCopyBackend(docker, st)
	mounts := []model.Mount{
		{Service: "db", Target: "/export/data"},
		{Service: "cache", Target: "/data"},
	}

	// Act
	sizes, err := backend.Snapshot(context.Background(), "100", "demo", mounts)

	// Assert
	require.NoError(t, err)
	require.Len(t, sizes, 2)
	assert.EqualValues(t, len("payload"), sizes[0])
	assert.EqualValues(t, len("payload"), sizes[1])
	// Each archive lands at its explicit, recomputable key relative to the store.
	for _, m := range mounts {
		key := model.SnapshotKey("100", m.Service, m.Target)
		data, readErr := os.ReadFile(filepath.Join(root, snapshotsDir, filepath.FromSlash(key)))
		require.NoError(t, readErr, key)
		assert.Equal(t, "payload", string(data))
	}
	assert.Equal(t, "100/db/export/data/data.tar", model.SnapshotKey("100", "db", "/export/data"))
}

func TestCopyBackend_Snapshot_OpenError(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	docker := &fakeDockerVolume{openErr: errors.New("export boom")}
	backend := NewCopyBackend(docker, st)

	// Act
	_, err := backend.Snapshot(context.Background(), "100", "demo", []model.Mount{{Service: "db", Target: "/data"}})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "export boom")
}

// Snapshot is the destructive-key producer, so a savepoint id that never was a
// Unix timestamp must be refused before it is rendered into a storage key.
func TestCopyBackend_Snapshot_RejectsInvalidSavepointID(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)

	// Act
	_, err := backend.Snapshot(context.Background(), "../..", "demo", []model.Mount{{Service: "db", Target: "/data"}})

	// Assert
	require.Error(t, err)
}

func TestCopyBackend_Restore(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	ctx := context.Background()
	docker := &fakeDockerVolume{payload: "hello"}
	backend := NewCopyBackend(docker, st)
	mounts := []model.Mount{{Service: "db", Target: "/data"}}
	_, err := backend.Snapshot(ctx, "100", "demo", mounts)
	require.NoError(t, err)

	// Act
	err = backend.Restore(ctx, "100", "demo", mounts)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "hello", docker.written.String())
}

func TestCopyBackend_Missing(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	ctx := context.Background()
	backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)
	present := model.Mount{Service: "db", Target: "/data"}
	absent := model.Mount{Service: "cache", Target: "/var"}
	_, err := backend.Snapshot(ctx, "100", "demo", []model.Mount{present})
	require.NoError(t, err)

	// Act
	missing, err := backend.Missing(ctx, "100", []model.Mount{present, absent})

	// Assert
	require.NoError(t, err)
	require.Len(t, missing, 1)
	assert.Equal(t, absent, missing[0])
}

func TestCopyBackend_Remove(t *testing.T) {
	// Arrange
	st, root := newStore(t)
	ctx := context.Background()
	backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)
	mounts := []model.Mount{{Service: "db", Target: "/data"}}
	_, err := backend.Snapshot(ctx, "100", "demo", mounts)
	require.NoError(t, err)

	// Act
	require.NoError(t, backend.Remove(ctx, "100"))

	// Assert
	_, statErr := os.Stat(filepath.Join(root, snapshotsDir, "100"))
	assert.True(t, os.IsNotExist(statErr), "savepoint directory must be gone")
	missing, err := backend.Missing(ctx, "100", mounts)
	require.NoError(t, err)
	assert.Len(t, missing, 1)

	// Remove of an already-absent directory is a no-op, not an error.
	assert.NoError(t, backend.Remove(ctx, "100"))
}

// Remove is the destructive call, so an id that never was a Unix timestamp must
// be refused before it is rendered into a storage key — the store must survive an
// id hand-planted in a record.
func TestCopyBackend_Remove_RejectsInvalidSavepointID(t *testing.T) {
	tests := []struct {
		name        string
		savepointID string
	}{
		{name: "empty id names the store root", savepointID: ""},
		{name: "traversal escapes the store", savepointID: "../.."},
		{name: "non-digit id", savepointID: "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStore(t)
			ctx := context.Background()
			backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)
			_, err := backend.Snapshot(ctx, "100", "demo", []model.Mount{{Service: "db", Target: "/data"}})
			require.NoError(t, err)

			// Act
			err = backend.Remove(ctx, tt.savepointID)

			// Assert
			require.Error(t, err)
			assert.DirExists(t, filepath.Join(root, snapshotsDir, "100"), "the store must be untouched")
		})
	}
}
