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

package core

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

// layerID and layerRef are a layer a rollback restored, which the container now runs on.
const (
	layerID  = model.ImageDigestID("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	layerRef = "arcave/demo-db-1-postgres-16:sp-1780000002"
)

func TestCapturableContainers(t *testing.T) {
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
			image: demoUpstreamID, ref: demoBaseImage,
			wantID: demoUpstreamID, want: demoBaseImage,
		},
		{
			name:  "a layer naming its upstream by label",
			image: layerID, ref: layerRef,
			labels: map[string]string{model.BaseImageIDLabel: demoUpstreamID.String(), model.BaseImageRefLabel: demoBaseImage},
			wantID: demoUpstreamID, want: demoBaseImage,
		},
		{
			name:  "a layer whose label is no digest",
			image: layerID, ref: layerRef,
			labels: map[string]string{model.BaseImageIDLabel: "postgres:16"},
		},
		{
			name:  "a layer carrying no labels",
			image: layerID, ref: layerRef,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			res := demoIntrospection()
			res.Project.Services = []model.Service{{Name: demoService, Containers: []model.Container{{
				ID: demoContainerID, Number: 1,
				Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{Image: tt.image.String()},
					Config:            &container.Config{Image: tt.ref},
				},
			}}}}
			res.Images = map[model.ImageDigestID]image.InspectResponse{
				tt.image: {
					ID:     tt.image.String(),
					Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: tt.labels}},
				},
			}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			got := capturableContainers(ctx, res)

			// Assert
			assert.Equal(t, []model.ContainerRef{{
				Service: demoService, Replica: 1, ID: demoContainerID,
				UpstreamImageID: tt.wantID, UpstreamImageRef: tt.want,
			}}, got)
		})
	}
}

func TestUpstreamImages(t *testing.T) {
	// Arrange: two replicas on one upstream, one on an upstream gone from the daemon,
	// one that cannot be traced.
	goneID := model.ImageDigestID("sha256:9999999999999999999999999999999999999999999999999999999999999999")
	res := demoIntrospectionWithContainers()
	containers := []model.ContainerRef{
		{Service: demoService, Replica: 1, UpstreamImageID: demoUpstreamID, UpstreamImageRef: demoBaseImage},
		{Service: demoService, Replica: 2, UpstreamImageID: demoUpstreamID, UpstreamImageRef: demoBaseImage},
		{Service: "cache", Replica: 1, UpstreamImageID: goneID, UpstreamImageRef: "redis:7"},
		{Service: "app", Replica: 1, UpstreamImageRef: "app:dev"},
	}

	// Act
	got := upstreamImages(res, containers)

	// Assert
	assert.Equal(t, []model.UpstreamImageCaptureRequest{
		demoUpstreamCaptureRequest(),
		{Image: model.UpstreamImage{ID: goneID, Refs: []string{"redis:7"}}},
	}, got)
}
