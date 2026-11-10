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

package dockerdesktop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// recordedID is the container testdata/ holds a real Docker Desktop's answers for: a volume and a
// bind through each API, the volume first, and the mount API's bind over a symlink.
const recordedID = "47e52eec6aaa60fe521a2dd9e23ef17f7441d94ba3c01a7b6b7eaf602f658bec"

// newRecordedDaemon is a client of a daemon answering for recordedID as a real Docker Desktop did.
func newRecordedDaemon(t *testing.T) *client.Client {
	t.Helper()
	inspect, err := os.ReadFile("testdata/container_inspect.json")
	require.NoError(t, err)
	list, err := os.ReadFile("testdata/container_list.json")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/"+recordedID+"/json"):
			_, _ = w.Write(inspect)
		case strings.HasSuffix(r.URL.Path, "/containers/json") &&
			strings.Contains(r.URL.Query().Get("filters"), recordedID):
			_, _ = w.Write(list)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	api, err := client.New(
		client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")),
		client.WithHTTPClient(server.Client()),
		client.WithAPIVersion("1.47"),
	)
	require.NoError(t, err)
	return api
}

// What a real Docker Desktop answered, off the wire, comes out with every bind request as it was
// asked for, and every bind mount on the directory it is on the host.
func TestAPI_ContainerInspect_RecordedDaemon(t *testing.T) {
	// Arrange
	api := NewAPI(newRecordedDaemon(t))

	// Act
	got, err := api.ContainerInspect(context.Background(), recordedID, client.ContainerInspectOptions{})

	// Assert
	require.NoError(t, err)
	require.NotNil(t, got.Container.HostConfig)
	assert.Equal(t, []string{
		"arcave-desktop-fixture:/v",
		"/tmp/arcave-desktop-fixture/conf:/conf:ro",
	}, got.Container.HostConfig.Binds)
	assert.Equal(t, []mount.Mount{{
		Type:          mount.TypeVolume,
		Source:        "arcave-desktop-fixture",
		Target:        "/m",
		VolumeOptions: &mount.VolumeOptions{NoCopy: true},
	}, {
		Type:   mount.TypeBind,
		Source: "/tmp/arcave-desktop-fixture/link",
		Target: "/data",
	}}, got.Container.HostConfig.Mounts)
	sources := map[string]string{}
	for _, mountPoint := range got.Container.Mounts {
		sources[mountPoint.Destination] = mountPoint.Source
	}
	assert.Equal(t, map[string]string{
		"/conf": "/private/tmp/arcave-desktop-fixture/conf",
		"/data": "/private/tmp/arcave-desktop-fixture/data",
		"/m":    "/var/lib/docker/volumes/arcave-desktop-fixture/_data",
		"/v":    "/var/lib/docker/volumes/arcave-desktop-fixture/_data",
	}, sources)
	assert.Contains(t, string(got.Raw), "/host_mnt/private/tmp/arcave-desktop-fixture/conf", "the raw answer as it came")
}

func TestWithHostSources(t *testing.T) {
	tests := []struct {
		name        string
		hostConfig  container.HostConfig
		hostSources map[string]string
		want        container.HostConfig
	}{
		{
			name: "the recorded path, a symlink's included, over the VM's",
			hostConfig: container.HostConfig{
				Binds:  []string{"/host_mnt/private/tmp/conf:/conf:ro"},
				Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/host_mnt/private/tmp/data", Target: "/data"}},
			},
			hostSources: map[string]string{"/conf": "/tmp/conf", "/data": "/tmp/link"},
			want: container.HostConfig{
				Binds:  []string{"/tmp/conf:/conf:ro"},
				Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/tmp/link", Target: "/data"}},
			},
		},
		{
			name: "with nothing recorded, the VM's path without its host mount",
			hostConfig: container.HostConfig{
				Binds:  []string{"/host_mnt/Users/dev/conf:/conf"},
				Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/host_mnt/private/var/data", Target: "/data"}},
			},
			want: container.HostConfig{
				Binds:  []string{"/Users/dev/conf:/conf"},
				Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/private/var/data", Target: "/data"}},
			},
		},
		{
			name: "a host path, another VM's path, a volume and what is no request stay",
			hostConfig: container.HostConfig{
				Binds: []string{"/srv/conf:/conf", "data:/data", "/run/desktop/mnt/host/c/app:/app", "/host_mnt/lone"},
				Mounts: []mount.Mount{
					{Type: mount.TypeVolume, Source: "cache", Target: "/cache"},
					{Type: mount.TypeBind, Source: "/host_mntx/etc", Target: "/etc/x"},
					{Type: mount.TypeBind, Source: "/host_mnt", Target: "/root"},
				},
			},
			hostSources: map[string]string{"/cache": "/srv/cache"},
			want: container.HostConfig{
				Binds: []string{"/srv/conf:/conf", "data:/data", "/run/desktop/mnt/host/c/app:/app", "/host_mnt/lone"},
				Mounts: []mount.Mount{
					{Type: mount.TypeVolume, Source: "cache", Target: "/cache"},
					{Type: mount.TypeBind, Source: "/host_mntx/etc", Target: "/etc/x"},
					{Type: mount.TypeBind, Source: "/host_mnt", Target: "/root"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			hostConfig := tt.hostConfig
			mounts := []container.MountPoint{{Type: mount.TypeBind, Source: "/host_mnt/private/tmp/data", Destination: "/data"}}
			inspect := container.InspectResponse{HostConfig: &hostConfig, Mounts: mounts}

			// Act
			got := withHostSources(inspect, tt.hostSources)

			// Assert
			assert.Equal(t, &tt.want, got.HostConfig)
			assert.Equal(t, mounts, got.Mounts, "where the VM mounted each bind stays")
			assert.Equal(t, tt.hostConfig, hostConfig, "the inspect is not rewritten in place")
		})
	}
}

func TestWithResolvedMounts(t *testing.T) {
	// Arrange
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))
	hostConfig := container.HostConfig{Binds: []string{link + ":/link"}}
	mounts := []container.MountPoint{
		{Type: mount.TypeBind, Source: "/host_mnt" + dir, Destination: "/vm"},
		{Type: mount.TypeBind, Source: dir, Destination: "/host"},
		{Type: mount.TypeBind, Source: link, Destination: "/link"},
		{Type: mount.TypeBind, Source: "/gone/from/this/host", Destination: "/gone"},
		{Type: mount.TypeVolume, Source: "/host_mnt/cache", Destination: "/cache"},
	}
	inspect := container.InspectResponse{HostConfig: &hostConfig, Mounts: slices.Clone(mounts)}

	// Act
	got := withResolvedMounts(inspect)

	// Assert
	assert.Equal(t, []container.MountPoint{
		{Type: mount.TypeBind, Source: dir, Destination: "/vm"},
		{Type: mount.TypeBind, Source: dir, Destination: "/host"},
		{Type: mount.TypeBind, Source: dir, Destination: "/link"},
		{Type: mount.TypeBind, Source: "/gone/from/this/host", Destination: "/gone"},
		{Type: mount.TypeVolume, Source: "/host_mnt/cache", Destination: "/cache"},
	}, got.Mounts, "one directory, however Docker Desktop named it")
	assert.Equal(t, &hostConfig, got.HostConfig, "the requests stay")
	assert.Equal(t, mounts, inspect.Mounts, "the inspect is not rewritten in place")
}

