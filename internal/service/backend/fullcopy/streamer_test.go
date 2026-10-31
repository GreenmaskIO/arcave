// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fullcopy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The stream lands verbatim at <id>/<objectID>/data.tar of the store the spec
// handed over, and the stat reports the bytes actually written.
func TestSnapshotter_Snapshot(t *testing.T) {
	const payload = "tar-stream-bytes"
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := "019e843e-1650-7000-8000-000000000000/" + volume.ObjectID + "/" + archiveName

	tests := []struct {
		name       string
		reader     io.ReadCloser
		openErr    error
		addressErr error
		verify     func(t *testing.T, stat model.SnapshotStat, err error, store storages.Storager)
	}{
		{
			name:   "the stream is stored verbatim",
			reader: io.NopCloser(strings.NewReader(payload)),
			verify: func(t *testing.T, stat model.SnapshotStat, err error, store storages.Storager) {
				require.NoError(t, err)
				assert.Equal(t, []string{archiveKey}, objectNames(t, store))
				assert.Equal(t, payload, readObject(t, store, archiveKey))
				assert.Equal(t, volume, stat.Volume, "the stat carries the unit it captured")
				assert.Equal(t, model.SnapshotBackendFullCopy, stat.Type)
				assert.Equal(t, int64(len(payload)), stat.SizeBytes)
				assert.Equal(t, int64(len(payload)), stat.StoredBytes, "the archive is stored whole")
				assert.Nil(t, stat.Payload, "a fullcopy archive is addressed by its key alone")
			},
		},
		{
			name:    "a volume that does not open stores nothing",
			openErr: errors.New("export boom"),
			verify: func(t *testing.T, _ model.SnapshotStat, err error, store storages.Storager) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "export boom")
				assert.Contains(t, err.Error(), "open docker volume /host/etc/cfg")
				assert.Empty(t, objectNames(t, store), "nothing may be stored from a volume that did not open")
			},
		},
		{
			name:       "a volume whose mount source cannot be resolved stores nothing",
			addressErr: errors.New("no such volume"),
			verify: func(t *testing.T, _ model.SnapshotStat, err error, store storages.Storager) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "resolve the mount source of volume volume-service-app-etc-cfg")
				assert.Contains(t, err.Error(), "no such volume")
				assert.Empty(t, objectNames(t, store))
			},
		},
		{
			name:   "a stream failing mid-copy surfaces as the archive write's error",
			reader: io.NopCloser(failingReader{err: errors.New("stream boom")}),
			verify: func(t *testing.T, _ model.SnapshotStat, err error, _ storages.Storager) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "write archive")
				assert.Contains(t, err.Error(), "stream boom")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			docker := &mockVolumeStreamer{}
			docker.On("ResolveMountSource", mock.Anything, volume).
				Return(bindAddress("/etc/cfg"), tt.addressErr).Once()
			if tt.addressErr == nil {
				docker.On("OpenVolume", mock.Anything, bindAddress("/etc/cfg")).Return(tt.reader, tt.openErr).Once()
			}
			store := newStore(t, "savepoints")
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{Store: store, Volume: volume})
			require.NoError(t, err)

			// Act
			stat, err := snapshotter.Snapshot(context.Background(), model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})

			// Assert
			tt.verify(t, stat, err, store)
			docker.AssertExpectations(t)
		})
	}
}

// The root the spec named is the whole of what an instance writes to and reads
// from. It is the only thing that tells a savepoint's instances from a
// rollback's undo, so an instance reaching the other root would be silent.
func TestSnapshotter_Snapshot_CapturesIntoTheStoreTheSpecNamed(t *testing.T) {
	// Arrange
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := "019e843e-1650-7000-8000-000000000000/" + volume.ObjectID + "/" + archiveName
	docker := &mockVolumeStreamer{}
	docker.On("ResolveMountSource", mock.Anything, volume).Return(bindAddress("/etc/cfg"), nil).Once()
	docker.On("OpenVolume", mock.Anything, bindAddress("/etc/cfg")).
		Return(io.NopCloser(strings.NewReader("tar-stream-bytes")), nil).Once()
	named, other := newStore(t, "named"), newStore(t, "other")
	snapshotter, err := NewFactory(docker).New(model.BackendSpec{Store: named, Volume: volume})
	require.NoError(t, err)

	// Act
	_, err = snapshotter.Snapshot(context.Background(), model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{archiveKey}, objectNames(t, named))
	assert.Empty(t, objectNames(t, other), "the capture reached a root the spec never named")
	docker.AssertExpectations(t)
}

