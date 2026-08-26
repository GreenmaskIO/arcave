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

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	snapshotID  = model.SavepointID("1780335122")
	containerID = "c0ffee"
	imageID     = model.ImageDigestID("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
)

func demoRequest() model.ImageCaptureRequest {
	return model.ImageCaptureRequest{
		SnapshotID: snapshotID,
		Container: model.ContainerRef{
			Service: "db", Replica: 1, ID: containerID, BaseImage: "postgres:16",
		},
	}
}

func TestImageStorage_AddressesOneImage(t *testing.T) {
	// Arrange
	store := newStore(t)

	// Act
	imageStore, err := imageStorage(store, snapshotID, "db", 1)

	// Assert
	require.NoError(t, err)
	require.NoError(t, imageStore.PutObject(context.Background(), "probe", strings.NewReader("x")))
	assert.Equal(t, []string{"1780335122/images/db/replicas/1/probe"}, objectNames(t, store),
		"the key both backends must agree on")
}

func TestImageStorage_RejectsAnUnaddressableImage(t *testing.T) {
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
			_, err := imageStorage(store, tt.snapshotID, tt.service, tt.replica)

			// Assert
			require.Error(t, err)
			assert.Empty(t, objectNames(t, store), "nothing is written under a key it refused")
		})
	}
}

func TestManifest_RoundTrip(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	imageStore, err := imageStorage(newStore(t), snapshotID, "db", 1)
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
			imageStore, err := imageStorage(newStore(t), snapshotID, "db", 1)
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

func TestCopier_Capture(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := newStore(t)
	docker := &mockContainerImager{}
	commit := docker.On("CommitContainer", mock.Anything, containerID).Return(imageID, nil).Once()
	save := docker.On("SaveImage", mock.Anything, imageID).
		Return(io.NopCloser(strings.NewReader("archive")), nil).Once()
	// The daemon's copy goes only once the layout has stored the archive.
	remove := docker.On("RemoveImage", mock.Anything, imageID).Return(nil).Once()
	mock.InOrder(commit, save, remove)
	layout := &stubLayout{size: StoredSize{Image: 900, Written: 100}}

	// Act
	capture, err := NewCopier(store, docker, layout).Capture(ctx, demoRequest())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.ImageCapture{
		Service: "db", Replica: 1, ContainerID: containerID,
		ImageID: imageID, BaseImage: "postgres:16", SizeBytes: 900, StoredBytes: 100,
	}, capture)
	assert.Equal(t, "archive", layout.stored, "the layout is handed the daemon's stream")
	docker.AssertExpectations(t)
}

func TestCopier_Capture_KeepsTheImageWhenTheLayoutFails(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	docker := &mockContainerImager{}
	docker.On("CommitContainer", mock.Anything, containerID).Return(imageID, nil).Once()
	docker.On("SaveImage", mock.Anything, imageID).
		Return(io.NopCloser(strings.NewReader("archive")), nil).Once()
	layout := &stubLayout{storeErr: errors.New("the store is full")}

	// Act: RemoveImage carries no expectation, so calling it fails the test — a
	// capture that did not land must leave the daemon's copy alone.
	_, err := NewCopier(newStore(t), docker, layout).Capture(ctx, demoRequest())

	// Assert
	require.ErrorContains(t, err, "the store is full")
	docker.AssertExpectations(t)
}

func TestCopier_Restore_TagsWhatTheDaemonAlreadyHolds(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	docker := &mockContainerImager{}
	docker.On("ImageExists", mock.Anything, imageID).Return(true, nil).Once()
	docker.On("TagImage", mock.Anything, imageID, "arcave/demo:sp-1").Return(nil).Once()
	layout := &stubLayout{}

	// Act
	restored, err := NewCopier(newStore(t), docker, layout).Restore(ctx, demoRestoreRequest())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "arcave/demo:sp-1", restored.ImageRef)
	assert.False(t, layout.loaded, "a load costs the whole archive and is not worth taking twice")
	docker.AssertExpectations(t)
}

func TestCopier_Restore_LoadsWhatTheLayoutRenders(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	docker := &mockContainerImager{}
	docker.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()
	docker.On("LoadImage", mock.Anything, "rendered archive").Return(nil).Once()
	docker.On("ImageExists", mock.Anything, imageID).Return(true, nil).Once()
	docker.On("TagImage", mock.Anything, imageID, "arcave/demo:sp-1").Return(nil).Once()
	layout := &stubLayout{renders: "rendered archive"}

	// Act
	restored, err := NewCopier(newStore(t), docker, layout).Restore(ctx, demoRestoreRequest())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, imageID, restored.ImageID)
	docker.AssertExpectations(t)
}

func TestCopier_Restore_RefusesALoadThatResolvedElsewhere(t *testing.T) {
	// Arrange: the daemon takes the archive but ends up holding another image.
	ctx := zerolog.Nop().WithContext(context.Background())
	docker := &mockContainerImager{}
	docker.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()
	docker.On("LoadImage", mock.Anything, "rendered archive").Return(nil).Once()
	docker.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()

	// Act
	_, err := NewCopier(newStore(t), docker, &stubLayout{renders: "rendered archive"}).
		Restore(ctx, demoRestoreRequest())

	// Assert
	require.ErrorContains(t, err, "does not hold image")
	docker.AssertExpectations(t)
}

func TestCopier_Restore_ReportsWhyTheLayoutCouldNotRender(t *testing.T) {
	// Arrange: the daemon swallows a broken stream, so the layout's own error is the
	// only account of why the image never arrived.
	ctx := zerolog.Nop().WithContext(context.Background())
	docker := &mockContainerImager{}
	docker.On("ImageExists", mock.Anything, imageID).Return(false, nil).Once()
	docker.On("LoadImage", mock.Anything, mock.Anything).Return(nil).Once()
	layout := &stubLayout{renders: "half an ", loadErr: model.ErrSavepointImageMissing}

	// Act
	_, err := NewCopier(newStore(t), docker, layout).Restore(ctx, demoRestoreRequest())

	// Assert
	require.ErrorIs(t, err, model.ErrSavepointImageMissing)
	docker.AssertExpectations(t)
}

func TestCopier_Restore_RefusesAnEmptyReference(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	docker := &mockContainerImager{}
	req := demoRestoreRequest()
	req.ImageRef = ""

	// Act
	_, err := NewCopier(newStore(t), docker, &stubLayout{}).Restore(ctx, req)

	// Assert
	require.ErrorContains(t, err, "under no reference")
	docker.AssertExpectations(t)
}

// stubLayout stands in for a real layout: it records the archive it was given and
// renders back whatever it was told to.
type stubLayout struct {
	size     StoredSize
	storeErr error
	renders  string
	loadErr  error

	stored string
	loaded bool
}

func (s *stubLayout) Store(
	_ context.Context, _ storages.Storager, _ model.ImageDigestID, archive io.Reader,
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

func (s *stubLayout) Load(_ context.Context, _ storages.Storager, _ model.ImageDigestID, archive io.Writer) error {
	s.loaded = true
	if _, err := io.WriteString(archive, s.renders); err != nil {
		return err
	}
	return s.loadErr
}

func demoRestoreRequest() model.ImageRestoreRequest {
	return model.ImageRestoreRequest{
		SnapshotID: snapshotID,
		Capture: model.ImageCapture{
			Service: "db", Replica: 1, ContainerID: containerID, ImageID: imageID,
		},
		ImageRef: "arcave/demo:sp-1",
	}
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
