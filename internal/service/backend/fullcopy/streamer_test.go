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
	"bytes"
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
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

const testSnapshotID model.SavepointID = "019e843e-1650-7000-8000-000000000000"

// tarStream stands in for the daemon's tar: repetitive enough that zstd shrinks it.
var tarStream = strings.Repeat("tar-stream-bytes ", 256)

// The stream lands at <id>/<objectID>/ under the key its format names, decodes
// back to what the daemon handed over, and the stat counts the tar and the store
// apart; a capture that cannot read its volume or write its archive reports nothing.
func TestSnapshotter_Snapshot(t *testing.T) {
	volume := bindVolume("app", "/etc/cfg")
	captureDir := testSnapshotID.String() + "/" + volume.ObjectID + "/"
	errStore := errors.New("store boom")

	type testCase struct {
		name        string
		compression Compression
		reader      io.ReadCloser // nil hands over tarStream
		openErr     error
		addressErr  error
		putErr      error
		wantKey     string
		wantStored  func(t *testing.T, stat model.SnapshotStat, object string)
		wantErrMsg  []string
		wantErrIs   error
		wantEmpty   bool
	}
	tests := []testCase{
		{
			name:        "none stores the tar as is",
			compression: CompressionNone,
			wantKey:     captureDir + archiveNameTar,
			wantStored: func(t *testing.T, stat model.SnapshotStat, object string) {
				assert.Equal(t, tarStream, object)
				assert.Equal(t, stat.SizeBytes, stat.StoredBytes, "the archive is stored whole")
			},
		},
		{
			name:        "zstd stores the tar compressed",
			compression: CompressionZstd,
			wantKey:     captureDir + archiveNameZstd,
			wantStored: func(t *testing.T, stat model.SnapshotStat, object string) {
				assert.Equal(t, tarStream, decodeZstd(t, object))
				assert.Equal(t, int64(len(object)), stat.StoredBytes, "stored is what reached the store")
				assert.Less(t, stat.StoredBytes, stat.SizeBytes)
			},
		},
	}
	for _, compression := range []Compression{CompressionNone, CompressionZstd} {
		prefix := string(compression) + ": "
		tests = append(tests,
			testCase{
				name:        prefix + "a volume that does not open stores nothing",
				compression: compression,
				openErr:     errors.New("export boom"),
				wantErrMsg:  []string{"export boom", "open docker volume /host/etc/cfg"},
				wantEmpty:   true,
			},
			testCase{
				name:        prefix + "a volume whose mount source cannot be resolved stores nothing",
				compression: compression,
				addressErr:  errors.New("no such volume"),
				wantErrMsg:  []string{"resolve the mount source of volume volume-service-app-etc-cfg", "no such volume"},
				wantEmpty:   true,
			},
			testCase{
				name:        prefix + "a stream failing mid-copy surfaces as the archive write's error",
				compression: compression,
				reader:      io.NopCloser(failingReader{err: errors.New("stream boom")}),
				wantErrMsg:  []string{"write archive", "stream boom"},
			},
			testCase{
				// The store reads nothing, so only the pipe's close unblocks the encoder.
				name:        prefix + "a store refusing the archive fails the capture, wrapped",
				compression: compression,
				putErr:      errStore,
				wantErrMsg:  []string{"write archive"},
				wantErrIs:   errStore,
				wantEmpty:   true,
			},
		)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			reader := tt.reader
			if reader == nil {
				reader = io.NopCloser(strings.NewReader(tarStream))
			}
			docker := &mockVolumeStreamer{}
			docker.On("ResolveMountSource", mock.Anything, volume).
				Return(bindAddress("/etc/cfg"), tt.addressErr).Once()
			if tt.addressErr == nil {
				docker.On("OpenVolume", mock.Anything, bindAddress("/etc/cfg")).Return(reader, tt.openErr).Once()
			}
			store := newStore(t, "savepoints")
			spec := storages.Storager(store)
			if tt.putErr != nil {
				spec = refusingStore{Storager: store, err: tt.putErr}
			}
			snapshotter, err := NewFactory(docker, tt.compression).
				New(model.BackendSpec{Store: spec, Volume: volume})
			require.NoError(t, err)

			// Act
			stat, err := snapshotter.Snapshot(context.Background(), model.BackendSnapshotRequest{SnapshotID: testSnapshotID})

			// Assert
			if tt.wantErrMsg != nil {
				require.Error(t, err)
				for _, msg := range tt.wantErrMsg {
					assert.Contains(t, err.Error(), msg)
				}
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				assert.Zero(t, stat, "a failed capture reports nothing")
				if tt.wantEmpty {
					assert.Empty(t, objectNames(t, store), "nothing may be stored by a failed capture")
				}
				docker.AssertExpectations(t)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{tt.wantKey}, objectNames(t, store))
			assert.Equal(t, volume, stat.Volume, "the stat carries the unit it captured")
			assert.Equal(t, model.SnapshotBackendFullCopy, stat.Type)
			assert.Equal(t, int64(len(tarStream)), stat.SizeBytes, "size counts the tar")
			assert.Zero(t, stat.ReusedBytes)
			assert.Equal(t, Payload{Compression: tt.compression}, stat.Payload, "the record names the format")
			tt.wantStored(t, stat, readObject(t, store, tt.wantKey))
			docker.AssertExpectations(t)
		})
	}
}

