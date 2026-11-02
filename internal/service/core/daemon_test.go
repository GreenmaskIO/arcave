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

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/system"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestRequireDaemon(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		components []types.ComponentVersion
		minMajor   int
		wantErr    error
	}{
		{name: "docker 24 is refused", version: "24.0.7", minMajor: 25, wantErr: model.ErrUnsupportedDaemon},
		{name: "docker 20.10 is refused", version: "20.10.24", minMajor: 25, wantErr: model.ErrUnsupportedDaemon},
		{name: "docker 25 is accepted", version: "25.0.0", minMajor: 25},
		{name: "a docker 25 release candidate is accepted", version: "25.0.0-rc.1", minMajor: 25},
		{name: "docker 28 is accepted", version: "28.3.2", minMajor: 25},
		{name: "an empty version passes", version: "", minMajor: 25},
		{name: "a dev build passes", version: "dev", minMajor: 25},
		{name: "no requirement accepts any daemon", version: "20.10.24", minMajor: 0},
		{name: "docker 29 naming its engine is accepted", version: "29.8.2", minMajor: 25,
			components: []types.ComponentVersion{{Name: "Engine", Version: "29.8.2"}}},
		{name: "docker 24 naming its engine is refused", version: "24.0.7", minMajor: 25, wantErr: model.ErrUnsupportedDaemon,
			components: []types.ComponentVersion{{Name: "Engine", Version: "24.0.7"}}},
		{name: "podman 6 passes", version: "6.1.4", minMajor: 25,
			components: []types.ComponentVersion{{Name: "Podman Engine", Version: "6.1.4"}, {Name: "Engine", Version: "6.1.4"}}},
		{name: "podman 4 passes", version: "4.9.3", minMajor: 25,
			components: []types.ComponentVersion{{Name: "Podman Engine", Version: "4.9.3"}}},
		{name: "a version below the floor without components is refused", version: "6.1.4", minMajor: 25,
			wantErr: model.ErrUnsupportedDaemon},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := requireDaemon(ctx, system.Info{ServerVersion: tt.version}, types.Version{Components: tt.components}, tt.minMajor)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Contains(t, err.Error(), tt.version)
				return
			}
			assert.NoError(t, err)
		})
	}
}
