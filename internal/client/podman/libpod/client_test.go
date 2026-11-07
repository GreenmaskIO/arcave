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

package libpod

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const recordedVolumeSource = "/var/home/core/.local/share/containers/storage/volumes/arcave-fixture/_data"

// bareImageID is an image id as libpod answers a commit with: no algorithm.
const bareImageID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeConnection struct {
	dial func(context.Context) (net.Conn, error)
	host string
}

func (f fakeConnection) Dialer() func(context.Context) (net.Conn, error) { return f.dial }
func (f fakeConnection) DaemonHost() string                              { return f.host }

// dialTo dials server's listener, as the SDK's dialer does a tcp host.
func dialTo(server *httptest.Server) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
}

// newTestClient is a Client whose daemon answers with handler.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(fakeConnection{
		dial: dialTo(server),
		host: "tcp://" + strings.TrimPrefix(server.URL, "http://"),
	}, Options{Version: "6.1.4"})
	require.NoError(t, err)
	return client
}

// A container is read off libpod's own inspect, by the names libpod reports its fields under.
func TestClient_InspectContainer(t *testing.T) {
	recorded, err := os.ReadFile("testdata/container_inspect.json")
	require.NoError(t, err)
	tests := []struct {
		name    string
		status  int
		body    string
		want    Container
		wantErr string
	}{
		{
			name:   "as a real Podman answers",
			status: http.StatusOK,
			body:   string(recorded),
			want: Container{
				Mounts: []Mount{{
					Type:        "volume",
					Name:        "arcave-fixture",
					Source:      recordedVolumeSource,
					Destination: "/data",
					Options:     []string{"nosuid", "nodev", "rbind"},
					RW:          true,
					Propagation: "rprivate",
				}, {
					Type:        "volume",
					Name:        "arcave-fixture",
					Source:      recordedVolumeSource,
					Destination: "/sub",
					Options:     []string{"nosuid", "nodev", "rbind"},
					Propagation: "rprivate",
					SubPath:     "sub",
				}, {
					Type:        "bind",
					Source:      "/srv",
					Destination: "/long",
					Options:     []string{"bind"},
					Propagation: "rshared",
				}, {
					Type:        "bind",
					Source:      "/tmp",
					Destination: "/short",
					Options:     []string{"nosuid", "nodev", "rbind"},
					RW:          true,
					Propagation: "rprivate",
				}},
				HostConfig: HostConfig{LogConfig: LogConfig{Size: "1MB"}},
			},
		},
		{
			name:    "an answer that is no JSON",
			status:  http.StatusOK,
			body:    "not json",
			wantErr: "decode libpod's answer: ",
		},
		{
			name:    "a container libpod does not have",
			status:  http.StatusNotFound,
			body:    `{"cause":"no such container"}`,
			wantErr: `libpod answered 404 Not Found: {"cause":"no such container"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var gotMethod, gotPath string
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			// Act
			got, err := client.InspectContainer(context.Background(), "c1")

			// Assert
			assert.Equal(t, http.MethodGet, gotMethod)
			assert.Equal(t, "/v6.1.4/libpod/containers/c1/json", gotPath)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, Container{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A commit is one POST carrying its options as a query; the id comes back as libpod spells it.
func TestClient_CommitContainer(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr string
	}{
		{
			name:   "the id as libpod answers it",
			status: http.StatusOK,
			body:   `{"Id":"` + bareImageID + `"}`,
			want:   bareImageID,
		},
		{
			name:   "any 2xx is an answer",
			status: http.StatusCreated,
			body:   `{"Id":"` + bareImageID + `"}`,
			want:   bareImageID,
		},
		{
			name:    "an answer that is no JSON",
			status:  http.StatusOK,
			body:    "not json",
			wantErr: "decode libpod's answer: invalid character 'o' in literal null (expecting 'u')",
		},
		{
			name:    "a container libpod does not have",
			status:  http.StatusInternalServerError,
			body:    `{"cause":"no such container"}`,
			wantErr: `libpod answered 500 Internal Server Error: {"cause":"no such container"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var gotMethod, gotPath string
			var gotQuery url.Values
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.Query()
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			// Act
			got, err := client.CommitContainer(context.Background(), CommitOptions{
				Container: "c1",
				Format:    CommitFormatOCI,
				Pause:     false,
				Changes:   []string{`LABEL "io.arcave.a"="one two"`, `LABEL "io.arcave.b"="2"`},
			})

			// Assert
			assert.Equal(t, http.MethodPost, gotMethod)
			assert.Equal(t, "/v6.1.4/libpod/commit", gotPath)
			assert.Equal(t, url.Values{
				"container": {"c1"},
				"format":    {"oci"},
				"pause":     {"false"},
				"changes":   {`LABEL "io.arcave.a"="one two"`, `LABEL "io.arcave.b"="2"`},
			}, gotQuery)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// An image streams back in the format asked for, its body the caller's to close.
func TestClient_ExportImage(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "the archive streams back", status: http.StatusOK, body: "oci-archive-bytes"},
		{
			name:    "an image libpod does not have",
			status:  http.StatusNotFound,
			body:    `{"cause":"image not known"}`,
			wantErr: `libpod answered 404 Not Found: {"cause":"image not known"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var gotMethod, gotPath, gotFormat string
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotFormat = r.Method, r.URL.Path, r.URL.Query().Get("format")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			// Act
			archive, err := client.ExportImage(context.Background(), "sha256:"+bareImageID, ExportFormatOCIArchive)

			// Assert
			assert.Equal(t, http.MethodGet, gotMethod)
			assert.Equal(t, "/v6.1.4/libpod/images/sha256:"+bareImageID+"/get", gotPath)
			assert.Equal(t, "oci-archive", gotFormat)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, archive)
				return
			}
			require.NoError(t, err)
			defer archive.Close()
			got, err := io.ReadAll(archive)
			require.NoError(t, err)
			assert.Equal(t, tt.body, string(got))
		})
	}
}

// An answer other than 2xx is an error carrying libpod's own message, cut at maxErrorBytes; only a
// 404 reads as not found.
func TestClient_call(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantMsg      string
		wantNotFound bool
	}{
		{
			name:         "a short refusal, whole",
			status:       http.StatusNotFound,
			body:         `{"cause":"image not known"}` + "\n",
			wantMsg:      `libpod answered 404 Not Found: {"cause":"image not known"}`,
			wantNotFound: true,
		},
		{
			name:    "a long one, its first bytes",
			status:  http.StatusInternalServerError,
			body:    strings.Repeat("x", maxErrorBytes+1024),
			wantMsg: "libpod answered 500 Internal Server Error: " + strings.Repeat("x", maxErrorBytes),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			// Act
			resp, err := client.call(context.Background(), http.MethodGet, nil, "images", "i1", "get")

			// Assert
			require.EqualError(t, err, tt.wantMsg)
			assert.Equal(t, tt.wantNotFound, cerrdefs.IsNotFound(err))
			assert.Nil(t, resp)
		})
	}
}

// A path on a tcp host stays in front of every request, as the SDK keeps it on its own.
func TestClient_call_HostPath(t *testing.T) {
	// Arrange
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer server.Close()
	client, err := NewClient(fakeConnection{
		dial: dialTo(server),
		host: "tcp://" + strings.TrimPrefix(server.URL, "http://") + "/prefix",
	}, Options{Version: "6.1.4"})
	require.NoError(t, err)

	// Act
	resp, err := client.call(context.Background(), http.MethodGet, nil, "images", "i1", "get")

	// Assert
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "/prefix/v6.1.4/libpod/images/i1/get", gotPath)
}

// A request goes out on the caller's context, so a cancelled command stops it.
func TestClient_call_Cancelled(t *testing.T) {
	// Arrange
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Act
	resp, err := client.call(ctx, http.MethodGet, nil, "images", "i1", "get")

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, resp)
}

// A connection whose dialer speaks TLS reaches libpod over it.
func TestClient_InspectContainer_OverTLS(t *testing.T) {
	// Arrange
	var gotTLS bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTLS = r.TLS != nil
		_, _ = io.WriteString(w, `{"HostConfig": {"LogConfig": {"Size": "1MB"}}}`)
	}))
	defer server.Close()
	tlsConfig := server.Client().Transport.(*http.Transport).TLSClientConfig
	client, err := NewClient(fakeConnection{
		dial: func(ctx context.Context) (net.Conn, error) {
			dialer := tls.Dialer{Config: tlsConfig}
			return dialer.DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
		host: "tcp://" + strings.TrimPrefix(server.URL, "https://"),
	}, Options{Version: "6.1.4"})
	require.NoError(t, err)

	// Act
	got, err := client.InspectContainer(context.Background(), "c1")

	// Assert
	require.NoError(t, err)
	assert.True(t, gotTLS)
	assert.Equal(t, "1MB", got.HostConfig.LogConfig.Size)
}

// A host that cannot be parsed, or no version to name in the path, fails the construction.
func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		options Options
		wantErr string
	}{
		{
			name:    "a host that cannot be parsed",
			host:    "://nonsense",
			options: Options{Version: "6.1.4"},
			wantErr: `parse daemon host "://nonsense"`,
		},
		{
			name:    "no version",
			host:    "tcp://10.0.0.5:2375",
			wantErr: "no Podman version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := NewClient(fakeConnection{host: tt.host}, tt.options)

			// Assert
			require.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, got)
		})
	}
}

// Only a tcp host is used as it is, over http, the dialer speaking any TLS; any other is
// addressed by a placeholder.
func TestNewBaseURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "a unix socket", host: "unix:///run/podman/podman.sock", want: "http://docker"},
		{name: "ssh", host: "ssh://core@127.0.0.1:54651", want: "http://docker"},
		{name: "ssh, as the client reports it", host: "http://docker.example.com", want: "http://docker"},
		{name: "tcp", host: "tcp://10.0.0.5:2376", want: "http://10.0.0.5:2376"},
		{name: "tcp with a path", host: "tcp://10.0.0.5:2376/prefix", want: "http://10.0.0.5:2376/prefix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := newBaseURL(tt.host)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}
