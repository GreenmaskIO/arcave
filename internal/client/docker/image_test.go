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

package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestEngine_CommitContainer(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ContainerCommit", mock.Anything, "c0ffee", client.ContainerCommitOptions{
		NoPause: true,
		Comment: "arcave savepoint 019e7044-8800-7000-8000-000000000000",
		Changes: []string{
			`LABEL "io.arcave.base-image-id"="sha256:bbbb"`,
			`LABEL "io.arcave.base-image-ref"="postgres:16"`,
		},
	}).
		Return(client.ContainerCommitResult{ID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil).Once()
	e := &Engine{api: api}

	// Act
	id, err := e.CommitContainer(context.Background(), "c0ffee", "arcave savepoint 019e7044-8800-7000-8000-000000000000", map[string]string{
		string(model.SystemLabelBaseImageRef): "postgres:16",
		string(model.SystemLabelBaseImageID):  "sha256:bbbb",
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.ImageDigestID("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), id)
	api.AssertExpectations(t)
}

func TestEngine_CommitContainer_PrefixesABareDigest(t *testing.T) {
	// Arrange
	const bare = "ebf716eb4137a315c21ae58522ef7b138f6b91f15585e3e2f694c5af324afbe9"
	api := &mockAPI{}
	api.On("ContainerCommit", mock.Anything, "c0ffee", client.ContainerCommitOptions{NoPause: true, Changes: []string{}}).
		Return(client.ContainerCommitResult{ID: bare}, nil).Once()
	e := &Engine{api: api}

	// Act
	id, err := e.CommitContainer(context.Background(), "c0ffee", "", nil)

	// Assert: Podman answers with the hex alone.
	require.NoError(t, err)
	assert.Equal(t, model.ImageDigestID("sha256:"+bare), id)
	api.AssertExpectations(t)
}

func TestEngine_CommitContainer_RefusesAnAnswerThatIsNotADigest(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ContainerCommit", mock.Anything, "c0ffee", client.ContainerCommitOptions{NoPause: true, Changes: []string{}}).
		Return(client.ContainerCommitResult{ID: "postgres:16"}, nil).Once()
	e := &Engine{api: api}

	// Act
	_, err := e.CommitContainer(context.Background(), "c0ffee", "", nil)

	// Assert: a name would be resolved by whatever it points at later on.
	require.ErrorIs(t, err, model.ErrInvalidDigestID)
	api.AssertExpectations(t)
}

func TestEngine_CommitContainer_ReportsTheDaemonsRefusal(t *testing.T) {
	// Arrange
	wantErr := errors.New("no such container")
	api := &mockAPI{}
	api.On("ContainerCommit", mock.Anything, "gone", mock.Anything).
		Return(client.ContainerCommitResult{}, wantErr).Once()
	e := &Engine{api: api}

	// Act
	id, err := e.CommitContainer(context.Background(), "gone", "", nil)

	// Assert
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, id)
	api.AssertExpectations(t)
}

func TestEngine_SaveImage(t *testing.T) {
	// Arrange
	const archive = "save-archive-bytes"
	api := &mockAPI{}
	api.On("ImageSave", mock.Anything, []string{"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).
		Return(client.ImageSaveResult(io.NopCloser(strings.NewReader(archive))), nil).Once()
	e := &Engine{api: api}

	// Act
	rc, err := e.SaveImage(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

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
	api.On("ImageRemove", mock.Anything, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", client.ImageRemoveOptions{}).
		Return(client.ImageRemoveResult{Items: []image.DeleteResponse{{Deleted: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}, nil).Once()
	e := &Engine{api: api}

	// Act
	err := e.RemoveImage(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
}

func TestEngine_RemoveImage_ReportsTheDaemonsRefusal(t *testing.T) {
	// Arrange
	wantErr := errors.New("image is being used by stopped container")
	api := &mockAPI{}
	api.On("ImageRemove", mock.Anything, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", client.ImageRemoveOptions{}).
		Return(nil, wantErr).Once()
	e := &Engine{api: api}

	// Act
	err := e.RemoveImage(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Assert
	require.ErrorIs(t, err, wantErr)
	api.AssertExpectations(t)
}

func TestEngine_RemoveImageRef(t *testing.T) {
	const (
		ref     = "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000"
		imageID = "sha256:aaaa"
	)
	refused := errors.New("image is being used by stopped container")
	tests := []struct {
		name       string
		inspectErr error
		containers []container.Summary
		// wantRemove is whether the reference is asked to go.
		wantRemove bool
		removeErr  error
		wantErr    error
		wantErrMsg string
	}{
		{name: "the reference goes", wantRemove: true},
		{
			name:       "one gone before the inspect",
			inspectErr: cerrdefs.ErrNotFound,
			wantErr:    model.ErrImageRefNotFound,
		},
		{
			name:       "one gone before the removal",
			wantRemove: true,
			removeErr:  cerrdefs.ErrNotFound,
			wantErr:    model.ErrImageRefNotFound,
		},
		{
			name:       "an image a container uses is refused, though another reference names it",
			containers: []container.Summary{{ID: "c1"}},
			wantErrMsg: "image sha256:aaaa is used by container c1",
		},
		{
			name:       "a reference the daemon refuses",
			wantRemove: true,
			removeErr:  refused,
			wantErr:    refused,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			calls := []*mock.Call{
				api.On("ImageInspect", mock.Anything, ref).Return(client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: imageID}}, tt.inspectErr).Once(),
			}
			if tt.inspectErr == nil {
				calls = append(calls, api.On("ContainerList", mock.Anything, client.ContainerListOptions{
					All:     true,
					Filters: client.Filters{}.Add("ancestor", strings.TrimPrefix(imageID, "sha256:")),
				}).Return(client.ContainerListResult{Items: tt.containers}, nil).Once())
			}
			if tt.wantRemove {
				calls = append(calls, api.On("ImageRemove", mock.Anything, ref, client.ImageRemoveOptions{PruneChildren: true}).
					Return(client.ImageRemoveResult{Items: []image.DeleteResponse{{Untagged: ref}}}, tt.removeErr).Once())
			}
			mock.InOrder(calls...)
			e := &Engine{api: api}

			// Act
			err := e.RemoveImageRef(context.Background(), ref)

			// Assert
			switch {
			case tt.wantErr != nil:
				assert.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, "remove image reference "+ref)
			case tt.wantErrMsg != "":
				assert.ErrorContains(t, err, tt.wantErrMsg)
			default:
				assert.NoError(t, err)
			}
			api.AssertExpectations(t)
		})
	}
}

func TestEngine_LoadImage(t *testing.T) {
	// Arrange
	const archive = "save-archive-bytes"
	body := &trackedBody{ReadCloser: io.NopCloser(strings.NewReader("{\"stream\":\"Loaded image ID: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}"))}
	api := &mockAPI{}
	api.On("ImageLoad", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			read, err := io.ReadAll(args.Get(1).(io.Reader))
			require.NoError(t, err)
			assert.Equal(t, archive, string(read))
		}).
		Return(client.ImageLoadResult(body), nil).Once()
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
	api.On("ImageLoad", mock.Anything, mock.Anything).Return(nil, wantErr).Once()
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
	api.On("ImageTag", mock.Anything, client.ImageTagOptions{
		Source: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Target: "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000",
	}).Return(nil).Once()
	e := &Engine{api: api}

	// Act
	err := e.TagImage(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000")

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
			api.On("ImageInspect", mock.Anything, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").
				Return(client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, tt.inspectErr).Once()
			e := &Engine{api: api}

			// Act
			got, err := e.ImageExists(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

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

// A tag comes back as Docker spells it, whichever daemon prefixed it with docker.io/.
func TestEngine_ListLayerImages(t *testing.T) {
	// Arrange
	const (
		layerRef  = "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000001"
		podmanRef = "arcave/demo-db-2:sp-019e843e-1650-7000-8000-000000000001"
	)
	api := &mockAPI{}
	api.On("ImageList", mock.Anything, client.ImageListOptions{
		Filters: client.Filters{}.Add("reference", "arcave/*"),
	}).Return(client.ImageListResult{Items: []image.Summary{
		{
			ID: "sha256:aaaa", RepoTags: []string{layerRef, "postgres:16"},
			Labels: map[string]string{string(model.SystemLabelArcaveProject): "app", string(model.SystemLabelComposeProject): "demo"},
		},
		{ID: "sha256:bbbb", RepoTags: []string{"docker.io/" + podmanRef}},
		{ID: "sha256:cccc", RepoTags: []string{"arcave/demo-db-3:sp-not-a-savepoint"}},
		{ID: "sha256:dddd", RepoTags: []string{"busybox:latest"}},
	}}, nil).Once()
	e := &Engine{api: api}

	// Act
	tagged, err := e.ListLayerImages(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.TaggedImage{
		{Ref: layerRef, ImageID: "sha256:aaaa", Origin: model.LayerOrigin{ArcaveProject: "app", ComposeProject: "demo"}},
		{Ref: podmanRef, ImageID: "sha256:bbbb"},
	}, tagged)
	api.AssertExpectations(t)
}

// A container's reference comes back as Docker spells it, to meet ListLayerImages' tags.
func TestEngine_ListContainerImages(t *testing.T) {
	// Arrange
	const layerRef = "arcave/demo-db-1:sp-019e843e-1650-7000-8000-000000000001"
	api := &mockAPI{}
	api.On("ContainerList", mock.Anything, client.ContainerListOptions{All: true}).Return(client.ContainerListResult{Items: []container.Summary{
		{ID: "c1", Image: layerRef, ImageID: "sha256:aaaa"},
		{ID: "c2", Image: "docker.io/" + layerRef, ImageID: "sha256:aaaa"},
		{ID: "c3", Image: "sha256:bbbb", ImageID: "sha256:bbbb"},
	}}, nil).Once()
	e := &Engine{api: api}

	// Act
	images, err := e.ListContainerImages(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.ContainerImage{
		{ContainerID: "c1", Ref: layerRef, ImageID: "sha256:aaaa"},
		{ContainerID: "c2", Ref: layerRef, ImageID: "sha256:aaaa"},
		{ContainerID: "c3", Ref: "sha256:bbbb", ImageID: "sha256:bbbb"},
	}, images)
	api.AssertExpectations(t)
}
