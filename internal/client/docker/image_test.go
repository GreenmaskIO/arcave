package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestEngine_CommitContainer(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ContainerCommit", mock.Anything, "c0ffee", container.CommitOptions{Pause: false}).
		Return(container.CommitResponse{ID: "sha256:aaaa"}, nil).Once()
	e := &Engine{api: api}

	// Act
	id, err := e.CommitContainer(context.Background(), "c0ffee")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "sha256:aaaa", id)
	api.AssertExpectations(t)
}

func TestEngine_CommitContainer_ReportsTheDaemonsRefusal(t *testing.T) {
	// Arrange
	wantErr := errors.New("no such container")
	api := &mockAPI{}
	api.On("ContainerCommit", mock.Anything, "gone", mock.Anything).
		Return(container.CommitResponse{}, wantErr).Once()
	e := &Engine{api: api}

	// Act
	id, err := e.CommitContainer(context.Background(), "gone")

	// Assert
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, id)
	api.AssertExpectations(t)
}

func TestEngine_SaveImage(t *testing.T) {
	// Arrange
	const archive = "save-archive-bytes"
	api := &mockAPI{}
	api.On("ImageSave", mock.Anything, []string{"sha256:aaaa"}).
		Return(io.NopCloser(strings.NewReader(archive)), nil).Once()
	e := &Engine{api: api}

	// Act
	rc, err := e.SaveImage(context.Background(), "sha256:aaaa")

	// Assert
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, archive, string(got))
	api.AssertExpectations(t)
}

func TestEngine_RemoveImage(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ImageRemove", mock.Anything, "sha256:aaaa", image.RemoveOptions{}).
		Return([]image.DeleteResponse{{Deleted: "sha256:aaaa"}}, nil).Once()
	e := &Engine{api: api}

	// Act
	err := e.RemoveImage(context.Background(), "sha256:aaaa")

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
}

func TestEngine_RemoveImage_ReportsTheDaemonsRefusal(t *testing.T) {
	// Arrange
	wantErr := errors.New("image is being used by stopped container")
	api := &mockAPI{}
	api.On("ImageRemove", mock.Anything, "sha256:aaaa", image.RemoveOptions{}).
		Return(nil, wantErr).Once()
	e := &Engine{api: api}

	// Act
	err := e.RemoveImage(context.Background(), "sha256:aaaa")

	// Assert
	require.ErrorIs(t, err, wantErr)
	api.AssertExpectations(t)
}
