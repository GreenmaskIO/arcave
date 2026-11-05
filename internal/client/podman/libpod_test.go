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

package podman

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bareImageID is an image id as libpod answers a commit with: no algorithm.
const bareImageID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeLibpod struct {
	client *http.Client
	host   string
}

func (f fakeLibpod) HTTPClient() *http.Client { return f.client }
func (f fakeLibpod) DaemonHost() string       { return f.host }
func (f fakeLibpod) ClientVersion() string    { return "1.41" }

// newTestLibpod is a Libpod whose daemon answers with handler.
func newTestLibpod(t *testing.T, handler http.HandlerFunc) Libpod {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewLibpod(fakeLibpod{
		client: server.Client(),
		host:   "tcp://" + strings.TrimPrefix(server.URL, "http://"),
	})
}

// A container is read off libpod's own inspect, by the names libpod reports its fields under.
func TestLibpod_inspectContainer(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    libpodContainer
		wantErr string
	}{
		{
			name:   "mounts and a log size",
			status: http.StatusOK,
			body: `{
  "Id": "c1",
  "Mounts": [
    {"Type": "volume", "Name": "pgdata", "Source": "/var/lib/containers/pgdata/_data", "Destination": "/sub", "RW": false, "SubPath": "sub"},
    {"Type": "bind", "Source": "/srv", "Destination": "/long", "Options": ["bind", "nosuid"], "RW": true, "Propagation": "rshared"}
  ],
  "HostConfig": {"LogConfig": {"Type": "k8s-file", "Size": "1MB"}}
}`,
			want: libpodContainer{
				Mounts: []libpodMount{{
					Type:        "volume",
					Name:        "pgdata",
					Source:      "/var/lib/containers/pgdata/_data",
					Destination: "/sub",
					SubPath:     "sub",
				}, {
					Type:        "bind",
					Source:      "/srv",
					Destination: "/long",
					Options:     []string{"bind", "nosuid"},
					RW:          true,
					Propagation: "rshared",
				}},
				HostConfig: libpodHostConfig{LogConfig: libpodLogConfig{Size: "1MB"}},
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
			libpod := newTestLibpod(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			// Act
			got, err := libpod.inspectContainer(context.Background(), "c1")

			// Assert
			assert.Equal(t, http.MethodGet, gotMethod)
			assert.Equal(t, "/v1.41/libpod/containers/c1/json", gotPath)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, libpodContainer{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A commit is one POST carrying what it was asked for as a query; the id comes back as
// libpod spells it.
func TestLibpod_commitContainer(t *testing.T) {
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
			libpod := newTestLibpod(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.Query()
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			// Act
			got, err := libpod.commitContainer(context.Background(), libpodCommit{
				Container: "c1",
				Format:    "oci",
				Pause:     false,
				Changes:   []string{`LABEL "io.arcave.a"="one two"`, `LABEL "io.arcave.b"="2"`},
			})

			// Assert
			assert.Equal(t, http.MethodPost, gotMethod)
			assert.Equal(t, "/v1.41/libpod/commit", gotPath)
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
func TestLibpod_exportImage(t *testing.T) {
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
			libpod := newTestLibpod(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotFormat = r.Method, r.URL.Path, r.URL.Query().Get("format")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			// Act
			archive, err := libpod.exportImage(context.Background(), "sha256:"+bareImageID, "oci-archive")

			// Assert
			assert.Equal(t, http.MethodGet, gotMethod)
			assert.Equal(t, "/v1.41/libpod/images/sha256:"+bareImageID+"/get", gotPath)
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

// An answer other than 2xx is an error carrying libpod's own message, cut at
// maxLibpodErrorBytes.
func TestLibpod_call(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantMsg string
	}{
		{
			name:    "a short refusal, whole",
			status:  http.StatusNotFound,
			body:    `{"cause":"image not known"}` + "\n",
			wantMsg: `libpod answered 404 Not Found: {"cause":"image not known"}`,
		},
		{
			name:    "a long one, its first bytes",
			status:  http.StatusInternalServerError,
			body:    strings.Repeat("x", maxLibpodErrorBytes+1024),
			wantMsg: "libpod answered 500 Internal Server Error: " + strings.Repeat("x", maxLibpodErrorBytes),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			libpod := newTestLibpod(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})

			// Act
			resp, err := libpod.call(context.Background(), http.MethodGet, nil, "images", "i1", "get")

			// Assert
			require.EqualError(t, err, tt.wantMsg)
			assert.Nil(t, resp)
		})
	}
}

// A socket the transport dials itself is addressed by a placeholder host; a tcp host is
// used as it is, plain.
func TestNewLibpodBaseURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "a unix socket", host: "unix:///run/podman/podman.sock", want: "http://docker"},
		{name: "ssh", host: "ssh://core@127.0.0.1:54651", want: "http://docker"},
		{name: "tcp is reached plain", host: "tcp://10.0.0.5:2375", want: "http://10.0.0.5:2375"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := newLibpodBaseURL(NewLibpod(fakeLibpod{client: &http.Client{}, host: tt.host}))

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}
