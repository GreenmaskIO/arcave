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

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/client/podman"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/utils"
)

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
			opened := &utils.Closers{}
			defer opened.Release(context.Background())
			daemon, err := connectDaemon(tt.newCase(t), io.Discard, opened)
			require.NoError(t, err)
			libpodClient, err := libpod.NewClient(daemon.api, libpod.Options{Version: "6.1.4"})
			require.NoError(t, err)

			// Act
			got, err := readRuntimeProfile(context.Background(), daemon)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, podman.NewAPI(daemon.api, libpodClient), got.API)
			inspect, err := got.API.ContainerInspect(context.Background(), "c1", client.ContainerInspectOptions{})
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"max-size": "1MB"}, inspect.Container.HostConfig.LogConfig.Config)
		})
	}
}
