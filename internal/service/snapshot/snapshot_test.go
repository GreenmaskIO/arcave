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

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"

	"github.com/greenmaskio/arcave/internal/model"
)

// snapshotsDir is the store the backend is bound to. It is a sub-storage of the
// temp project root, so the tests exercise the same re-rooting the DI root does.
const snapshotsDir = "snapshots"

// newStore returns a Storager rooted at snapshotsDir inside a fresh temp
// directory, plus that temp directory, so a test can assert on the real paths.
func newStore(t *testing.T) (storages.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(directory.Config{Path: root})
	require.NoError(t, err)
	sub, err := st.SubStorage(snapshotsDir, true)
	require.NoError(t, err)
	return sub, root
}

// namedVolume, bindVolume and anonVolume mint the three units a backend can be
// handed. Each takes its object id from the model rather than spelling one out:
// the layout is derived from it, so a hand-written id would let the two drift.
func namedVolume(dockerName string, usedBy ...model.ServiceReference) model.Volume {
	return model.Volume{
		ObjectID: model.NamedVolume{Name: dockerName}.ObjectID(),
		Type:     model.VolumeTypeNamed,
		Backend:  model.SnapshotBackendFullCopy,
		Named:    &model.VolumePropertiesNamed{VolumeName: dockerName, UsedBy: usedBy},
	}
}

func bindVolume(service, target string) model.Volume {
	return model.Volume{
		ObjectID: model.ServiceVolume{Type: model.VolumeTypeBind, Target: target}.ObjectID(service),
		Type:     model.VolumeTypeBind,
		Backend:  model.SnapshotBackendFullCopy,
		Bind:     &model.VolumePropertiesBind{Service: service, Target: target},
	}
}

func anonVolume(service, target string) model.Volume {
	return model.Volume{
		ObjectID: model.ServiceVolume{Type: model.VolumeTypeAnon, Target: target}.ObjectID(service),
		Type:     model.VolumeTypeAnon,
		Backend:  model.SnapshotBackendFullCopy,
		Anon:     &model.VolumePropertiesAnon{Service: service, Target: target},
	}
}

// fakeDockerVolume stands in for the Docker engine: OpenMount hands out a fixed
// payload as every mount's tar stream, and WriteMount captures whatever is
// extracted back. Errors drive the failure paths, and every (service, target)
// pair is recorded, so a test can assert which mount the backend chose.
type fakeDockerVolume struct {
	payload  string
	written  strings.Builder
	openErr  error
	writeErr error
	opened   [][2]string
	wrote    [][2]string
}

func (f *fakeDockerVolume) OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error) {
	f.opened = append(f.opened, [2]string{service, target})
	if f.openErr != nil {
		return nil, f.openErr
	}
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

func (f *fakeDockerVolume) WriteMount(ctx context.Context, project, service, target string, r io.Reader) error {
	f.wrote = append(f.wrote, [2]string{service, target})
	if f.writeErr != nil {
		return f.writeErr
	}
	_, err := io.Copy(&f.written, r)
	return err
}

// TestCopyBackend_Snapshot covers one unit of each type: the key it lands at, the
// stat it reports, the mount it was reached through. One volume per case, so the
// fake's single payload and recorded access path are unambiguous.
func TestCopyBackend_Snapshot(t *testing.T) {
	tests := []struct {
		name       string
		volume     model.Volume
		wantKey    string
		wantAccess [2]string
	}{
		{
			name:       "a named volume is keyed by its docker name",
			volume:     namedVolume("demo_pgdata", model.ServiceReference{Service: "db", Target: "/var/lib/postgresql/data"}),
			wantKey:    "100/volume/demo_pgdata/data.tar",
			wantAccess: [2]string{"db", "/var/lib/postgresql/data"},
		},
		{
			name:       "a bind mount is keyed by service and container path",
			volume:     bindVolume("db", "/export/data"),
			wantKey:    "100/service/db/export/data/data.tar",
			wantAccess: [2]string{"db", "/export/data"},
		},
		{
			name:       "an anonymous volume is keyed like a bind",
			volume:     anonVolume("cache", "/data"),
			wantKey:    "100/service/cache/data/data.tar",
			wantAccess: [2]string{"cache", "/data"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStore(t)
			docker := &fakeDockerVolume{payload: "payload"}
			backend := NewCopyBackend(docker, st)

			// Act
			stats, err := backend.Snapshot(context.Background(), "100", "demo", []model.Volume{tt.volume})

			// Assert
			require.NoError(t, err)
			require.Len(t, stats, 1)
			assert.Equal(t, tt.volume, stats[0].Volume, "the stat carries the unit it captured")
			assert.Equal(t, model.SnapshotBackendFullCopy, stats[0].Type)
			assert.Equal(t, FullCopyStat{SizeBytes: int64(len("payload"))}, stats[0].Payload)

			assert.Equal(t, tt.wantKey, snapshotKey("100", tt.volume.ObjectID))
			data, readErr := os.ReadFile(filepath.Join(root, snapshotsDir, filepath.FromSlash(tt.wantKey)))
			require.NoError(t, readErr, tt.wantKey)
			assert.Equal(t, "payload", string(data))

			assert.Equal(t, [][2]string{tt.wantAccess}, docker.opened)
		})
	}
}

// Every archive is ≥2 segments deep, so no volume can ever be written over
// savepoint.json — the record the repository commits in the same directory.
func TestSnapshotKey_NeverCollidesWithTheRecord(t *testing.T) {
	tests := []struct {
		name   string
		volume model.Volume
	}{
		{name: "named", volume: namedVolume("savepoint.json")},
		{name: "bind at the root", volume: bindVolume("db", "/")},
		{name: "anon", volume: anonVolume("db", "/savepoint.json")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			key := snapshotKey("100", tt.volume.ObjectID)

			// Assert
			assert.NotEqual(t, "100/savepoint.json", key)
			assert.GreaterOrEqual(t, len(strings.Split(key, "/")), 3, "key = %s", key)
		})
	}
}

