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

package composespec

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
)

func TestDivergentFields(t *testing.T) {
	first := types.ServiceConfig{
		Name:          "web",
		ContainerName: "demo-web-1",
		ContainerSpec: types.ContainerSpec{
			Image:       "nginx:1",
			Hostname:    "web-1",
			Environment: types.NewMappingWithEquals([]string{"MODE=a"}),
		},
		WorkloadSpec: types.WorkloadSpec{Expose: types.StringOrNumberList{"80/tcp"}},
	}

	tests := []struct {
		name   string
		modify func(cfg *types.ServiceConfig)
		want   []string
	}{
		{
			name:   "identical configs",
			modify: func(*types.ServiceConfig) {},
		},
		{
			name:   "an environment promoted from the container spec is named by its key",
			modify: func(cfg *types.ServiceConfig) { cfg.Environment = types.NewMappingWithEquals([]string{"MODE=b"}) },
			want:   []string{"environment"},
		},
		{
			name:   "an expose promoted from the workload spec is named by its key",
			modify: func(cfg *types.ServiceConfig) { cfg.Expose = types.StringOrNumberList{"81/tcp"} },
			want:   []string{"expose"},
		},
		{
			name:   "a field of the service itself is named by its key",
			modify: func(cfg *types.ServiceConfig) { cfg.Restart = "always" },
			want:   []string{"restart"},
		},
		{
			name:   "a hostname differs per replica by construction",
			modify: func(cfg *types.ServiceConfig) { cfg.Hostname = "web-2" },
		},
		{
			name:   "a container name differs per replica by construction",
			modify: func(cfg *types.ServiceConfig) { cfg.ContainerName = "demo-web-2" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			second := first
			tt.modify(&second)

			// Act
			got := divergentFields(first, second)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