// The archive's content goes verbatim into the volume the record addresses.
func TestSnapshotter_Restore(t *testing.T) {
	const payload = "tar-stream-bytes"
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := "019e843e-1650-7000-8000-000000000000/" + volume.ObjectID + "/" + archiveName

	tests := []struct {
		name       string
		seed       bool // whether the archive is in the store
		reqPayload any
		writeErr   error
		verify     func(t *testing.T, err error, restored string, docker *mockVolumeStreamer)
	}{
		{
			name: "hands the stored stream to the volume verbatim",
			seed: true,
			verify: func(t *testing.T, err error, restored string, _ *mockVolumeStreamer) {
				require.NoError(t, err)
				assert.Equal(t, payload, restored)
			},
		},
		{
			name:       "an old-core record's payload is never interpreted",
			seed:       true,
			reqPayload: map[string]any{"size_bytes": float64(len(payload))},
			verify: func(t *testing.T, err error, restored string, _ *mockVolumeStreamer) {
				require.NoError(t, err)
				assert.Equal(t, payload, restored)
			},
		},
		{
			name:     "a volume refusing the stream fails the restore, wrapped",
			seed:     true,
			writeErr: errors.New("import boom"),
			verify: func(t *testing.T, err error, _ string, _ *mockVolumeStreamer) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "write docker volume /host/etc/cfg")
				assert.Contains(t, err.Error(), "import boom")
			},
		},
		{
			name: "a missing capture is a store error, not an empty restore",
			verify: func(t *testing.T, err error, _ string, docker *mockVolumeStreamer) {
				require.Error(t, err)
				assert.ErrorIs(t, err, storages.ErrFileNotFound)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var restored strings.Builder
			docker := &mockVolumeStreamer{}
			store := newStore(t, "savepoints")
			ctx := context.Background()
			docker.On("ResolveMountSource", mock.Anything, volume).Return(bindAddress("/etc/cfg"), nil).Once()
			if tt.seed {
				docker.On("WriteVolume", mock.Anything, bindAddress("/etc/cfg"), mock.Anything).
					Run(func(args mock.Arguments) {
						_, err := io.Copy(&restored, args.Get(2).(io.Reader))
						assert.NoError(t, err)
					}).
					Return(tt.writeErr).Once()
				require.NoError(t, store.PutObject(ctx, archiveKey, strings.NewReader(payload)))
			}
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{Store: store, Volume: volume})
			require.NoError(t, err)

			// Act
			err = snapshotter.Restore(ctx, model.BackendRestoreRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000", Payload: tt.reqPayload})

			// Assert
			tt.verify(t, err, restored.String(), docker)
			docker.AssertExpectations(t)
		})
	}
}

// bytesRecorder keeps the bytes of every sample the tracker reports.
type bytesRecorder struct {
	samples []progress.Bytes
}

func (r *bytesRecorder) Render(evt progress.Event) {
	if evt.Sample.Bytes != nil {
		r.samples = append(r.samples, *evt.Sample.Bytes)
	}
}