// refusingStore refuses every object written under it, its sub-stores included.
type refusingStore struct {
	storages.Storager
	err error
}

func (s refusingStore) SubStorage(subPath string, relative bool) (storages.Storager, error) {
	sub, err := s.Storager.SubStorage(subPath, relative)
	if err != nil {
		return nil, err
	}
	return refusingStore{Storager: sub, err: s.err}, nil
}

func (s refusingStore) PutObject(context.Context, string, io.Reader) error { return s.err }

// The root the spec named is the whole of what an instance writes to and reads
// from. It is the only thing that tells a savepoint's instances from a
// rollback's undo, so an instance reaching the other root would be silent.
func TestSnapshotter_Snapshot_CapturesIntoTheStoreTheSpecNamed(t *testing.T) {
	// Arrange
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := testSnapshotID.String() + "/" + volume.ObjectID + "/" + archiveNameZstd
	docker := &mockVolumeStreamer{}
	docker.On("ResolveMountSource", mock.Anything, volume).Return(bindAddress("/etc/cfg"), nil).Once()
	docker.On("OpenVolume", mock.Anything, bindAddress("/etc/cfg")).
		Return(io.NopCloser(strings.NewReader(tarStream)), nil).Once()
	named, other := newStore(t, "named"), newStore(t, "other")
	snapshotter, err := NewFactory(docker, CompressionZstd).
		New(model.BackendSpec{Store: named, Volume: volume})
	require.NoError(t, err)

	// Act
	_, err = snapshotter.Snapshot(context.Background(), model.BackendSnapshotRequest{SnapshotID: testSnapshotID})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{archiveKey}, objectNames(t, named))
	assert.Empty(t, objectNames(t, other), "the capture reached a root the spec never named")
	docker.AssertExpectations(t)
}

// A capture's progress counts the tar it read, not the bytes the encoder wrote.
func TestSnapshotter_Snapshot_ReportsTheTarBytes(t *testing.T) {
	// Arrange
	volume := bindVolume("app", "/etc/cfg")
	docker := &mockVolumeStreamer{}
	docker.On("ResolveMountSource", mock.Anything, volume).Return(bindAddress("/etc/cfg"), nil).Once()
	docker.On("OpenVolume", mock.Anything, bindAddress("/etc/cfg")).
		Return(io.NopCloser(strings.NewReader(tarStream)), nil).Once()
	store := newStore(t, "savepoints")
	recorder := &bytesRecorder{}
	ctx := newBytesStepContext(recorder, "volume.capture")
	snapshotter, err := NewFactory(docker, CompressionZstd).
		New(model.BackendSpec{Store: store, Volume: volume})
	require.NoError(t, err)

	// Act
	stat, err := snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: testSnapshotID})

	// Assert
	require.NoError(t, err)
	require.NotEmpty(t, recorder.samples)
	assert.Equal(t, int64(len(tarStream)), recorder.samples[len(recorder.samples)-1].Done)
	assert.Equal(t, int64(len(tarStream)), stat.SizeBytes)
	docker.AssertExpectations(t)
}

