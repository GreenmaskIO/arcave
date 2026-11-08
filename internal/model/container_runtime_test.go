// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package model

import (
	"testing"

	"github.com/moby/moby/api/types/system"
	"github.com/stretchr/testify/assert"
)

func TestNewContainerRuntime(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		components []system.ComponentVersion
		want       ContainerRuntime
	}{
		{
			name:    "Docker",
			version: "29.8.2",
			components: []system.ComponentVersion{
				{Name: "Engine", Version: "29.8.2"}, {Name: "containerd", Version: "2.1.0"},
			},
			want: ContainerRuntime{Kind: ContainerRuntimeDocker, Version: "29.8.2"},
		},
		{
			name:    "Podman, by its component",
			version: "4.9.4",
			components: []system.ComponentVersion{
				{Name: "Podman Engine", Version: "6.1.4"}, {Name: "Conmon", Version: "2.1.13"},
			},
			want: ContainerRuntime{Kind: ContainerRuntimePodman, Version: "4.9.4"},
		},
		{
			name:    "Podman listing a plain Engine first",
			version: "6.1.4",
			components: []system.ComponentVersion{
				{Name: "Engine", Version: "6.1.4"}, {Name: "Podman Engine", Version: "6.1.4"},
			},
			want: ContainerRuntime{Kind: ContainerRuntimePodman, Version: "6.1.4"},
		},
		{
			name:    "no components at all",
			version: "24.0.7",
			want:    ContainerRuntime{Kind: ContainerRuntimeDocker, Version: "24.0.7"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewContainerRuntime(tt.version, tt.components)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