func TestCopyBackend_Snapshot_OpenError(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	docker := &fakeDockerVolume{openErr: errors.New("export boom")}
	backend := NewCopyBackend(docker, st)

	// Act
	_, err := backend.Snapshot(context.Background(), "100", "demo", []model.Volume{bindVolume("db", "/data")})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "export boom")
	assert.Contains(t, err.Error(), "service/db/data", "the failure names the unit by its object id")
}

// Snapshot is the destructive-key producer, so a savepoint id that never was a
// Unix timestamp must be refused before it is rendered into a storage key.
func TestCopyBackend_Snapshot_RejectsInvalidSavepointID(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)

	// Act
	_, err := backend.Snapshot(context.Background(), "../..", "demo", []model.Volume{bindVolume("db", "/data")})

	// Assert
	require.Error(t, err)
}

// An unreachable volume never gets this far — core filters it out — but the
// backend refuses it rather than panicking on a nil arm or inventing a mount.
func TestCopyBackend_Snapshot_RejectsUnreachableVolume(t *testing.T) {
	tests := []struct {
		name   string
		volume model.Volume
	}{
		{name: "a named volume nobody mounts", volume: namedVolume("demo_orphan")},
		{
			name:   "a named volume mounted only at a subpath",
			volume: namedVolume("demo_assets", model.ServiceReference{Service: "web", Target: "/srv", Subpath: "public"}),
		},
		{
			name:   "a volume with no addressing arm at all",
			volume: model.Volume{ObjectID: "volume/broken", Type: model.VolumeTypeNamed},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, _ := newStore(t)
			docker := &fakeDockerVolume{payload: "hi"}
			backend := NewCopyBackend(docker, st)

			// Act
			_, err := backend.Snapshot(context.Background(), "100", "demo", []model.Volume{tt.volume})

			// Assert
			require.Error(t, err)
			assert.Empty(t, docker.opened, "no mount may be opened for a volume with no way in")
		})
	}
}

func TestCopyBackend_Restore(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	ctx := context.Background()
	docker := &fakeDockerVolume{payload: "hello"}
	backend := NewCopyBackend(docker, st)
	volumes := []model.Volume{bindVolume("db", "/data")}
	_, err := backend.Snapshot(ctx, "100", "demo", volumes)
	require.NoError(t, err)

	// Act
	err = backend.Restore(ctx, "100", "demo", volumes)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "hello", docker.written.String())
	assert.Equal(t, [][2]string{{"db", "/data"}}, docker.wrote)
}

func TestCopyBackend_Missing(t *testing.T) {
	// Arrange
	st, _ := newStore(t)
	ctx := context.Background()
	backend := NewCopyBackend(&fakeDockerVolume{payload: "hi"}, st)
	present := bindVolume("db", "/data")
	absent := namedVolume("demo_cache", model.ServiceReference{Service: "cache", Target: "/var"})
	_, err := backend.Snapshot(ctx, "100", "demo", []model.Volume{present})
	require.NoError(t, err)

	// Act
	missing, err := backend.Missing(ctx, "100", []model.Volume{present, absent})

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
	volumes := []model.Volume{bindVolume("db", "/data")}
	_, err := backend.Snapshot(ctx, "100", "demo", volumes)
	require.NoError(t, err)

	// Act
	require.NoError(t, backend.Remove(ctx, "100"))

	// Assert
	_, statErr := os.Stat(filepath.Join(root, snapshotsDir, "100"))
	assert.True(t, os.IsNotExist(statErr), "savepoint directory must be gone")
	missing, err := backend.Missing(ctx, "100", volumes)
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
			_, err := backend.Snapshot(ctx, "100", "demo", []model.Volume{bindVolume("db", "/data")})
			require.NoError(t, err)

			// Act
			err = backend.Remove(ctx, tt.savepointID)

			// Assert
			require.Error(t, err)
			assert.DirExists(t, filepath.Join(root, snapshotsDir, "100"), "the store must be untouched")
		})
	}
}
