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

package imagecopy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

const (
	snapshotID  = model.SavepointID("019e843e-1650-7000-8000-000000000000")
	containerID = "c0ffee"
	imageID     = model.ImageDigestID("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	upstreamID  = model.ImageDigestID("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	commitNote  = "arcave savepoint 019e843e-1650-7000-8000-000000000000"
)

func demoRequest() model.WritableLayerCaptureRequest {
	return model.WritableLayerCaptureRequest{
		SnapshotID: snapshotID,
		Container: model.ContainerRef{
			Service: "db", Replica: 1, ID: containerID, Name: "demo-db-1",
			UpstreamImageID: upstreamID, UpstreamImageRef: "postgres:16",
		},
		Backend: model.ImageBackendDedupCopy,
	}
}

var demoOwner = model.LayerOrigin{ArcaveProject: "app", ComposeProject: "demo"}

func demoUpstreamLabels() map[string]string {
	return map[string]string{
		string(model.SystemLabelArcaveProject):  "app",
		string(model.SystemLabelComposeProject): "demo",
		string(model.SystemLabelBaseImageID):    upstreamID.String(),
		string(model.SystemLabelBaseImageRef):   "postgres:16",
	}
}

func demoUpstream() model.Image {
	return model.Image{
		ID: upstreamID, Refs: []string{"postgres:16"}, RepoDigests: []string{"postgres@sha256:cc"}, SizeBytes: 900,
	}
}

func TestWritableLayerStorage_AddressesOneImage(t *testing.T) {
	// Arrange
	store := newStore(t)

	// Act
	imageStore, err := writableLayerStorage(store, snapshotID, "db", 1)

	// Assert
	require.NoError(t, err)
	require.NoError(t, imageStore.PutObject(context.Background(), "probe", strings.NewReader("x")))
	assert.Equal(t, []string{"019e843e-1650-7000-8000-000000000000/images/db/replicas/1/probe"}, objectNames(t, store),
		"the key both backends must agree on")
}

func TestImageStorage_AddressesOneImageByContent(t *testing.T) {
	// Arrange
	store := newStore(t)

	// Act
	imageStore, err := imageStorage(store, upstreamID)

	// Assert
	require.NoError(t, err)
	require.NoError(t, imageStore.PutObject(context.Background(), "probe", strings.NewReader("x")))
	assert.Equal(t, []string{"shared/oci/images/sha256/" + strings.TrimPrefix(upstreamID.String(), "sha256:") + "/probe"},
		objectNames(t, store))
}

func TestImageStorage_RejectsAnIDThatIsNoDigest(t *testing.T) {
	// Arrange
	store := newStore(t)

	// Act
	_, err := imageStorage(store, "../../escape")

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidDigestID)
	assert.Empty(t, objectNames(t, store))
}

func TestWritableLayerStorage_RejectsAnUnaddressableImage(t *testing.T) {
	tests := []struct {
		name       string
		snapshotID model.SavepointID
		service    string
		replica    int
	}{
		{name: "an id that is not digits", snapshotID: "../escape", service: "db", replica: 1},
		{name: "a container of an unnamed service", snapshotID: snapshotID, replica: 1},
		{name: "a replica number that is not a replica", snapshotID: snapshotID, service: "db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)

			// Act
			_, err := writableLayerStorage(store, tt.snapshotID, tt.service, tt.replica)

			// Assert
			require.Error(t, err)
			assert.Empty(t, objectNames(t, store), "nothing is written under a key it refused")
		})
	}
}

func TestManifest_RoundTrip(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	imageStore, err := writableLayerStorage(newStore(t), snapshotID, "db", 1)
	require.NoError(t, err)
	written := Manifest{
		ImageID: imageID,
		Format:  FormatBlobs,
		Members: []Member{
			{Name: "blobs/sha256/abc", Kind: KindBlob, Digest: "abc"},
			{Name: "index.json", Kind: KindMetadata},
		},
	}

	// Act
	require.NoError(t, WriteManifest(ctx, imageStore, written))
	read, err := ReadManifest(ctx, imageStore, FormatBlobs)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, written, read)
}

