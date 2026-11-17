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

package runtimeprofile

import (
	"testing"

	clicontext "github.com/docker/cli/cli/context"
	dockercontext "github.com/docker/cli/cli/context/docker"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/client/dockerdesktop"
	"github.com/greenmaskio/arcave/internal/client/podman"
	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/imagebackend/dedupcopy"
)

// The engine decides the profile whole: Podman is reached past its compat API, through the
// client's own dialer, held to no Docker version, with its own defaults; Docker through the API itself.
func TestNew(t *testing.T) {
	api, err := client.New(client.WithHost("tcp://10.0.0.5:2376"))
	require.NoError(t, err)
	info := system.Info{Name: "host-1", LoggingDriver: "json-file", DefaultRuntime: "runc"}
	plain := dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2375"}}
	dockerEngine := docker.NewEngine(api)
	libpodClient, err := libpod.NewClient(api, libpod.Options{Version: "6.1.4"})
	require.NoError(t, err)
	podmanAPI := podman.NewAPI(api, libpodClient)
	podmanEngine := docker.NewEngine(podmanAPI)
	desktopInfo := info
	desktopInfo.OperatingSystem = "Docker Desktop"
	desktopAPI := dockerdesktop.NewAPI(api)
	desktopEngine := docker.NewEngine(desktopAPI)
	podmanProfile := Profile{
		Kind:           model.ContainerRuntimePodman,
		API:            podmanAPI,
		Engine:         podmanEngine,
		Volumes:        podman.NewEngine(podmanEngine),
		Version:        "6.1.4",
		MinDaemonMajor: 0,
		Assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
	}
	tests := []struct {
		name     string
		runtime  model.ContainerRuntime
		info     *system.Info
		endpoint dockercontext.Endpoint
		want     Profile
		wantErr  string
	}{
		{
			name:     "docker desktop answers inspects through its own API",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			info:     &desktopInfo,
			endpoint: plain,
			want: Profile{
				Kind:           model.ContainerRuntimeDocker,
				API:            desktopAPI,
				Engine:         desktopEngine,
				Volumes:        desktopEngine,
				Version:        "29.8.2",
				MinDaemonMajor: dedupcopy.MinDaemonMajor,
				Assembler:      composespec.NewAssembler(composespec.NewDockerStamps(desktopInfo)),
			},
		},
		{
			name:     "docker",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: plain,
			want: Profile{
				Kind:           model.ContainerRuntimeDocker,
				API:            api,
				Engine:         dockerEngine,
				Volumes:        dockerEngine,
				Version:        "29.8.2",
				MinDaemonMajor: dedupcopy.MinDaemonMajor,
				Assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
			},
		},
		{
			name:    "docker behind TLS",
			runtime: model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: dockercontext.Endpoint{
				EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376"},
				TLSData:      &clicontext.TLSData{},
			},
			want: Profile{
				Kind:           model.ContainerRuntimeDocker,
				API:            api,
				Engine:         dockerEngine,
				Volumes:        dockerEngine,
				Version:        "29.8.2",
				MinDaemonMajor: dedupcopy.MinDaemonMajor,
				Assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
			},
		},
		{
			name:     "podman",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: plain,
			want:     podmanProfile,
		},
		{
			name:     "podman reporting no version is refused",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimePodman},
			endpoint: plain,
			wantErr:  "reach libpod: no Podman version",
		},
		{
			name:    "podman behind TLS builds the same profile",
			runtime: model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: dockercontext.Endpoint{
				EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376"},
				TLSData:      &clicontext.TLSData{},
			},
			want: podmanProfile,
		},
		{
			name:    "podman behind an https:// host of a context is refused",
			runtime: model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: dockercontext.Endpoint{
				EndpointMeta: dockercontext.EndpointMeta{Host: "https://10.0.0.5:2376"},
				TLSData:      &clicontext.TLSData{},
			},
			wantErr: "name it tcp://",
		},
		{
			name:     "podman behind an http:// host of a context is refused",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: "http://10.0.0.5:2375"}},
			wantErr:  "name it tcp://",
		},
		{
			name:     "docker behind an http:// host of a context is not refused",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: "http://10.0.0.5:2375"}},
			want: Profile{
				Kind:           model.ContainerRuntimeDocker,
				API:            api,
				Engine:         dockerEngine,
				Volumes:        dockerEngine,
				Version:        "29.8.2",
				MinDaemonMajor: dedupcopy.MinDaemonMajor,
				Assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemonInfo := info
			if tt.info != nil {
				daemonInfo = *tt.info
			}

			// Act
			got, err := New(tt.runtime, daemonInfo, api, tt.endpoint)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, Profile{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
