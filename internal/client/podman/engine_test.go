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
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
)

// isVolumeExportSpec reports whether spec is the export of demo_pgdata, whole: a tar of
// the volume mounted read-only, unconfined, keeping no log, its output the caller's.
func isVolumeExportSpec(spec model.OneShotSpec) bool {
	hasOutput := spec.Stdout != nil && spec.Stderr != nil
	spec.Stdout, spec.Stderr = nil, nil
	return hasOutput && assert.ObjectsAreEqual(model.OneShotSpec{
		Image:       "busybox",
		Cmd:         []string{"tar", "-c", "-f", "-", "-C", "/arcave-data", "."},
		Binds:       []string{"demo_pgdata:/arcave-data:ro"},
		SecurityOpt: []string{"label=disable"},
		DiscardLog:  true,
	}, spec)
}

// A volume leaves through the export's own container, not the embedded engine's archive API.
func TestEngine_OpenVolume(t *testing.T) {
	// Arrange
	created := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			body, _ := io.ReadAll(r.Body)
			created <- string(body)
			http.Error(w, "refused", http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "/images/"):
			_, _ = io.WriteString(w, `{}`)
		default:
			http.Error(w, "refused", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	api, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")),
		client.WithHTTPClient(server.Client()),
		client.WithVersion("1.44"),
	)
	require.NoError(t, err)
	e := NewEngine(docker.NewEngine(api))

	// Act
	rc, openErr := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"})

	// Assert
	require.NoError(t, openErr)
	_, readErr := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.ErrorContains(t, readErr, "export volume demo_pgdata: ")
	require.Len(t, created, 1, "the export must ask for its own container")
	assert.Contains(t, <-created, `"Cmd":["tar","-c","-f","-","-C","/arcave-data","."]`)
}

// A volume comes out as the tar a container of arcave's own writes; a tar that fails fails the
// stream's last read with its stderr, cut at maxExportStderrBytes.
func TestEngine_OpenVolume_Stream(t *testing.T) {
	tests := []struct {
		name       string
		runErr     error
		stderr     string
		wantSuffix string
	}{
		{name: "the archive streams out"},
		{
			name:       "a tar that fails saying nothing",
			runErr:     &model.ContainerExitError{Image: "busybox", StatusCode: 1},
			wantSuffix: "export volume demo_pgdata: busybox container exited with status 1",
		},
		{
			name:       "a tar that fails",
			runErr:     &model.ContainerExitError{Image: "busybox", StatusCode: 1},
			stderr:     "tar: ./pgdata: Permission denied\n",
			wantSuffix: "(stderr: tar: ./pgdata: Permission denied)",
		},
		{
			name:       "a tar that fails at length, its first bytes",
			runErr:     &model.ContainerExitError{Image: "busybox", StatusCode: 1},
			stderr:     strings.Repeat("x", maxExportStderrBytes+1024),
			wantSuffix: "(stderr: " + strings.Repeat("x", maxExportStderrBytes) + ")",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &mockOneShotRunner{}
			runner.On("RunOneShot", mock.Anything, mock.MatchedBy(isVolumeExportSpec)).
				Run(func(args mock.Arguments) {
					spec := args.Get(1).(model.OneShotSpec)
					_, _ = io.WriteString(spec.Stdout, "tar-bytes")
					_, _ = io.WriteString(spec.Stderr, tt.stderr)
				}).Return(tt.runErr).Once()
			e := &Engine{runner: runner}
			rc, err := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"})
			require.NoError(t, err)

			// Act
			got, readErr := io.ReadAll(rc)
			closeErr := rc.Close()

			// Assert
			require.NoError(t, closeErr)
			assert.Equal(t, "tar-bytes", string(got))
			if tt.runErr != nil {
				var exitErr *model.ContainerExitError
				require.ErrorAs(t, readErr, &exitErr)
				assert.Equal(t, int64(1), exitErr.StatusCode)
				assert.ErrorContains(t, readErr, "export volume demo_pgdata: ")
				assert.True(t, strings.HasSuffix(readErr.Error(), tt.wantSuffix), "the error must end in tar's stderr, bounded")
			} else {
				assert.NoError(t, readErr)
			}
			runner.AssertExpectations(t)
		})
	}
}

// A stream closed before its end stops the export, and Close waits for its container to go.
func TestEngine_OpenVolume_ClosedEarly(t *testing.T) {
	// Arrange
	stopped := make(chan struct{})
	runner := &mockOneShotRunner{}
	runner.On("RunOneShot", mock.Anything, mock.MatchedBy(isVolumeExportSpec)).
		Run(func(args mock.Arguments) {
			ctx := args.Get(0).(context.Context)
			spec := args.Get(1).(model.OneShotSpec)
			_, _ = io.WriteString(spec.Stdout, strings.Repeat("x", 1<<20))
			<-ctx.Done()
			close(stopped)
		}).Return(context.Canceled).Once()
	e := &Engine{runner: runner}
	rc, err := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"})
	require.NoError(t, err)
	_, err = rc.Read(make([]byte, 16))
	require.NoError(t, err)

	// Act
	closed := make(chan error, 1)
	go func() { closed <- rc.Close() }()

	// Assert
	select {
	case closeErr := <-closed:
		require.NoError(t, closeErr)
	case <-time.After(30 * time.Second):
		t.Fatal("closing the stream must stop the export")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Close must wait for the export to go")
	}
	runner.AssertExpectations(t)
}

// A volume with no source is refused before any container is made.
func TestEngine_OpenVolume_NoSource(t *testing.T) {
	// Arrange: no expectation, so any run fails the test.
	runner := &mockOneShotRunner{}
	e := &Engine{runner: runner}

	// Act
	rc, err := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume})

	// Assert
	require.ErrorContains(t, err, "no source")
	assert.Nil(t, rc)
	runner.AssertExpectations(t)
}

func TestNewEngine(t *testing.T) {
	// Arrange
	engine := docker.NewEngine(nil)

	// Act
	e := NewEngine(engine)

	// Assert
	assert.Same(t, engine, e.Engine)
	assert.Same(t, engine, e.runner)
}
