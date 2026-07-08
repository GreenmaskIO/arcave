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
// payload as the mount's tar stream, and WriteMount captures whatever is
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

func TestCopyBackend_Create(t *testing.T) {
	// Arrange
	st, root := newStore(t)
	docker := &fakeDockerVolume{payload: "payload"}
	backend := NewCopyBackend(docker, st)

	// Act
	meta, err := backend.Create(context.Background(), model.VolumeSnapshotRequest{
		Namespace:      model.SavepointNamespace("100"),
		ComposeProject: "demo",
		Volume:         model.VolumeKey{Service: "db", MountPath: "/export/data"},
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, BackendNameFullCopy, meta.Backend)
	assert.Equal(t, "100/db/export/data/data.tar", meta.Opaque)
	assert.EqualValues(t, len("payload"), meta.SizeBytes)
	// Opaque is relative to the store the backend is bound to.
	data, readErr := os.ReadFile(filepath.Join(root, snapshotsDir, filepath.FromSlash(meta.Opaque)))
	require.NoError(t, readErr)
	assert.Equal(t, "payload", string(data))
}

func TestCopyBackend_Create_OpenError(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	docker := &fakeDockerVolume{openErr: errors.New("export boom")}
	backend := NewCopyBackend(docker, st)

	// Act
	_, err := backend.Create(context.Background(), model.VolumeSnapshotRequest{
		Namespace:      model.SavepointNamespace("100"),
		ComposeProject: "demo",
		Volume:         model.VolumeKey{Service: "db", MountPath: "/data"},
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "export boom")
}

func TestCopyBackend_Restore(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	ctx := context.Background()
	docker := &fakeDockerVolume{payload: "hello"}
	backend := NewCopyBackend(docker, st)
	meta, err := backend.Create(ctx, model.VolumeSnapshotRequest{
		Namespace:      model.SavepointNamespace("100"),
		ComposeProject: "demo",
		Volume:         model.VolumeKey{Service: "db", MountPath: "/data"},
	})
	require.NoError(t, err)

	// Act
	err = backend.Restore(ctx, model.VolumeRestoreRequest{
		ComposeProject: "demo",
		Volume:         model.VolumeKey{Service: "db", MountPath: "/data"},
		Snapshot:       meta.SnapshotRef,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "hello", docker.written.String())
}

// A ref minted by another backend carries an Opaque this one cannot interpret,
// so Restore and Exists must refuse it rather than silently reading whatever
// path the string happens to name.
func TestCopyBackend_ForeignRef(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	ctx := context.Background()
	docker := &fakeDockerVolume{payload: "hi"}
	backend := NewCopyBackend(docker, st)
	foreign := model.SnapshotRef{Backend: "btrfs", Opaque: "some-subvol-uuid"}

	// Act
	restoreErr := backend.Restore(ctx, model.VolumeRestoreRequest{
		ComposeProject: "demo",
		Volume:         model.VolumeKey{Service: "db", MountPath: "/data"},
		Snapshot:       foreign,
	})
	_, existsErr := backend.Exists(ctx, foreign)

	// Assert
	require.Error(t, restoreErr)
	assert.Contains(t, restoreErr.Error(), `created by backend "btrfs"`)
	require.Error(t, existsErr)
	assert.Contains(t, existsErr.Error(), `created by backend "btrfs"`)
	assert.Empty(t, docker.written.String(), "a foreign ref must never reach the mount")
}

func TestCopyBackend_Exists(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	ctx := context.Background()
	backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)
	meta, err := backend.Create(ctx, model.VolumeSnapshotRequest{
		Namespace:      model.SavepointNamespace("100"),
		ComposeProject: "demo",
		Volume:         model.VolumeKey{Service: "db", MountPath: "/data"},
	})
	require.NoError(t, err)

	// Act + Assert
	exists, err := backend.Exists(ctx, meta.SnapshotRef)
	require.NoError(t, err)
	assert.True(t, exists)

	missing, err := backend.Exists(ctx, model.SnapshotRef{Backend: BackendNameFullCopy, Opaque: "999/db/data/data.tar"})
	require.NoError(t, err)
	assert.False(t, missing)
}

func TestCopyBackend_Remove(t *testing.T) {
	// Arrange
	st, root := newStore(t)
	ctx := context.Background()
	backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)
	meta, err := backend.Create(ctx, model.VolumeSnapshotRequest{
		Namespace:      model.SavepointNamespace("100"),
		ComposeProject: "demo",
		Volume:         model.VolumeKey{Service: "db", MountPath: "/data"},
	})
	require.NoError(t, err)

	// Act
	require.NoError(t, backend.Remove(ctx, model.SavepointNamespace("100")))

	// Assert
	_, statErr := os.Stat(filepath.Join(root, snapshotsDir, "100"))
	assert.True(t, os.IsNotExist(statErr), "savepoint namespace must be gone")
	exists, err := backend.Exists(ctx, meta.SnapshotRef)
	require.NoError(t, err)
	assert.False(t, exists)

	// Remove of an already-absent namespace is a no-op, not an error.
	assert.NoError(t, backend.Remove(ctx, model.SavepointNamespace("100")))
}

// Remove is the destructive call, so an id that never was a Unix timestamp must
// be refused before it is rendered into a storage key — the store must survive a
// namespace hand-planted in a record.
func TestCopyBackend_Remove_RejectsInvalidNamespace(t *testing.T) {
	tests := []struct {
		name string
		ns   model.Namespace
	}{
		{name: "empty id names the store root", ns: model.SavepointNamespace("")},
		{name: "traversal escapes the store", ns: model.SavepointNamespace("../..")},
		{name: "unknown kind", ns: model.Namespace{Kind: "volume", ID: "100"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStore(t)
			ctx := context.Background()
			backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)
			_, err := backend.Create(ctx, model.VolumeSnapshotRequest{
				Namespace:      model.SavepointNamespace("100"),
				ComposeProject: "demo",
				Volume:         model.VolumeKey{Service: "db", MountPath: "/data"},
			})
			require.NoError(t, err)

			// Act
			err = backend.Remove(ctx, tt.ns)

			// Assert
			require.Error(t, err)
			assert.DirExists(t, filepath.Join(root, snapshotsDir, "100"), "the store must be untouched")
		})
	}
}
