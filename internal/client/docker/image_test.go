package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
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

func TestEngine_LoadImage(t *testing.T) {
	// Arrange
	const archive = "save-archive-bytes"
	body := &trackedBody{ReadCloser: io.NopCloser(strings.NewReader("{\"stream\":\"Loaded image ID: sha256:aaaa\"}"))}
	api := &mockAPI{}
	api.On("ImageLoad", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			read, err := io.ReadAll(args.Get(1).(io.Reader))
			require.NoError(t, err)
			assert.Equal(t, archive, string(read))
		}).
		Return(image.LoadResponse{Body: body}, nil).Once()
	e := &Engine{api: api}

	// Act
	err := e.LoadImage(context.Background(), strings.NewReader(archive))

	// Assert
	require.NoError(t, err)
	assert.True(t, body.drained, "an unread response can leave the load unfinished")
	assert.True(t, body.closed, "the response body is closed")
	api.AssertExpectations(t)
}

func TestEngine_LoadImage_ReportsTheDaemonsRefusal(t *testing.T) {
	// Arrange
	wantErr := errors.New("unexpected EOF")
	api := &mockAPI{}
	api.On("ImageLoad", mock.Anything, mock.Anything).Return(image.LoadResponse{}, wantErr).Once()
	e := &Engine{api: api}

	// Act
	err := e.LoadImage(context.Background(), strings.NewReader("truncated"))

	// Assert
	require.ErrorIs(t, err, wantErr)
	api.AssertExpectations(t)
}

func TestEngine_TagImage(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ImageTag", mock.Anything, "sha256:aaaa", "arcave/demo-db-1-postgres-16:sp-1780335122").Return(nil).Once()
	e := &Engine{api: api}

	// Act
	err := e.TagImage(context.Background(), "sha256:aaaa", "arcave/demo-db-1-postgres-16:sp-1780335122")

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
}

func TestEngine_ImageExists(t *testing.T) {
	hardErr := errors.New("daemon closed")

	tests := []struct {
		name       string
		inspectErr error
		want       bool
		wantErr    error
	}{
		{name: "the daemon holds it", want: true},
		{name: "the daemon does not", inspectErr: cerrdefs.ErrNotFound},
		{name: "the daemon cannot say", inspectErr: hardErr, wantErr: hardErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			api.On("ImageInspect", mock.Anything, "sha256:aaaa").
				Return(image.InspectResponse{ID: "sha256:aaaa"}, tt.inspectErr).Once()
			e := &Engine{api: api}

			// Act
			got, err := e.ImageExists(context.Background(), "sha256:aaaa")

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			api.AssertExpectations(t)
		})
	}
}

// trackedBody reports whether the load response was read to the end and closed.
type trackedBody struct {
	io.ReadCloser
	drained bool
	closed  bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.drained = true
	}
	return n, err
}

func (b *trackedBody) Close() error {
	b.closed = true
	return b.ReadCloser.Close()
}
