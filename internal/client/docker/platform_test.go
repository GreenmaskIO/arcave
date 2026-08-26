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
	"testing"

	"github.com/docker/docker/api/types/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The decision needs both ends on one kernel: a native Linux daemon reached
// from a Linux arcave, and nothing else — Desktop's "host" is its VM's, and a
// non-linux arcave never shares a loopback with any daemon.
func Test_sharesHostLoopback(t *testing.T) {
	tests := []struct {
		name    string
		localOS string
		info    system.Info
		want    bool
	}{
		{
			name:    "linux to a native linux daemon",
			localOS: "linux",
			info:    system.Info{OSType: "linux", OperatingSystem: "Ubuntu 24.04.2 LTS"},
			want:    true,
		},
		{
			// WSL2: linux on both ends, and the daemon's loopback is still the VM's.
			name:    "linux to docker desktop's daemon",
			localOS: "linux",
			info:    system.Info{OSType: "linux", OperatingSystem: "Docker Desktop"},
			want:    false,
		},
		{
			name:    "darwin to docker desktop",
			localOS: "darwin",
			info:    system.Info{OSType: "linux", OperatingSystem: "Docker Desktop"},
			want:    false,
		},
		{
			// colima, a remote engine — any linux daemon a mac reaches is not local.
			name:    "darwin to a plain linux daemon",
			localOS: "darwin",
			info:    system.Info{OSType: "linux", OperatingSystem: "Ubuntu 24.04.2 LTS"},
			want:    false,
		},
		{
			name:    "linux to a windows daemon",
			localOS: "linux",
			info:    system.Info{OSType: "windows", OperatingSystem: "Windows Server 2022"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := sharesHostLoopback(tt.localOS, tt.info)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEngine_HostLoopbackShared(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("Info", mock.Anything).Return(system.Info{OSType: "linux", OperatingSystem: "Docker Desktop"}, nil)

	// Act
	shared, err := NewEngine(api).HostLoopbackShared(context.Background())

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
	assert.False(t, shared)
}

func TestEngine_HostLoopbackShared_daemonError(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("Info", mock.Anything).Return(system.Info{}, errors.New("daemon down"))

	// Act
	_, err := NewEngine(api).HostLoopbackShared(context.Background())

	// Assert
	require.ErrorContains(t, err, "daemon down")
	api.AssertExpectations(t)
}