func TestManifest_RefusesWhatItCannotRead(t *testing.T) {
	tests := []struct {
		name    string
		written *Manifest
		asks    Format
		wantErr error
	}{
		{
			name:    "a capture stored by the other backend",
			written: &Manifest{ImageID: imageID, Format: FormatArchive},
			asks:    FormatBlobs,
			wantErr: model.ErrUnsupportedImageArchive,
		},
		{
			name:    "a manifest naming no members",
			written: &Manifest{ImageID: imageID, Format: FormatBlobs},
			asks:    FormatBlobs,
			wantErr: model.ErrSavepointImageMissing,
		},
		{
			name:    "a capture with no manifest at all",
			asks:    FormatBlobs,
			wantErr: model.ErrSavepointImageMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			imageStore, err := writableLayerStorage(newStore(t), snapshotID, "db", 1)
			require.NoError(t, err)
			if tt.written != nil {
				if tt.written.Members == nil && tt.wantErr == model.ErrUnsupportedImageArchive {
					tt.written.Members = []Member{{Name: "image.tar", Kind: KindArchive}}
				}
				require.NoError(t, WriteManifest(ctx, imageStore, *tt.written))
			}

			// Act
			_, err = ReadManifest(ctx, imageStore, tt.asks)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestCopier_CaptureWritableLayer(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := newStore(t)
	engine := &mockImageEngine{}
	commit := engine.On("CommitContainer", mock.Anything, containerID, commitNote, demoUpstreamLabels()).
		Return(imageID, nil).Once()
	save := engine.On("SaveImage", mock.Anything, imageID).
		Return(io.NopCloser(strings.NewReader("archive")), nil).Once()
	// The daemon's copy goes only once the archiver has stored the archive.
	remove := engine.On("RemoveImage", mock.Anything, imageID).Return(nil).Once()
	mock.InOrder(commit, save, remove)
	archiver := &stubArchiver{size: StoredSize{Image: 900, Written: 100}}

	// Act
	capture, err := newCopier(store, engine, archiver).CaptureWritableLayer(ctx, demoRequest())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.WritableLayer{
		Service: "db", Replica: 1, ContainerID: containerID, ContainerName: "demo-db-1",
		CommittedImageID: imageID, ImageBackend: model.ImageBackendDedupCopy,
		UpstreamImageID: upstreamID, UpstreamImageRef: "postgres:16",
		SizeBytes: 900, StoredBytes: 100,
	}, capture)
	assert.Equal(t, "archive", archiver.stored, "the archiver is handed the daemon's stream")
	engine.AssertExpectations(t)
}

// A layer is stored by the archiver its backend names, and recorded under it.
func TestCopier_CaptureWritableLayer_StoresByTheRequestedBackend(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	engine.On("CommitContainer", mock.Anything, containerID, commitNote, demoUpstreamLabels()).
		Return(imageID, nil).Once()
	engine.On("SaveImage", mock.Anything, imageID).
		Return(io.NopCloser(strings.NewReader("archive")), nil).Once()
	engine.On("RemoveImage", mock.Anything, imageID).Return(nil).Once()
	dedup, full := &stubArchiver{}, &stubArchiver{}
	copier := NewCopier(newStore(t), demoOwner, engine, map[model.ImageBackendKind]ImageArchiver{
		model.ImageBackendDedupCopy: dedup,
		model.ImageBackendFullCopy:  full,
	}, dedup)
	req := demoRequest()
	req.Backend = model.ImageBackendFullCopy

	// Act
	capture, err := copier.CaptureWritableLayer(ctx, req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.ImageBackendFullCopy, capture.ImageBackend)
	assert.Equal(t, "archive", full.stored)
	assert.Empty(t, dedup.stored)
	engine.AssertExpectations(t)
}

// A backend the copier was not built with is refused before anything is
// committed or loaded.
func TestCopier_RefusesAnUnknownBackend(t *testing.T) {
	// Arrange: the imager carries no expectation, so any call fails the test.
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	copier := NewCopier(newStore(t), demoOwner, engine, map[model.ImageBackendKind]ImageArchiver{}, &stubArchiver{})
	restore := demoRestoreRequest()
	restore.Layer.ImageBackend = ""

	// Act
	_, captureErr := copier.CaptureWritableLayer(ctx, demoRequest())
	_, restoreErr := copier.RestoreWritableLayer(ctx, restore)

	// Assert
	require.ErrorIs(t, captureErr, model.ErrUnsupportedImageArchive)
	require.ErrorIs(t, restoreErr, model.ErrUnsupportedImageArchive)
	engine.AssertExpectations(t)
}

func TestCopier_CaptureWritableLayer_KeepsTheImageWhenTheArchiverFails(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	engine.On("CommitContainer", mock.Anything, containerID, commitNote, demoUpstreamLabels()).
		Return(imageID, nil).Once()
	engine.On("SaveImage", mock.Anything, imageID).
		Return(io.NopCloser(strings.NewReader("archive")), nil).Once()
	archiver := &stubArchiver{storeErr: errors.New("the store is full")}

	// Act: RemoveImage carries no expectation, so calling it fails the test — a
	// capture that did not land must leave the daemon's copy alone.
	_, err := newCopier(newStore(t), engine, archiver).CaptureWritableLayer(ctx, demoRequest())

	// Assert
	require.ErrorContains(t, err, "the store is full")
	engine.AssertExpectations(t)
}

func TestCopier_CaptureWritableLayer_StampsNoUpstreamItWasNotTold(t *testing.T) {
	// Arrange: a broken chain leaves the upstream unknown; the origin is always known.
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	engine.On("CommitContainer", mock.Anything, containerID, commitNote, demoOwner.Labels()).
		Return(imageID, nil).Once()
	engine.On("SaveImage", mock.Anything, imageID).
		Return(io.NopCloser(strings.NewReader("archive")), nil).Once()
	engine.On("RemoveImage", mock.Anything, imageID).Return(nil).Once()
	req := demoRequest()
	req.Container.UpstreamImageID, req.Container.UpstreamImageRef = "", ""

	// Act
	capture, err := newCopier(newStore(t), engine, &stubArchiver{}).CaptureWritableLayer(ctx, req)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, capture.UpstreamImageID)
	engine.AssertExpectations(t)
}

func TestCopier_CaptureImage(t *testing.T) {
	intact := `{"image_id":"` + upstreamID.String() + `","format":"oci-blobs","members":[{"name":"index.json","kind":"metadata"}]}`
	tests := []struct {
		name           string
		stored         string // the manifest already in the store, verbatim; empty for none
		existsInDaemon bool
		saveErr        error
		wantStored     bool
		wantBytes      int64
		wantSaved      bool
	}{
		{name: "already in the store", stored: intact, existsInDaemon: true, wantStored: true},
		{
			name: "a torn manifest is stored again", stored: intact[:40], existsInDaemon: true,
			wantStored: true, wantBytes: 40, wantSaved: true,
		},
		{
			name:   "a manifest of another image is stored again",
			stored: strings.Replace(intact, upstreamID.String(), imageID.String(), 1), existsInDaemon: true,
			wantStored: true, wantBytes: 40, wantSaved: true,
		},
		{
			name: "a manifest naming no members is stored again", stored: `{"image_id":"` + upstreamID.String() + `"}`,
			existsInDaemon: true, wantStored: true, wantBytes: 40, wantSaved: true,
		},
		{name: "in the daemon", existsInDaemon: true, wantStored: true, wantBytes: 40, wantSaved: true},
		{name: "nowhere", wantStored: false},
		{name: "the daemon fails to hand it over", existsInDaemon: true, saveErr: errors.New("content digest not found")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			store := newStore(t)
			if tt.stored != "" {
				upstreamStore, err := imageStorage(store, upstreamID)
				require.NoError(t, err)
				require.NoError(t, upstreamStore.PutObject(ctx, manifestName, strings.NewReader(tt.stored)))
			}
			engine := &mockImageEngine{}
			if tt.wantSaved || tt.saveErr != nil {
				engine.On("SaveImage", mock.Anything, upstreamID).
					Return(io.NopCloser(strings.NewReader("upstream archive")), tt.saveErr).Once()
			}
			archiver := &stubArchiver{size: StoredSize{Image: 900, Written: 40}}

			// Act
			got, err := newCopier(store, engine, archiver).CaptureImage(ctx, model.ImageCaptureRequest{
				Image: demoUpstream(), ExistsInDaemon: tt.existsInDaemon,
			})

			// Assert
			require.NoError(t, err, "a missing upstream never fails the savepoint")
			want := demoUpstream()
			want.Stored, want.StoredBytes = tt.wantStored, tt.wantBytes
			assert.Equal(t, want, got)
			if tt.wantSaved {
				assert.Equal(t, "upstream archive", archiver.stored)
			}
			engine.AssertExpectations(t)
		})
	}
}

