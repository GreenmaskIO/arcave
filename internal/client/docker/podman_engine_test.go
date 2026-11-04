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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const podmanImageID = model.ImageDigestID("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

type fakeLibpod struct {
	client *http.Client
	host   string
}

func (f fakeLibpod) HTTPClient() *http.Client { return f.client }
func (f fakeLibpod) DaemonHost() string       { return f.host }
func (f fakeLibpod) ClientVersion() string    { return "1.41" }

// An image is saved through libpod as an OCI archive; a refusal carries libpod's
// own message.
func TestPodmanEngine_SaveImage(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "the archive streams back", status: http.StatusOK, body: "oci-archive-bytes"},
		{name: "an image libpod does not have", status: http.StatusNotFound, body: `{"cause":"image not known"}`,
			wantErr: "libpod answered 404 Not Found: {\"cause\":\"image not known\"}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var gotPath, gotFormat string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotFormat = r.URL.Path, r.URL.Query().Get("format")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			e := NewPodmanEngine(NewEngine(&mockAPI{}), NewLibpod(fakeLibpod{
				client: server.Client(),
				host:   "tcp://" + strings.TrimPrefix(server.URL, "http://"),
			}))

			// Act
			archive, err := e.SaveImage(context.Background(), podmanImageID)

			// Assert
			assert.Equal(t, "/v1.41/libpod/images/"+podmanImageID.String()+"/get", gotPath)
			assert.Equal(t, "oci-archive", gotFormat)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
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

// expectVolumeExport drives the export container through to exitCode, its output hj's.
func expectVolumeExport(api *mockAPI, hj *fakeHijack, exitCode int64) (create, remove *mock.Call) {
	expectImagePresent(api, volumeToolsImage)
	create = api.On("ContainerCreate", mock.Anything,
		mock.MatchedBy(func(cfg *container.Config) bool {
			return cfg.Image == volumeToolsImage && slices.Equal([]string(cfg.Cmd), volumeExportCmd)
		}),
		&container.HostConfig{
			Binds:       []string{"demo_pgdata:" + volumeHolderMount + ":ro"},
			SecurityOpt: []string{"label=disable"},
			LogConfig:   container.LogConfig{Type: "none"},
		},
		mock.Anything, mock.Anything, "").
		Return(container.CreateResponse{ID: oneShotID}, nil).Once()
	attach := api.On("ContainerAttach", mock.Anything, oneShotID,
		container.AttachOptions{Stream: true, Stdout: true, Stderr: true}).Return(hj.resp, nil).Once()
	start := api.On("ContainerStart", mock.Anything, oneShotID, container.StartOptions{}).Return(nil).Once()
	status := make(chan container.WaitResponse, 1)
	status <- container.WaitResponse{StatusCode: exitCode}
	api.On("ContainerWait", mock.Anything, oneShotID, container.WaitConditionNotRunning).
		Return((<-chan container.WaitResponse)(status), (<-chan error)(make(chan error))).Maybe()
	remove = api.On("ContainerRemove", mock.Anything, oneShotID, container.RemoveOptions{Force: true}).Return(nil).Once()
	mock.InOrder(create, attach, start, remove)
	return create, remove
}

// A volume comes out as the tar a container of arcave's own writes, since Podman's archive
// API hands every file to root; a tar that fails fails the stream's last read with its stderr.
func TestPodmanEngine_OpenVolume(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int64
		wantExit bool
	}{
		{name: "the archive streams out"},
		{name: "a tar that fails", exitCode: 1, wantExit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			hj := newFakeHijack()
			go func() {
				_, _ = io.WriteString(hj.stdout, "tar-bytes")
				if tt.wantExit {
					_, _ = io.WriteString(hj.stderr, "tar: ./pgdata: Permission denied\n")
				}
				hj.closeOutput()
			}()
			api := &mockAPI{}
			expectVolumeExport(api, hj, tt.exitCode)
			if tt.wantExit {
				// The run keeps no log: the daemon refuses to read one back.
				api.On("ContainerLogs", mock.Anything, oneShotID, mock.Anything).
					Return(io.NopCloser(strings.NewReader("")), errors.New("configured logging driver does not support reading")).Once()
			}
			e := NewPodmanEngine(NewEngine(api), NewLibpod(fakeLibpod{}))

			// Act
			rc, openErr := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"})
			got, readErr := io.ReadAll(rc)
			closeErr := rc.Close()

			// Assert
			require.NoError(t, openErr)
			require.NoError(t, closeErr)
			assert.Equal(t, "tar-bytes", string(got))
			if tt.wantExit {
				var exitErr *model.ContainerExitError
				require.ErrorAs(t, readErr, &exitErr)
				assert.Equal(t, tt.exitCode, exitErr.StatusCode)
				assert.ErrorContains(t, readErr, "export volume demo_pgdata")
				assert.ErrorContains(t, readErr, "(stderr: tar: ./pgdata: Permission denied)")
			} else {
				assert.NoError(t, readErr)
			}
			api.AssertExpectations(t)
		})
	}
}

