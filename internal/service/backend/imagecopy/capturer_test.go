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
	imageID     = "sha256:aaaa"
	archive     = "the save archive, opaque bytes"
)

// captureKey is where a capture of demoRequest's container lands.
const captureKey = "1780335122/images/db/1/image.tar"

func demoRequest() model.ImageCaptureRequest {
	return model.ImageCaptureRequest{
		SnapshotID: snapshotID,
		Container: model.ContainerRef{
			Service: "db", Replica: 1, ID: containerID, BaseImage: "postgres:16",
		},
	}
}

func TestCapturer_Capture(t *testing.T) {
	removeErr := errors.New("image is in use")

	tests := []struct {
		name   string
		expect func(docker *mockContainerImager)
		verify func(t *testing.T, capture model.ImageCapture, err error, store storages.Storager)
	}{
		{
			name: "stores the archive verbatim and drops the image",
			expect: func(docker *mockContainerImager) {
				commit := docker.On("CommitContainer", mock.Anything, containerID).Return(imageID, nil).Once()
				save := docker.On("SaveImage", mock.Anything, imageID).
					Return(io.NopCloser(strings.NewReader(archive)), nil).Once()
				// The daemon's copy goes only once the store holds the archive.
				remove := docker.On("RemoveImage", mock.Anything, imageID).Return(nil).Once()
				mock.InOrder(commit, save, remove)
			},
			verify: func(t *testing.T, capture model.ImageCapture, err error, store storages.Storager) {
				require.NoError(t, err)
				assert.Equal(t, model.ImageCapture{
					Service: "db", Replica: 1, ContainerID: containerID,
					ImageID: imageID, BaseImage: "postgres:16", SizeBytes: int64(len(archive)),
				}, capture)
				assert.Equal(t, []string{captureKey}, objectNames(t, store))
				assert.Equal(t, archive, readObject(t, store, captureKey))
			},
		},
		{
			name: "a failed removal leaves the capture successful",
			expect: func(docker *mockContainerImager) {
				docker.On("CommitContainer", mock.Anything, containerID).Return(imageID, nil).Once()
				docker.On("SaveImage", mock.Anything, imageID).
					Return(io.NopCloser(strings.NewReader(archive)), nil).Once()
				docker.On("RemoveImage", mock.Anything, imageID).Return(removeErr).Once()
			},
			verify: func(t *testing.T, capture model.ImageCapture, err error, store storages.Storager) {
				require.NoError(t, err)
				assert.Equal(t, imageID, capture.ImageID)
				assert.Equal(t, []string{captureKey}, objectNames(t, store))
			},
		},
		{
			name: "a failed commit reaches neither the store nor the daemon",
			expect: func(docker *mockContainerImager) {
				docker.On("CommitContainer", mock.Anything, containerID).
					Return("", errors.New("no such container")).Once()
			},
			verify: func(t *testing.T, capture model.ImageCapture, err error, store storages.Storager) {
				require.Error(t, err)
				assert.Equal(t, model.ImageCapture{}, capture)
				assert.Empty(t, objectNames(t, store))
			},
		},
		{
			name: "a failed save keeps the image in the daemon",
			expect: func(docker *mockContainerImager) {
				docker.On("CommitContainer", mock.Anything, containerID).Return(imageID, nil).Once()
				docker.On("SaveImage", mock.Anything, imageID).Return(nil, errors.New("daemon closed")).Once()
			},
			verify: func(t *testing.T, capture model.ImageCapture, err error, store storages.Storager) {
				require.Error(t, err)
				assert.Equal(t, model.ImageCapture{}, capture)
			},
		},
		{
			name: "a stream that fails mid-archive fails the capture",
			expect: func(docker *mockContainerImager) {
				docker.On("CommitContainer", mock.Anything, containerID).Return(imageID, nil).Once()
				docker.On("SaveImage", mock.Anything, imageID).
					Return(io.NopCloser(failingReader{err: errors.New("connection reset")}), nil).Once()
			},
			verify: func(t *testing.T, capture model.ImageCapture, err error, _ storages.Storager) {
				require.ErrorContains(t, err, "write image archive")
				assert.Equal(t, model.ImageCapture{}, capture)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)
			docker := &mockContainerImager{}
			tt.expect(docker)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			capture, err := New(store, docker).Capture(ctx, demoRequest())

			// Assert
			tt.verify(t, capture, err, store)
			docker.AssertExpectations(t)
		})
	}
}

func TestCapturer_Capture_RejectsAnUnaddressableRequest(t *testing.T) {
	tests := []struct {
		name string
		req  model.ImageCaptureRequest
	}{
		{
			name: "an id that is not a savepoint id",
			req: model.ImageCaptureRequest{
				SnapshotID: "../escape", Container: demoRequest().Container,
			},
		},
		{
			name: "a container of an unnamed service",
			req: model.ImageCaptureRequest{
				SnapshotID: snapshotID,
				Container:  model.ContainerRef{Replica: 1, ID: containerID},
			},
		},
		{
			name: "a replica number compose never assigns",
			req: model.ImageCaptureRequest{
				SnapshotID: snapshotID,
				Container:  model.ContainerRef{Service: "db", Replica: 0, ID: containerID},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)
			docker := &mockContainerImager{}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := New(store, docker).Capture(ctx, tt.req)

			// Assert
			require.Error(t, err)
			assert.Empty(t, objectNames(t, store))
			docker.AssertExpectations(t)
		})
	}
}

// TestImagesNamespace_CannotCollideWithAVolume pins images/ as a reserved key: no
// volume's own directory is ever named "images".
func TestImagesNamespace_CannotCollideWithAVolume(t *testing.T) {
	// Arrange
	volumes := []string{
		model.NamedVolume{Name: ImagesNamespace}.ObjectID(),
		model.ServiceVolume{Type: model.VolumeTypeBind, Target: "/" + ImagesNamespace}.ObjectID(ImagesNamespace),
		model.ServiceVolume{Type: model.VolumeTypeAnon, Target: "/" + ImagesNamespace}.ObjectID("db"),
	}

	// Assert
	for _, objectID := range volumes {
		assert.NotEqual(t, ImagesNamespace, objectID)
		assert.True(t, strings.HasPrefix(objectID, "volume-"), objectID)
	}
}

func newStore(t *testing.T) storages.Storager {
	t.Helper()

	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
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

	src, err := store.GetObject(context.Background(), key)
	require.NoError(t, err)
	content, err := io.ReadAll(src)
	require.NoError(t, err)
	require.NoError(t, src.Close())

	return string(content)
}