// The archive the payload names is decoded, and the tar alone reaches the volume.
func TestSnapshotter_Restore(t *testing.T) {
	volume := bindVolume("app", "/etc/cfg")
	captureDir := testSnapshotID.String() + "/" + volume.ObjectID + "/"

	tests := []struct {
		name       string
		seedKey    string // the key the archive is seeded under; empty seeds nothing
		seed       func(t *testing.T) string
		reqPayload any
		writeErr   error
		verify     func(t *testing.T, err error, restored string)
	}{
		{
			name:       "a zstd archive reaches the volume decoded",
			seedKey:    captureDir + archiveNameZstd,
			seed:       encodeZstd,
			reqPayload: Payload{Compression: CompressionZstd},
			verify: func(t *testing.T, err error, restored string) {
				require.NoError(t, err)
				assert.Equal(t, tarStream, restored)
			},
		},
		{
			name:       "a payload read back from the store names its format the same",
			seedKey:    captureDir + archiveNameZstd,
			seed:       encodeZstd,
			reqPayload: map[string]any{"compression": "zstd"},
			verify: func(t *testing.T, err error, restored string) {
				require.NoError(t, err)
				assert.Equal(t, tarStream, restored)
			},
		},
		{
			name:       "an uncompressed archive reaches the volume verbatim",
			seedKey:    captureDir + archiveNameTar,
			reqPayload: Payload{Compression: CompressionNone},
			verify: func(t *testing.T, err error, restored string) {
				require.NoError(t, err)
				assert.Equal(t, tarStream, restored)
			},
		},
		{
			name:    "a record with no payload is uncompressed",
			seedKey: captureDir + archiveNameTar,
			verify: func(t *testing.T, err error, restored string) {
				require.NoError(t, err)
				assert.Equal(t, tarStream, restored)
			},
		},
		{
			name:       "a volume refusing the stream fails the restore, wrapped",
			seedKey:    captureDir + archiveNameZstd,
			seed:       encodeZstd,
			reqPayload: Payload{Compression: CompressionZstd},
			writeErr:   errors.New("import boom"),
			verify: func(t *testing.T, err error, _ string) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "write docker volume /host/etc/cfg")
				assert.Contains(t, err.Error(), "import boom")
			},
		},
		{
			name:       "a missing capture is a store error, not an empty restore",
			reqPayload: Payload{Compression: CompressionZstd},
			verify: func(t *testing.T, err error, _ string) {
				require.ErrorIs(t, err, storages.ErrFileNotFound)
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
			if tt.seedKey != "" {
				object := tarStream
				if tt.seed != nil {
					object = tt.seed(t)
				}
				require.NoError(t, store.PutObject(ctx, tt.seedKey, strings.NewReader(object)))
				// The reader is a stream: what it carries is read in Run, which is the assertion.
				docker.On("WriteVolume", mock.Anything, bindAddress("/etc/cfg"), mock.Anything).
					Run(func(args mock.Arguments) {
						_, err := io.Copy(&restored, args.Get(2).(io.Reader))
						assert.NoError(t, err)
					}).
					Return(tt.writeErr).Once()
			}
			snapshotter, err := NewFactory(docker, CompressionZstd).
				New(model.BackendSpec{Store: store, Volume: volume})
			require.NoError(t, err)

			// Act
			err = snapshotter.Restore(ctx, model.BackendRestoreRequest{SnapshotID: testSnapshotID, Payload: tt.reqPayload})

			// Assert
			tt.verify(t, err, restored.String())
			docker.AssertExpectations(t)
		})
	}
}

