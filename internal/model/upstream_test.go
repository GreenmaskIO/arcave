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

package model

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
)

const (
	upstreamID  = ImageDigestID("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	upstreamRef = "postgres:16"
	layerID     = ImageDigestID("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	layerRef    = "arcave/demo-db-1-postgres-16:sp-1780000002"
)

func TestResolveUpstream(t *testing.T) {
	tests := []struct {
		name   string
		image  ImageDigestID
		ref    string
		labels map[string]string
		noConf bool
		wantID ImageDigestID
		want   string
		wantOK bool
	}{
		{
			name:  "a container on its upstream",
			image: upstreamID, ref: upstreamRef,
			wantID: upstreamID, want: upstreamRef, wantOK: true,
		},
		{
			name:  "a layer naming its upstream by label",
			image: layerID, ref: layerRef,
			labels: map[string]string{BaseImageIDLabel: upstreamID.String(), BaseImageRefLabel: upstreamRef},
			wantID: upstreamID, want: upstreamRef, wantOK: true,
		},
		{
			name:  "a layer whose label is no digest",
			image: layerID, ref: layerRef,
			labels: map[string]string{BaseImageIDLabel: "postgres:16"},
		},
		{
			name:  "a layer carrying no labels",
			image: layerID, ref: layerRef,
		},
		{
			name:   "a container with no config",
			image:  upstreamID,
			noConf: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			c := Container{ID: "c1", Inspect: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{Image: tt.image.String()},
			}}
			if !tt.noConf {
				c.Inspect.Config = &container.Config{Image: tt.ref}
			}
			res := IntrospectionResult{Images: map[ImageDigestID]image.InspectResponse{
				tt.image: {
					ID:     tt.image.String(),
					Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: tt.labels}},
				},
			}}

			// Act
			id, ref, ok := ResolveUpstream(res, c)

			// Assert
			assert.Equal(t, tt.wantID, id)
			assert.Equal(t, tt.want, ref)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestIsLayerRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{ref: layerRef, want: true},
		{ref: "arcave/demo-db-1:sp-1780000001", want: true},
		{ref: "postgres:16"},
		{ref: "arcave/demo-db-1:latest"},
		{ref: "arcave/demo-db-1:sp-abc"},
		{ref: "registry:5000/arcave/x:sp-1780000001"},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			// Act
			got := IsLayerRef(tt.ref)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