func TestWithHostSources_NoHostConfig(t *testing.T) {
	// Arrange
	inspect := container.InspectResponse{Mounts: []container.MountPoint{
		{Type: mount.TypeBind, Source: "/host_mnt/srv", Destination: "/srv"},
	}}

	// Act
	got := withHostSources(inspect, map[string]string{"/srv": "/srv"})

	// Assert
	assert.Equal(t, inspect, got)
}

func TestParseHostSources(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   map[string]string
	}{
		{
			name:   "no Docker Desktop label",
			labels: map[string]string{"com.docker.compose.service": "app"},
			want:   map[string]string{},
		},
		{
			name: "both APIs, each numbered on its own",
			labels: map[string]string{
				"desktop.docker.io/binds/1/Source":     "/tmp/conf",
				"desktop.docker.io/binds/1/SourceKind": "hostFile",
				"desktop.docker.io/binds/1/Target":     "/conf",
				"desktop.docker.io/mounts/1/Source":    "/tmp/link",
				"desktop.docker.io/mounts/1/Target":    "/data",
				"desktop.docker.io/ports.scheme":       "v2",
			},
			want: map[string]string{"/conf": "/tmp/conf", "/data": "/tmp/link"},
		},
		{
			name:   "a source with no target names no bind",
			labels: map[string]string{"desktop.docker.io/mounts/0/Source": "/tmp/link"},
			want:   map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := parseHostSources(tt.labels)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAPI_ContainerInspect(t *testing.T) {
	wantErr := errors.New("daemon gone")
	listed := client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{"id": {"c1": true}},
	}
	tests := []struct {
		name      string
		setup     func(daemon *mockAPI)
		wantBinds []string
		wantErr   error
	}{
		{
			name: "a container gone before the list keeps what the VM's path tells",
			setup: func(daemon *mockAPI) {
				daemon.On("ContainerInspect", mock.Anything, "c1", client.ContainerInspectOptions{}).
					Return(container.InspectResponse{
						ID:         "c1",
						HostConfig: &container.HostConfig{Binds: []string{"/host_mnt/srv:/srv"}},
					}, nil).Once()
				daemon.On("ContainerList", mock.Anything, listed).Return([]container.Summary(nil), nil).Once()
			},
			wantBinds: []string{"/srv:/srv"},
		},
		{
			name: "an inspect asking for no VM's path is not listed",
			setup: func(daemon *mockAPI) {
				daemon.On("ContainerInspect", mock.Anything, "c1", client.ContainerInspectOptions{}).
					Return(container.InspectResponse{
						ID:         "c1",
						HostConfig: &container.HostConfig{Binds: []string{"/srv:/srv", "data:/data"}},
						Mounts:     []container.MountPoint{{Type: mount.TypeBind, Source: "/host_mnt/srv", Destination: "/srv"}},
					}, nil).Once()
			},
			wantBinds: []string{"/srv:/srv", "data:/data"},
		},
		{
			name: "a failed inspect",
			setup: func(daemon *mockAPI) {
				daemon.On("ContainerInspect", mock.Anything, "c1", client.ContainerInspectOptions{}).
					Return(container.InspectResponse{}, wantErr).Once()
			},
			wantErr: wantErr,
		},
		{
			name: "a failed list",
			setup: func(daemon *mockAPI) {
				daemon.On("ContainerInspect", mock.Anything, "c1", client.ContainerInspectOptions{}).
					Return(container.InspectResponse{
						ID:         "c1",
						HostConfig: &container.HostConfig{Binds: []string{"/host_mnt/srv:/srv"}},
					}, nil).Once()
				daemon.On("ContainerList", mock.Anything, listed).Return([]container.Summary(nil), wantErr).Once()
			},
			wantErr: wantErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockAPI{}
			tt.setup(daemon)

			// Act
			got, err := NewAPI(daemon).ContainerInspect(context.Background(), "c1", client.ContainerInspectOptions{})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, client.ContainerInspectResult{}, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got.Container.HostConfig)
				assert.Equal(t, tt.wantBinds, got.Container.HostConfig.Binds)
			}
			daemon.AssertExpectations(t)
		})
	}
}