// A payload naming a format the backend cannot read fails before the store or
// the daemon is reached.
func TestSnapshotter_Restore_RefusesAnUnknownCompression(t *testing.T) {
	// Arrange
	docker := &mockVolumeStreamer{}
	store := newStore(t, "savepoints")
	snapshotter, err := NewFactory(docker, CompressionZstd).
		New(model.BackendSpec{Store: store, Volume: bindVolume("app", "/etc/cfg")})
	require.NoError(t, err)

	// Act
	err = snapshotter.Restore(context.Background(), model.BackendRestoreRequest{
		SnapshotID: testSnapshotID,
		Payload:    map[string]any{"compression": "lz4"},
	})

	// Assert
	require.ErrorIs(t, err, errUnknownCompression)
	docker.AssertExpectations(t)
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

// newBytesStepContext opens a byte-measuring step whose clock moves an hour per
// read, so the sampler sends every sample.
func newBytesStepContext(recorder *bytesRecorder, kind progress.Kind) context.Context {
	clock := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	now := func() time.Time {
		clock = clock.Add(time.Hour)
		return clock
	}
	ctx := progress.WithTracker(context.Background(), progress.New(recorder, now))
	ctx, _ = progress.Begin(ctx, progress.Step{Kind: "workflow"})
	ctx, _ = progress.Run(ctx, progress.Step{Kind: kind}, progress.MeasureBytes)
	return ctx
}

// The restore counts the decoded tar out of the size the request carries, the
// capture's own count, so the bar ends where size_bytes says.
func TestSnapshotter_Restore_ReportsTheTarBytes(t *testing.T) {
	// Arrange
	volume := bindVolume("app", "/etc/cfg")
	archiveKey := testSnapshotID.String() + "/" + volume.ObjectID + "/" + archiveNameZstd
	docker := &mockVolumeStreamer{}
	store := newStore(t, "savepoints")
	recorder := &bytesRecorder{}
	ctx := newBytesStepContext(recorder, "volume.restore")
	require.NoError(t, store.PutObject(ctx, archiveKey, strings.NewReader(encodeZstd(t))))
	docker.On("ResolveMountSource", mock.Anything, volume).Return(bindAddress("/etc/cfg"), nil).Once()
	docker.On("WriteVolume", mock.Anything, bindAddress("/etc/cfg"), mock.Anything).
		Run(func(args mock.Arguments) {
			_, err := io.Copy(io.Discard, args.Get(2).(io.Reader))
			assert.NoError(t, err)
		}).
		Return(nil).Once()
	snapshotter, err := NewFactory(docker, CompressionZstd).
		New(model.BackendSpec{Store: store, Volume: volume})
	require.NoError(t, err)

	// Act
	err = snapshotter.Restore(ctx, model.BackendRestoreRequest{
		SnapshotID: testSnapshotID,
		Payload:    Payload{Compression: CompressionZstd},
		SizeBytes:  int64(len(tarStream)),
	})

	// Assert
	require.NoError(t, err)
	require.NotEmpty(t, recorder.samples)
	last := recorder.samples[len(recorder.samples)-1]
	assert.Equal(t, int64(len(tarStream)), last.Total)
	assert.Equal(t, int64(len(tarStream)), last.Done)
	docker.AssertExpectations(t)
}

// Removal deletes the archive the payload names — an archive already gone is
// not an error — and leaves an archive of another format alone.
func TestSnapshotter_Remove(t *testing.T) {
	volume := bindVolume("app", "/etc/cfg")
	captureDir := testSnapshotID.String() + "/" + volume.ObjectID + "/"

	tests := []struct {
		name       string
		seed       []string
		reqPayload any
		want       []string
		wantErrIs  error
	}{
		{
			name:       "deletes the zstd archive",
			seed:       []string{captureDir + archiveNameZstd},
			reqPayload: Payload{Compression: CompressionZstd},
		},
		{
			name:       "deletes the uncompressed archive of a record read back from the store",
			seed:       []string{captureDir + archiveNameTar},
			reqPayload: map[string]any{"compression": "none"},
		},
		{
			name: "a record with no payload deletes the uncompressed archive",
			seed: []string{captureDir + archiveNameTar},
		},
		{
			name:       "an archive already gone is not an error",
			reqPayload: Payload{Compression: CompressionZstd},
		},
		{
			name:       "only the format the payload names is deleted",
			seed:       []string{captureDir + archiveNameTar},
			reqPayload: Payload{Compression: CompressionZstd},
			want:       []string{captureDir + archiveNameTar},
		},
		{
			name:       "an unknown format deletes nothing",
			seed:       []string{captureDir + archiveNameTar},
			reqPayload: map[string]any{"compression": "lz4"},
			want:       []string{captureDir + archiveNameTar},
			wantErrIs:  errUnknownCompression,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			docker := &mockVolumeStreamer{}
			store := newStore(t, "savepoints")
			ctx := context.Background()
			for _, key := range tt.seed {
				require.NoError(t, store.PutObject(ctx, key, strings.NewReader("archive")))
			}
			snapshotter, err := NewFactory(docker, CompressionZstd).
				New(model.BackendSpec{Store: store, Volume: volume})
			require.NoError(t, err)

			// Act
			err = snapshotter.Remove(ctx, model.BackendRemoveRequest{SnapshotID: testSnapshotID, Payload: tt.reqPayload})

			// Assert
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
			} else {
				require.NoError(t, err)
			}
			if tt.want == nil {
				assert.Empty(t, objectNames(t, store))
			} else {
				assert.Equal(t, tt.want, objectNames(t, store))
			}
			docker.AssertExpectations(t)
		})
	}
}