func TestCopier_CaptureImage_FailsWhenTheRunIsCancelled(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	cancel()
	engine := &mockImageEngine{}
	engine.On("SaveImage", mock.Anything, upstreamID).Return(nil, context.Canceled).Once()

	// Act
	_, err := newCopier(newStore(t), engine, &stubArchiver{}).CaptureImage(ctx, model.ImageCaptureRequest{
		Image: demoUpstream(), ExistsInDaemon: true,
	})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	engine.AssertExpectations(t)
}

func TestCopier_RestoreWritableLayer_TagsWhatTheDaemonAlreadyHolds(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	engine.On("ImageExists", mock.Anything, imageID).Return(true, nil).Once()
	engine.On("TagImage", mock.Anything, imageID, "arcave/demo:sp-1").Return(nil).Once()
	archiver := &stubArchiver{}

	// Act
	restored, err := newCopier(newStore(t), engine, archiver).RestoreWritableLayer(ctx, demoRestoreRequest())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.WritableLayerRestoreResult{
		Service: "db", Replica: 1, ContainerName: "demo-db-1", ImageID: imageID, Tag: "arcave/demo:sp-1",
	}, restored, "the restore names the container the replica is rebuilt as")
	assert.False(t, archiver.loaded, "a load costs the whole archive and is not worth taking twice")
	engine.AssertExpectations(t)
}