// The restore reports its bytes out of the size the request carries, the capture's own count.
func TestSnapshotter_Restore_ReportsTheRequestedTotal(t *testing.T) {
	// Arrange
	const payload = "tar-stream-bytes"
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := "019e843e-1650-7000-8000-000000000000/" + volume.ObjectID + "/" + archiveName
	docker := &mockVolumeStreamer{}
	store := newStore(t, "savepoints")
	recorder := &bytesRecorder{}
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	ctx := progress.WithTracker(context.Background(), progress.New(recorder, now))
	ctx, _ = progress.Begin(ctx, progress.Step{Kind: "rollback"})
	ctx, _ = progress.Run(ctx, progress.Step{Kind: "volume.restore"}, progress.MeasureBytes)
	require.NoError(t, store.PutObject(ctx, archiveKey, strings.NewReader(payload)))
	docker.On("ResolveMountSource", mock.Anything, volume).Return(bindAddress("/etc/cfg"), nil).Once()
	docker.On("WriteVolume", mock.Anything, bindAddress("/etc/cfg"), mock.Anything).
		Run(func(args mock.Arguments) {
			_, err := io.Copy(io.Discard, args.Get(2).(io.Reader))
			assert.NoError(t, err)
		}).
		Return(nil).Once()
	snapshotter, err := NewFactory(docker).New(model.BackendSpec{Store: store, Volume: volume})
	require.NoError(t, err)

	// Act
	err = snapshotter.Restore(ctx, model.BackendRestoreRequest{
		SnapshotID: "019e843e-1650-7000-8000-000000000000",
		SizeBytes:  int64(len(payload)),
	})

	// Assert
	require.NoError(t, err)
	require.NotEmpty(t, recorder.samples)
	assert.Equal(t, int64(len(payload)), recorder.samples[0].Total)
	docker.AssertExpectations(t)
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
			docker := &mockVolumeStreamer{}
			store := newStore(t, "savepoints")
			ctx := context.Background()
			if tt.seed {
				archiveKey := "019e843e-1650-7000-8000-000000000000/" + volume.ObjectID + "/" + archiveName
				require.NoError(t, store.PutObject(ctx, archiveKey, strings.NewReader(payload)))
			}
			snapshotter, err := NewFactory(docker).New(model.BackendSpec{Store: store, Volume: volume})
			require.NoError(t, err)

			// Act
			err = snapshotter.Remove(ctx, model.BackendRemoveRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000", Payload: tt.reqPayload})

			// Assert
			assert.NoError(t, err)
			assert.Empty(t, objectNames(t, store))
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
				docker := &mockVolumeStreamer{}
				store := newStore(t, "savepoints")
				snapshotter, err := NewFactory(docker).New(model.BackendSpec{Store: store, Volume: bindVolume("app", "/etc/cfg")})
				require.NoError(t, err)

				// Act
				err = op.call(context.Background(), snapshotter, tt.snapshotID)

				// Assert
				require.Error(t, err)
				require.ErrorIs(t, err, model.ErrInvalidSavepointID)
				assert.Empty(t, objectNames(t, store), "the store must be untouched")
				docker.AssertExpectations(t)
			})
		}
	}
}

// The one thing fixed for an instance's life is resolved at build: the root the
// spec handed over. The volume's address is not — reaching the daemon needs a
// ctx a constructor does not have — so the empty engine mock is the assertion
// that building a backend contacts nothing.
func TestFactory_New(t *testing.T) {
	tests := []struct {
		name      string
		volume    model.Volume
		noStore   bool
		wantErrIs error
	}{
		{
			name:   "a bind volume and a store build a snapshotter",
			volume: bindVolume("app", "/etc/cfg"),
		},
		{
			name:   "a named volume nobody mounts builds a snapshotter too",
			volume: namedVolume("demo_orphan"),
		},
		{
			name:   "so does one mounted only at a subpath: it is captured whole",
			volume: namedVolume("demo_assets", model.ServiceReference{Service: "web", Target: "/srv", Subpath: "public"}),
		},
		{
			name:      "a spec carrying no store has nowhere to write",
			volume:    bindVolume("app", "/etc/cfg"),
			noStore:   true,
			wantErrIs: errNoStore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			docker := &mockVolumeStreamer{}
			spec := model.BackendSpec{Volume: tt.volume}
			if !tt.noStore {
				spec.Store = newStore(t, "savepoints")
			}

			// Act
			snapshotter, err := NewFactory(docker).New(spec)

			// Assert
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
				assert.Nil(t, snapshotter)
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
