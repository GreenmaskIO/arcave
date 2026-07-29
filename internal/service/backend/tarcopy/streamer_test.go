package tarcopy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The stream lands verbatim at <id>/<objectID>/data.tar of the tier the
// snapshotter was built for, and the stat reports the bytes actually written.
func TestSnapshotter_Snapshot(t *testing.T) {
	const payload = "tar-stream-bytes"
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := "1780335122/" + volume.ObjectID + "/" + archiveName

	tests := []struct {
		name    string
		tier    model.BackendTier
		reader  io.ReadCloser
		openErr error
		verify  func(t *testing.T, stat model.SnapshotStat, err error, sessionStore, safetyStore storages.Storager)
	}{
		{
			name:   "savepoint tier stores the stream verbatim in the session store",
			tier:   model.BackendTierSavepoint,
			reader: io.NopCloser(strings.NewReader(payload)),
			verify: func(t *testing.T, stat model.SnapshotStat, err error, sessionStore, safetyStore storages.Storager) {
				require.NoError(t, err)
				assert.Equal(t, []string{archiveKey}, objectNames(t, sessionStore))
				assert.Empty(t, objectNames(t, safetyStore), "the savepoint tier must not touch the safety store")
				assert.Equal(t, payload, readObject(t, sessionStore, archiveKey))
				assert.Equal(t, volume, stat.Volume, "the stat carries the unit it captured")
				assert.Equal(t, model.SnapshotBackendCopy, stat.Type)
				assert.Equal(t, Payload{SizeBytes: int64(len(payload))}, stat.Payload)
			},
		},
		{
			name:   "safety tier routes the same capture into the safety store",
			tier:   model.BackendTierSafety,
			reader: io.NopCloser(strings.NewReader(payload)),
			verify: func(t *testing.T, _ model.SnapshotStat, err error, sessionStore, safetyStore storages.Storager) {
				require.NoError(t, err)
				assert.Equal(t, []string{archiveKey}, objectNames(t, safetyStore))
				assert.Empty(t, objectNames(t, sessionStore), "the safety tier must not touch the session store")
			},
		},
		{
			name:    "a mount that does not open stores nothing",
			tier:    model.BackendTierSavepoint,
			openErr: errors.New("export boom"),
			verify: func(t *testing.T, _ model.SnapshotStat, err error, sessionStore, _ storages.Storager) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "export boom")
				assert.Contains(t, err.Error(), "open docker mount app /etc/cfg")
				assert.Empty(t, objectNames(t, sessionStore), "nothing may be stored from a mount that did not open")
			},
		},
		{
			name:   "a stream failing mid-copy surfaces as the archive write's error",
			tier:   model.BackendTierSavepoint,
			reader: io.NopCloser(failingReader{err: errors.New("stream boom")}),
			verify: func(t *testing.T, _ model.SnapshotStat, err error, _, _ storages.Storager) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "write archive")
				assert.Contains(t, err.Error(), "stream boom")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			docker := &mockMountStreamer{}
			docker.On("OpenMount", mock.Anything, testComposeProject, "app", "/etc/cfg").Return(tt.reader, tt.openErr).Once()
			sessionStore, safetyStore := newStore(t, "session"), newStore(t, "safety")
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{
				SessionStore: sessionStore,
				SafetyStore:  safetyStore,
				Volume:       volume,
				Tier:         tt.tier,
			})
			require.NoError(t, err)

			// Act
			stat, err := snapshotter.Snapshot(context.Background(), model.BackendSnapshotRequest{SnapshotID: "1780335122"})

			// Assert
			tt.verify(t, stat, err, sessionStore, safetyStore)
			docker.AssertExpectations(t)
		})
	}
}

// An archive answers only for the tier that wrote it, and Exists never touches
// the engine.
func TestSnapshotter_Exists(t *testing.T) {
	tests := []struct {
		name string
		tier model.BackendTier
		want bool
	}{
		{name: "the tier that wrote the capture sees it", tier: model.BackendTierSavepoint, want: true},
		{name: "a savepoint-tier capture does not answer for the safety tier", tier: model.BackendTierSafety, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			volume := bindVolume("app", "/etc/cfg")
			docker := &mockMountStreamer{}
			sessionStore, safetyStore := newStore(t, "session"), newStore(t, "safety")
			ctx := context.Background()
			archiveKey := "1780335122/" + volume.ObjectID + "/" + archiveName
			require.NoError(t, sessionStore.PutObject(ctx, archiveKey, strings.NewReader("tar-stream-bytes")))
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{
				SessionStore: sessionStore,
				SafetyStore:  safetyStore,
				Volume:       volume,
				Tier:         tt.tier,
			})
			require.NoError(t, err)

			// Act
			exists, err := snapshotter.Exists(ctx, model.BackendExistsRequest{SnapshotID: "1780335122"})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, exists)
			docker.AssertExpectations(t)
		})
	}
}

