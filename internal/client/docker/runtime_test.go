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
package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

type fakeVersionReader struct {
	version client.ServerVersionResult
	err     error
}

func (f fakeVersionReader) ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error) {
	return f.version, f.err
}

func TestReadRuntime(t *testing.T) {
	unreachable := errors.New("connection refused")
	podman := client.ServerVersionResult{Version: "6.1.4", Components: []system.ComponentVersion{{Name: "Podman Engine", Version: "6.1.4"}}}
	tests := []struct {
		name    string
		reader  fakeVersionReader
		want    model.ContainerRuntime
		wantErr error
	}{
		{
			name:   "the daemon's answer, named",
			reader: fakeVersionReader{version: podman},
			want:   model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
		},
		{name: "a daemon that does not answer", reader: fakeVersionReader{err: unreachable}, wantErr: unreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ReadRuntime(context.Background(), tt.reader)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