func TestCopier_RestoreWritableLayer_LoadsWhatTheArchiverRenders(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	engine.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()
	engine.On("LoadImage", mock.Anything, "rendered archive").Return(nil).Once()
	engine.On("ImageExists", mock.Anything, imageID).Return(true, nil).Once()
	engine.On("TagImage", mock.Anything, imageID, "arcave/demo:sp-1").Return(nil).Once()
	archiver := &stubArchiver{renders: "rendered archive"}

	// Act
	restored, err := newCopier(newStore(t), engine, archiver).RestoreWritableLayer(ctx, demoRestoreRequest())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, imageID, restored.ImageID)
	engine.AssertExpectations(t)
}

// The load is counted once, here and not in the archiver, out of the archive size the
// capture recorded.
func TestCopier_RestoreWritableLayer_CountsTheLoadOutOfTheLayerSize(t *testing.T) {
	// Arrange
	recorder := &bytesRecorder{}
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, now))
	ctx, _ = progress.Begin(ctx, progress.Step{Kind: "rollback"})
	ctx, _ = progress.Run(ctx, progress.Step{Kind: "layer.restore"}, progress.MeasureBytes)
	engine := &mockImageEngine{}
	engine.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()
	engine.On("LoadImage", mock.Anything, "rendered archive").Return(nil).Once()
	engine.On("ImageExists", mock.Anything, imageID).Return(true, nil).Once()
	engine.On("TagImage", mock.Anything, imageID, "arcave/demo:sp-1").Return(nil).Once()
	req := demoRestoreRequest()
	req.Layer.SizeBytes = int64(len("rendered archive"))

	// Act
	_, err := newCopier(newStore(t), engine, &stubArchiver{renders: "rendered archive"}).RestoreWritableLayer(ctx, req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []progress.Bytes{{Done: req.Layer.SizeBytes, Total: req.Layer.SizeBytes}}, recorder.samples)
	engine.AssertExpectations(t)
}