// The archive's content goes verbatim into the mount the volume addresses.
func TestSnapshotter_Restore(t *testing.T) {
	const payload = "tar-stream-bytes"
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := "1780335122/" + volume.ObjectID + "/" + archiveName

	tests := []struct {
		name       string
		seed       bool // whether the archive is in the store
		reqPayload any
		writeErr   error
		verify     func(t *testing.T, err error, restored string, docker *mockMountStreamer)
	}{
		{
			name: "hands the stored stream to the mount verbatim",
			seed: true,
			verify: func(t *testing.T, err error, restored string, _ *mockMountStreamer) {
				require.NoError(t, err)
				assert.Equal(t, payload, restored)
			},
		},
		{
			name:       "an old-core record's payload is never interpreted",
			seed:       true,
			reqPayload: map[string]any{"size_bytes": float64(len(payload))},
			verify: func(t *testing.T, err error, restored string, _ *mockMountStreamer) {
				require.NoError(t, err)
				assert.Equal(t, payload, restored)
			},
		},
		{
			name:     "a mount refusing the stream fails the restore, wrapped",
			seed:     true,
			writeErr: errors.New("import boom"),
			verify: func(t *testing.T, err error, _ string, _ *mockMountStreamer) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "write docker mount app /etc/cfg")
				assert.Contains(t, err.Error(), "import boom")
			},
		},
		{
			name: "a missing capture is a store error, not an empty restore",
			verify: func(t *testing.T, err error, _ string, docker *mockMountStreamer) {
				require.Error(t, err)
				assert.ErrorIs(t, err, storages.ErrFileNotFound)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var restored strings.Builder
			docker := &mockMountStreamer{}
			sessionStore := newStore(t, "session")
			ctx := context.Background()
			if tt.seed {
				docker.On("WriteMount", mock.Anything, testComposeProject, "app", "/etc/cfg", mock.Anything).
					Run(func(args mock.Arguments) {
						_, err := io.Copy(&restored, args.Get(4).(io.Reader))
						assert.NoError(t, err)
					}).
					Return(tt.writeErr).Once()
				require.NoError(t, sessionStore.PutObject(ctx, archiveKey, strings.NewReader(payload)))
			}
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{
				SessionStore: sessionStore,
				SafetyStore:  newStore(t, "safety"),
				Volume:       volume,
				Tier:         model.BackendTierSavepoint,
			})
			require.NoError(t, err)

			// Act
			err = snapshotter.Restore(ctx, model.BackendRestoreRequest{SnapshotID: "1780335122", Payload: tt.reqPayload})

			// Assert
			tt.verify(t, err, restored.String(), docker)
			docker.AssertExpectations(t)
		})
	}
}

// Removal is a sweep — an archive already gone is not an error — and the
// payload of an old-core record is never interpreted.
func TestSnapshotter_Remove(t *testing.T) {
	const payload = "tar-stream-bytes"

	tests := []struct {
		name       string
		seed       bool
		reqPayload any
	}{
		{name: "deletes the archive", seed: true},
		{name: "an archive already gone is not an error"},
		{name: "an old-core record's payload is never interpreted", seed: true, reqPayload: map[string]any{"size_bytes": float64(len(payload))}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			volume := bindVolume("app", "/etc/cfg")
			docker := &mockMountStreamer{}
			sessionStore := newStore(t, "session")
			ctx := context.Background()
			if tt.seed {
				archiveKey := "1780335122/" + volume.ObjectID + "/" + archiveName
				require.NoError(t, sessionStore.PutObject(ctx, archiveKey, strings.NewReader(payload)))
			}
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{
				SessionStore: sessionStore,
				SafetyStore:  newStore(t, "safety"),
				Volume:       volume,
				Tier:         model.BackendTierSavepoint,
			})
			require.NoError(t, err)

			// Act
			err = snapshotter.Remove(ctx, model.BackendRemoveRequest{SnapshotID: "1780335122", Payload: tt.reqPayload})

			// Assert
			assert.NoError(t, err)
			assert.Empty(t, objectNames(t, sessionStore))
			docker.AssertExpectations(t)
		})
	}
}