// A payload carries the format, typed or as the store hands it back; none is
// the format of a record that names no other.
func TestDecodePayload(t *testing.T) {
	tests := []struct {
		name       string
		stored     any
		want       Payload
		wantErrIs  error
		wantErrMsg string
	}{
		{
			name:   "a typed payload passes through",
			stored: Payload{Compression: CompressionZstd},
			want:   Payload{Compression: CompressionZstd},
		},
		{
			name:   "a map read back from the store decodes",
			stored: map[string]any{"compression": "zstd"},
			want:   Payload{Compression: CompressionZstd},
		},
		{
			name:   "no payload is none",
			stored: nil,
			want:   Payload{Compression: CompressionNone},
		},
		{
			name:   "a payload naming no format is none",
			stored: map[string]any{},
			want:   Payload{Compression: CompressionNone},
		},
		{
			name:       "a format that is not a string does not decode",
			stored:     map[string]any{"compression": 5},
			wantErrMsg: "decode fullcopy payload",
		},
		{
			name:      "an unknown format is refused",
			stored:    map[string]any{"compression": "lz4"},
			wantErrIs: errUnknownCompression,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := DecodePayload(tt.stored)

			// Assert
			if tt.wantErrIs != nil || tt.wantErrMsg != "" {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
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
				snapshotter, err := NewFactory(docker, CompressionZstd).New(model.BackendSpec{Store: store, Volume: bindVolume("app", "/etc/cfg")})
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
		name        string
		volume      model.Volume
		noStore     bool
		compression Compression
		wantErrIs   error
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
		{
			name:        "a format the backend cannot write is refused",
			volume:      bindVolume("app", "/etc/cfg"),
			compression: "lz4",
			wantErrIs:   errUnknownCompression,
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
			compression := tt.compression
			if compression == "" {
				compression = CompressionZstd
			}

			// Act
			snapshotter, err := NewFactory(docker, compression).New(spec)

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

// encodeZstd is tarStream as a zstd capture stores it.
func encodeZstd(t *testing.T) string {
	t.Helper()

	var encoded bytes.Buffer
	encoder, err := zstd.NewWriter(&encoded)
	require.NoError(t, err)
	_, err = io.WriteString(encoder, tarStream)
	require.NoError(t, err)
	require.NoError(t, encoder.Close())
	return encoded.String()
}

// decodeZstd is the tar a zstd object holds.
func decodeZstd(t *testing.T, object string) string {
	t.Helper()

	decoder, err := zstd.NewReader(strings.NewReader(object))
	require.NoError(t, err)
	defer decoder.Close()
	decoded, err := io.ReadAll(decoder)
	require.NoError(t, err)
	return string(decoded)
}
