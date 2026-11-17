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

package captureplan

import (
	"context"
	"testing"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// layerID and layerRef are a layer a rollback restored, which the container now runs on.
const (
	layerID  = model.ImageDigestID("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	layerRef = "arcave/demo-db-1-postgres-16:sp-019e7044-8fd0-7000-8000-000000000000"
)

func TestNewContainerRefs(t *testing.T) {
	tests := []struct {
		name   string
		image  model.ImageDigestID
		ref    string
		labels map[string]string
		wantID model.ImageDigestID
		want   string
	}{
		{
			name:  "a container on its upstream",
			image: composetest.DemoUpstreamID, ref: composetest.DemoBaseImage,
			wantID: composetest.DemoUpstreamID, want: composetest.DemoBaseImage,
		},
		{
			name:  "a layer naming its upstream by label",
			image: layerID, ref: layerRef,
			labels: map[string]string{string(model.SystemLabelBaseImageID): composetest.DemoUpstreamID.String(), string(model.SystemLabelBaseImageRef): composetest.DemoBaseImage},
			wantID: composetest.DemoUpstreamID, want: composetest.DemoBaseImage,
		},
		{
			name:  "a layer whose label is no digest",
			image: layerID, ref: layerRef,
			labels: map[string]string{string(model.SystemLabelBaseImageID): "postgres:16"},
		},
		{
			name:  "a layer carrying no labels",
			image: layerID, ref: layerRef,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := composetest.DemoIntrospection()
			introspection.Project.Services = []model.Service{{Name: composetest.DemoService, Containers: []model.Container{{
				ID: composetest.DemoContainerID, Number: 1,
				Inspect: container.InspectResponse{
					Image:  tt.image.String(),
					Config: &container.Config{Image: tt.ref},
				},
			}}}}
			introspection.Images = map[model.ImageDigestID]image.InspectResponse{
				tt.image: {
					ID:     tt.image.String(),
					Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: tt.labels}},
				},
			}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			got := newContainerRefs(ctx, introspection)

			// Assert
			assert.Equal(t, []model.ContainerRef{{
				Service: composetest.DemoService, Replica: 1, ID: composetest.DemoContainerID,
				UpstreamImageID: tt.wantID, UpstreamImageRef: tt.want,
			}}, got)
		})
	}
}

func TestNewUnresolvedImages(t *testing.T) {
	// Arrange
	introspection := composetest.DemoIntrospectionWithContainers()
	resolved := composetest.DemoContainerRef()
	unresolved := model.ContainerRef{Service: composetest.DemoService, Replica: 1, ID: composetest.DemoContainerID, Name: composetest.DemoContainerName(1)}

	// Act
	none := newUnresolvedImages(introspection, []model.ContainerRef{resolved})
	got := newUnresolvedImages(introspection, []model.ContainerRef{unresolved})

	// Assert
	assert.Nil(t, none)
	assert.Equal(t, []model.UnresolvedImage{{
		Service: composetest.DemoService, Replica: 1, Container: composetest.DemoContainerName(1), Image: composetest.DemoBaseImage,
	}}, got)
}

func TestNewImageCaptures(t *testing.T) {
	// Arrange: two replicas on one upstream, one on an upstream gone from the daemon,
	// one that cannot be traced.
	goneID := model.ImageDigestID("sha256:9999999999999999999999999999999999999999999999999999999999999999")
	introspection := composetest.DemoIntrospectionWithContainers()
	containers := []model.ContainerRef{
		{Service: composetest.DemoService, Replica: 1, UpstreamImageID: composetest.DemoUpstreamID, UpstreamImageRef: composetest.DemoBaseImage},
		{Service: composetest.DemoService, Replica: 2, UpstreamImageID: composetest.DemoUpstreamID, UpstreamImageRef: composetest.DemoBaseImage},
		{Service: "cache", Replica: 1, UpstreamImageID: goneID, UpstreamImageRef: "redis:7"},
		{Service: "app", Replica: 1, UpstreamImageRef: "app:dev"},
	}

	// Act
	got := newImageCaptures(introspection, containers)

	// Assert
	assert.Equal(t, []model.ImageCaptureRequest{
		composetest.DemoImageCaptureRequest(),
		{Image: model.Image{ID: goneID, Refs: []string{"redis:7"}}},
	}, got)
}

func TestWarnOnComposeImageMismatch(t *testing.T) {
	const (
		runningID = model.ImageDigestID("sha256:1111111111111111111111111111111111111111111111111111111111111111")
		otherID   = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	)
	tests := []struct {
		name      string
		composeID string
		mediaType string
		want      bool
	}{
		{name: "the image compose created it from", composeID: runningID.String()},
		{name: "another image", composeID: otherID, mediaType: ocispec.MediaTypeImageManifest, want: true},
		{name: "a manifest of the index it runs on", composeID: otherID, mediaType: ocispec.MediaTypeImageIndex},
		{name: "a manifest of a docker manifest list", composeID: otherID, mediaType: dockerManifestList},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			c := model.Container{
				ID:     "c1",
				Labels: map[string]string{composeImageLabel: tt.composeID},
				Inspect: container.InspectResponse{
					Image: runningID.String(),
				},
			}
			inspect := image.InspectResponse{}
			if tt.mediaType != "" {
				inspect.Descriptor = &ocispec.Descriptor{MediaType: tt.mediaType}
			}
			introspection := model.IntrospectionResult{Images: map[model.ImageDigestID]image.InspectResponse{runningID: inspect}}
			logs := &composetest.SyncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			warnOnComposeImageMismatch(ctx, introspection, c)

			// Assert
			assert.Equal(t, tt.want, logs.String() != "")
		})
	}
}

// Podman tags an image docker.io/library/…: the refs keep Docker's spelling, the one the
// container's own already carries named once.
func TestNewImageCaptures_PodmanRepoTags(t *testing.T) {
	// Arrange
	introspection := composetest.DemoIntrospectionWithContainers()
	upstream := introspection.Images[composetest.DemoUpstreamID]
	upstream.RepoTags = []string{"docker.io/library/" + composetest.DemoBaseImage, "registry:5000/mirror/postgres:16"}
	introspection.Images[composetest.DemoUpstreamID] = upstream
	containers := []model.ContainerRef{
		{Service: composetest.DemoService, Replica: 1, UpstreamImageID: composetest.DemoUpstreamID, UpstreamImageRef: composetest.DemoBaseImage},
	}

	// Act
	got := newImageCaptures(introspection, containers)

	// Assert
	require.Len(t, got, 1)
	assert.Equal(t, []string{composetest.DemoBaseImage, "registry:5000/mirror/postgres:16"}, got[0].Image.Refs)
}