func TestCopier_RestoreWritableLayer_RefusesALoadThatResolvedElsewhere(t *testing.T) {
	// Arrange: the daemon takes the archive but ends up holding another image.
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	engine.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()
	engine.On("LoadImage", mock.Anything, "rendered archive").Return(nil).Once()
	engine.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()

	// Act
	_, err := newCopier(newStore(t), engine, &stubArchiver{renders: "rendered archive"}).
		RestoreWritableLayer(ctx, demoRestoreRequest())

	// Assert
	require.ErrorContains(t, err, "does not hold image")
	engine.AssertExpectations(t)
}

func TestCopier_RestoreWritableLayer_ReportsWhyTheArchiverCouldNotRender(t *testing.T) {
	// Arrange: the daemon swallows a broken stream, so the archiver's own error is the
	// only account of why the image never arrived.
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	engine.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()
	engine.On("LoadImage", mock.Anything, mock.Anything).Return(nil).Once()
	archiver := &stubArchiver{renders: "half an ", loadErr: model.ErrSavepointImageMissing}

	// Act
	_, err := newCopier(newStore(t), engine, archiver).RestoreWritableLayer(ctx, demoRestoreRequest())

	// Assert
	require.ErrorIs(t, err, model.ErrSavepointImageMissing)
	engine.AssertExpectations(t)
}

func TestCopier_RestoreWritableLayer_RefusesAnEmptyReference(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	engine := &mockImageEngine{}
	req := demoRestoreRequest()
	req.Tag = ""

	// Act
	_, err := newCopier(newStore(t), engine, &stubArchiver{}).RestoreWritableLayer(ctx, req)

	// Assert
	require.ErrorContains(t, err, "under no reference")
	engine.AssertExpectations(t)
}

// bytesRecorder keeps the bytes of every sample the tracker reports.
type bytesRecorder struct {
	samples []progress.Bytes
}

func (r *bytesRecorder) Report(evt progress.Event) {
	if evt.Sample.Bytes != nil {
		r.samples = append(r.samples, *evt.Sample.Bytes)
	}
}

// stubArchiver stands in for a real archiver: it records the archive it was given and
// renders back whatever it was told to.
type stubArchiver struct {
	size     StoredSize
	storeErr error
	renders  string
	loadErr  error

	stored string
	loaded bool
}

func (s *stubArchiver) Store(
	_ context.Context, _ storages.Writer, _ model.ImageDigestID, archive io.Reader,
) (StoredSize, error) {
	body, err := io.ReadAll(archive)
	if err != nil {
		return StoredSize{}, err
	}
	s.stored = string(body)
	if s.storeErr != nil {
		return StoredSize{}, s.storeErr
	}
	return s.size, nil
}

func (s *stubArchiver) Load(_ context.Context, _ storages.Storager, _ model.ImageDigestID, archive io.Writer) error {
	s.loaded = true
	if _, err := io.WriteString(archive, s.renders); err != nil {
		return err
	}
	return s.loadErr
}

func demoRestoreRequest() model.WritableLayerRestoreRequest {
	return model.WritableLayerRestoreRequest{
		SnapshotID: snapshotID,
		Layer: model.WritableLayer{
			Service: "db", Replica: 1, ContainerID: containerID, ContainerName: "demo-db-1",
			CommittedImageID: imageID, ImageBackend: model.ImageBackendDedupCopy,
		},
		Tag: "arcave/demo:sp-1",
	}
}

// newCopier builds a copier whose every archiver, the images' included, is archiver.
func newCopier(store storages.Navigator, engine dockerImageEngine, archiver ImageArchiver) *Copier {
	return NewCopier(store, demoOwner, engine, map[model.ImageBackendKind]ImageArchiver{
		model.ImageBackendDedupCopy: archiver,
		model.ImageBackendFullCopy:  archiver,
	}, archiver)
}

func newStore(t *testing.T) storages.Storager {
	t.Helper()

	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	return store
}

func objectNames(t *testing.T, store storages.Storager) []string {
	t.Helper()

	objects, err := store.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(objects))
	for _, object := range objects {
		names = append(names, object.Name)
	}
	return names
}
