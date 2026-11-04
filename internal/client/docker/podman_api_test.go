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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compatInspect is what Podman's compat inspect answers: every mount in Binds, no mount
// requests, a log with no options.
const compatInspect = `{
  "Id": "c1",
  "Name": "/demo-db-1",
  "HostConfig": {
    "Binds": ["pgdata:/data:rprivate,nosuid,nodev,rbind", "pgdata:/sub:rprivate,nosuid,nodev,rbind"],
    "Mounts": null,
    "LogConfig": {"Type": "json-file", "Config": null}
  },
  "Config": {"Image": "postgres:16"}
}`

// newPodmanServer serves compatInspect and libpod's inspect, the latter answering libpodStatus.
func newPodmanServer(t *testing.T, libpodStatus int, libpodBody string) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.44/containers/c1/json":
			_, _ = io.WriteString(w, compatInspect)
		case "/v1.44/libpod/containers/c1/json":
			w.WriteHeader(libpodStatus)
			_, _ = io.WriteString(w, libpodBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	api, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")),
		client.WithHTTPClient(server.Client()),
		client.WithVersion("1.44"),
	)
	require.NoError(t, err)
	return api
}

// newPodmanAPI reaches libpod over api's own plain connection.
func newPodmanAPI(api *client.Client) *PodmanAPI { return NewPodmanAPI(api, NewLibpod(api)) }

// A volume at a subpath and a bind of the mount API come back as their mount requests,
// and the log's size as its max-size; every other mount is left to Binds.
func TestPodmanAPI_ContainerInspect(t *testing.T) {
	tests := []struct {
		name        string
		libpod      string
		wantMounts  []mount.Mount
		wantLogging map[string]string
	}{
		{
			name: "a subpath and a log size",
			libpod: `{
  "Mounts": [
    {"Type": "volume", "Name": "pgdata", "Destination": "/data", "RW": true},
    {"Type": "volume", "Name": "pgdata", "Destination": "/sub", "RW": false, "SubPath": "sub"},
    {"Type": "bind", "Source": "/tmp", "Destination": "/short", "Options": ["nosuid", "nodev", "rbind"], "RW": true, "Propagation": "rprivate"},
    {"Type": "bind", "Source": "/srv", "Destination": "/long", "Options": ["bind", "nosuid"], "RW": false, "Propagation": "rshared"}
  ],
  "HostConfig": {"LogConfig": {"Type": "k8s-file", "Size": "1MB"}}
}`,
			wantMounts: []mount.Mount{{
				Type:          mount.TypeVolume,
				Source:        "pgdata",
				Target:        "/sub",
				ReadOnly:      true,
				VolumeOptions: &mount.VolumeOptions{Subpath: "sub"},
			}, {
				Type:        mount.TypeBind,
				Source:      "/srv",
				Target:      "/long",
				ReadOnly:    true,
				BindOptions: &mount.BindOptions{Propagation: mount.PropagationRShared},
			}},
			wantLogging: map[string]string{"max-size": "1MB"},
		},
		{
			name:   "neither: libpod reports no size as a negative one",
			libpod: `{"Mounts": [{"Type": "volume", "Name": "pgdata", "Destination": "/data", "RW": true}], "HostConfig": {"LogConfig": {"Size": "-1B"}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := newPodmanAPI(newPodmanServer(t, http.StatusOK, tt.libpod))

			// Act
			got, err := api.ContainerInspect(context.Background(), "c1")

			// Assert
			require.NoError(t, err)
			require.NotNil(t, got.HostConfig)
			assert.Equal(t, []string{"pgdata:/data:rprivate,nosuid,nodev,rbind", "pgdata:/sub:rprivate,nosuid,nodev,rbind"},
				got.HostConfig.Binds)
			assert.Equal(t, tt.wantMounts, got.HostConfig.Mounts)
			assert.Equal(t, container.LogConfig{Type: "json-file", Config: tt.wantLogging}, got.HostConfig.LogConfig)
		})
	}
}

// A container libpod cannot inspect fails the inspect with libpod's own message.
func TestPodmanAPI_ContainerInspect_LibpodRefused(t *testing.T) {
	// Arrange
	api := newPodmanAPI(newPodmanServer(t, http.StatusNotFound, `{"cause":"no such container"}`))

	// Act
	_, err := api.ContainerInspect(context.Background(), "c1")

	// Assert
	require.ErrorContains(t, err, `inspect container c1 through libpod: libpod answered 404 Not Found: {"cause":"no such container"}`)
}

// libpod's size becomes max-size beside any other option, unless libpod reports none
// or a size it cannot read.
func TestApplyLogSize(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]string
		size    string
		want    map[string]string
	}{
		{name: "a size", size: "1MB", want: map[string]string{"max-size": "1MB"}},
		{name: "beside other options", options: map[string]string{"tag": "x"}, size: "1MB", want: map[string]string{"tag": "x", "max-size": "1MB"}},
		{name: "over a max-size compat carried", options: map[string]string{"max-size": "5MB"}, size: "1MB", want: map[string]string{"max-size": "1MB"}},
		{name: "none, reported negative", size: "-1B"},
		{name: "unreadable", size: "lots"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logConfig := container.LogConfig{Type: "json-file", Config: tt.options}

			// Act
			applyLogSize(&logConfig, tt.size)
			got := logConfig

			// Assert
			assert.Equal(t, container.LogConfig{Type: "json-file", Config: tt.want}, got)
		})
	}
}