// Every method reaches its objects through captureStore, so every method — the
// destructive ones included — stands behind the id gate, and none of them
// touches the store or the engine under an id it refuses.
func TestSnapshotter_RejectsInvalidSnapshotID(t *testing.T) {
	ids := []struct {
		name       string
		snapshotID model.SavepointID
	}{
		{name: "empty id names the store root", snapshotID: ""},
		{name: "traversal escapes the store", snapshotID: "../.."},
		{name: "non-digit id", snapshotID: "abc"},
	}

	ops := []struct {
		name string
		call func(ctx context.Context, s model.VolumeSnapshotBackend, id model.SavepointID) error
	}{
		{
			name: "snapshot",
			call: func(ctx context.Context, s model.VolumeSnapshotBackend, id model.SavepointID) error {
				_, err := s.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: id})
				return err
			},
		},
		{
			name: "restore",
			call: func(ctx context.Context, s model.VolumeSnapshotBackend, id model.SavepointID) error {
				return s.Restore(ctx, model.BackendRestoreRequest{SnapshotID: id})
			},
		},
		{
			name: "exists",
			call: func(ctx context.Context, s model.VolumeSnapshotBackend, id model.SavepointID) error {
				_, err := s.Exists(ctx, model.BackendExistsRequest{SnapshotID: id})
				return err
			},
		},
		{
			name: "remove",
			call: func(ctx context.Context, s model.VolumeSnapshotBackend, id model.SavepointID) error {
				return s.Remove(ctx, model.BackendRemoveRequest{SnapshotID: id})
			},
		},
	}

	for _, op := range ops {
		for _, tt := range ids {
			t.Run(op.name+" "+tt.name, func(t *testing.T) {
				// Arrange
				docker := &mockMountStreamer{}
				sessionStore := newStore(t, "session")
				snapshotter, err := NewFactory(docker).New(model.BackendSpec{
					SessionStore: sessionStore,
					SafetyStore:  newStore(t, "safety"),
					Volume:       bindVolume("app", "/etc/cfg"),
					Tier:         model.BackendTierSavepoint,
				})
				require.NoError(t, err)

				// Act
				err = op.call(context.Background(), snapshotter, tt.snapshotID)

				// Assert
				require.Error(t, err)
				require.ErrorIs(t, err, model.ErrInvalidSavepointID)
				assert.Empty(t, objectNames(t, sessionStore), "the store must be untouched")
				docker.AssertExpectations(t)
			})
		}
	}
}

// Both things fixed for an instance's life are resolved at build: the root the
// tier names, and the mount the volume's data is reached through. A spec that
// resolves neither is refused here, never at the first capture.
func TestFactory_New(t *testing.T) {
	tests := []struct {
		name      string
		volume    model.Volume
		tier      model.BackendTier
		wantErr   bool
		wantErrIs error
	}{
		{
			name:   "a reachable volume and a tier build a snapshotter",
			volume: bindVolume("app", "/etc/cfg"),
			tier:   model.BackendTierSavepoint,
		},
		{
			name:      "a spec naming no tier resolves to no root",
			volume:    bindVolume("app", "/etc/cfg"),
			wantErr:   true,
			wantErrIs: model.ErrUnknownBackendTier,
		},
		{
			name:    "a named volume nobody mounts has no way in",
			volume:  namedVolume("demo_orphan"),
			tier:    model.BackendTierSavepoint,
			wantErr: true,
		},
		{
			name:    "a named volume mounted only at a subpath has no way in",
			volume:  namedVolume("demo_assets", model.ServiceReference{Service: "web", Target: "/srv", Subpath: "public"}),
			tier:    model.BackendTierSavepoint,
			wantErr: true,
		},
		{
			name:    "a volume with no addressing arm at all has no way in",
			volume:  model.Volume{ObjectID: "volume/broken", Type: model.VolumeTypeNamed},
			tier:    model.BackendTierSavepoint,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			docker := &mockMountStreamer{}

			// Act
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{
				SessionStore: newStore(t, "session"),
				SafetyStore:  newStore(t, "safety"),
				Volume:       tt.volume,
				Tier:         tt.tier,
			})

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				docker.AssertExpectations(t)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.volume, snapshotter.Volume())
			docker.AssertExpectations(t)
		})
	}
}

func newStore(t *testing.T, name string) storages.Storager {
	t.Helper()

	root := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(root, 0o755))
	store, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })

	return store
}

// objectNames lists a store's whole tree.
func objectNames(t *testing.T, store storages.Storager) []string {
	t.Helper()

	written, err := store.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(written))
	for _, obj := range written {
		names = append(names, obj.Name)
	}
	return names
}

// readObject returns an object's whole content.
func readObject(t *testing.T, store storages.Storager, key string) string {
	t.Helper()

	data, err := store.GetObject(context.Background(), key)
	require.NoError(t, err)
	content, err := io.ReadAll(data)
	require.NoError(t, err)
	require.NoError(t, data.Close())
	return string(content)
}