// A stream closed before its end stops the export and removes its container.
func TestPodmanEngine_OpenVolume_ClosedEarly(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	go func() { _, _ = io.WriteString(hj.stdout, strings.Repeat("x", 1<<20)) }()
	api := &mockAPI{}
	_, remove := expectVolumeExport(api, hj, 0)
	removed := make(chan struct{})
	remove.Run(func(mock.Arguments) { close(removed) })
	e := NewPodmanEngine(NewEngine(api), NewLibpod(fakeLibpod{}))

	// Act
	rc, openErr := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"})
	_, readErr := rc.Read(make([]byte, 16))
	closeErr := rc.Close()

	// Assert
	require.NoError(t, openErr)
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	assertClosed(t, removed, "closing the stream must remove the export's container")
	api.AssertExpectations(t)
}

// A volume with no source is refused before any container is made.
func TestPodmanEngine_OpenVolume_NoSource(t *testing.T) {
	// Arrange: no expectation, so any call to the daemon fails the run.
	api := &mockAPI{}
	e := NewPodmanEngine(NewEngine(api), NewLibpod(fakeLibpod{}))

	// Act
	_, err := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume})

	// Assert
	require.ErrorContains(t, err, "no source")
	api.AssertExpectations(t)
}

// A layer is committed through libpod in the OCI format, its labels as LABEL changes
// and libpod's bare id spelled as a digest.
func TestPodmanEngine_CommitContainer(t *testing.T) {
	// Arrange
	var gotMethod, gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.Query()
		_, _ = io.WriteString(w, `{"Id":"`+strings.TrimPrefix(podmanImageID.String(), "sha256:")+`"}`)
	}))
	defer server.Close()
	e := NewPodmanEngine(NewEngine(&mockAPI{}), NewLibpod(fakeLibpod{
		client: server.Client(),
		host:   "tcp://" + strings.TrimPrefix(server.URL, "http://"),
	}))

	// Act
	got, err := e.CommitContainer(context.Background(), "c1", "a comment OCI cannot carry",
		map[string]string{"io.arcave.b": "2", "io.arcave.a": "one two"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, podmanImageID, got)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/v1.41/libpod/commit", gotPath)
	assert.Equal(t, url.Values{
		"container": {"c1"},
		"format":    {"oci"},
		"pause":     {"false"},
		"changes":   {`LABEL "io.arcave.a"="one two"`, `LABEL "io.arcave.b"="2"`},
	}, gotQuery)
}

// A commit libpod refuses carries libpod's message, cut at maxLibpodErrorBytes.
func TestPodmanEngine_CommitContainer_Refused(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{
			name:    "a short refusal, whole",
			body:    `{"cause":"no such container"}`,
			wantMsg: `commit container c1: libpod answered 500 Internal Server Error: {"cause":"no such container"}`,
		},
		{
			name:    "a long one, its first bytes",
			body:    strings.Repeat("x", maxLibpodErrorBytes+1024),
			wantMsg: "commit container c1: libpod answered 500 Internal Server Error: " + strings.Repeat("x", maxLibpodErrorBytes),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			e := NewPodmanEngine(NewEngine(&mockAPI{}), NewLibpod(fakeLibpod{
				client: server.Client(),
				host:   "tcp://" + strings.TrimPrefix(server.URL, "http://"),
			}))

			// Act
			_, err := e.CommitContainer(context.Background(), "c1", "", nil)

			// Assert
			require.EqualError(t, err, tt.wantMsg)
		})
	}
}
