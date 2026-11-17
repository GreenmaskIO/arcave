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

package compose

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/imagebackend/dedupcopy"
)

// The engine decides the profile whole: Podman is reached past its compat API, through the
// client's own dialer, held to no Docker version, with its own defaults; Docker through the API itself.
func TestNewRuntimeProfile(t *testing.T) {
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
	podmanProfile := runtimeProfile{
		api:            podmanAPI,
		engine:         podmanEngine,
		volumes:        podman.NewEngine(podmanEngine),
		version:        "6.1.4",
		minDaemonMajor: 0,
		assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
	}
	tests := []struct {
		name     string
		runtime  model.ContainerRuntime
		info     *system.Info
		endpoint dockercontext.Endpoint
		want     runtimeProfile
		wantErr  string
	}{
		{
			name:     "docker desktop answers inspects through its own API",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			info:     &desktopInfo,
			endpoint: plain,
			want: runtimeProfile{
				api:            desktopAPI,
				engine:         desktopEngine,
				volumes:        desktopEngine,
				version:        "29.8.2",
				minDaemonMajor: dedupcopy.MinDaemonMajor,
				assembler:      composespec.NewAssembler(composespec.NewDockerStamps(desktopInfo)),
			},
		},
		{
			name:     "docker",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: plain,
			want: runtimeProfile{
				api:            api,
				engine:         dockerEngine,
				volumes:        dockerEngine,
				version:        "29.8.2",
				minDaemonMajor: dedupcopy.MinDaemonMajor,
				assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
			},
		},
		{
			name:    "docker behind TLS",
			runtime: model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: dockercontext.Endpoint{
				EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376"},
				TLSData:      &clicontext.TLSData{},
			},
			want: runtimeProfile{
				api:            api,
				engine:         dockerEngine,
				volumes:        dockerEngine,
				version:        "29.8.2",
				minDaemonMajor: dedupcopy.MinDaemonMajor,
				assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
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
			want: runtimeProfile{
				api:            api,
				engine:         dockerEngine,
				volumes:        dockerEngine,
				version:        "29.8.2",
				minDaemonMajor: dedupcopy.MinDaemonMajor,
				assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
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
			got, err := newRuntimeProfile(tt.runtime, daemonInfo, api, tt.endpoint)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, runtimeProfile{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A Podman daemon is reached through libpod over the very connection the client resolved: a
// socket, plain tcp or TLS. ssh is the integration tests'.
func TestReadRuntimeProfile(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/version"):
			_, _ = io.WriteString(w, `{"Version":"6.1.4","Components":[{"Name":"Podman Engine","Version":"6.1.4"}]}`)
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/v6.1.4/libpod/containers/c1/json"):
			_, _ = io.WriteString(w, `{"HostConfig": {"LogConfig": {"Size": "1MB"}}}`)
		case strings.HasSuffix(r.URL.Path, "/containers/c1/json"):
			_, _ = io.WriteString(w, `{"Id": "c1", "HostConfig": {"LogConfig": {"Type": "k8s-file"}}}`)
		default:
			http.NotFound(w, r)
		}
	})
	tests := []struct {
		name    string
		newCase func(t *testing.T) composeconfig.Docker
	}{
		{
			name: "a daemon on a unix socket",
			newCase: func(t *testing.T) composeconfig.Docker {
				t.Helper()
				// t.TempDir is too long a path for a socket on macOS.
				dir, err := os.MkdirTemp("", "arcave")
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
				socket := filepath.Join(dir, "d.sock")
				listener, err := net.Listen("unix", socket)
				require.NoError(t, err)
				server := httptest.NewUnstartedServer(handler)
				server.Listener = listener
				server.Start()
				t.Cleanup(server.Close)
				return composeconfig.Docker{Host: "unix://" + socket}
			},
		},
		{
			name: "a plain daemon",
			newCase: func(t *testing.T) composeconfig.Docker {
				t.Helper()
				server := httptest.NewServer(handler)
				t.Cleanup(server.Close)
				return composeconfig.Docker{Host: "tcp://" + strings.TrimPrefix(server.URL, "http://")}
			},
		},
		{
			name: "a daemon behind TLS",
			newCase: func(t *testing.T) composeconfig.Docker {
				t.Helper()
				server := httptest.NewTLSServer(handler)
				t.Cleanup(server.Close)
				caFile := filepath.Join(t.TempDir(), "ca.pem")
				caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
				require.NoError(t, os.WriteFile(caFile, caPEM, 0o600))
				return composeconfig.Docker{Host: "tcp://" + strings.TrimPrefix(server.URL, "https://"), CAFile: caFile}
			},
		},
		{
			name: "a daemon behind TLS under skip_tls_verify, with no certificate",
			newCase: func(t *testing.T) composeconfig.Docker {
				t.Helper()
				server := httptest.NewTLSServer(handler)
				t.Cleanup(server.Close)
				return composeconfig.Docker{Host: "tcp://" + strings.TrimPrefix(server.URL, "https://"), SkipTLSVerify: true}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			opened := &closers{}
			defer opened.release(context.Background())
			daemon, err := connectDaemon(tt.newCase(t), io.Discard, opened)
			require.NoError(t, err)
			libpodClient, err := libpod.NewClient(daemon.api, libpod.Options{Version: "6.1.4"})
			require.NoError(t, err)

			// Act
			got, err := readRuntimeProfile(context.Background(), daemon)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, podman.NewAPI(daemon.api, libpodClient), got.api)
			inspect, err := got.api.ContainerInspect(context.Background(), "c1", client.ContainerInspectOptions{})
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"max-size": "1MB"}, inspect.Container.HostConfig.LogConfig.Config)
		})
	}
}
