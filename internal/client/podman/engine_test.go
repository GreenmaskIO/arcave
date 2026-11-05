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
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
)

const podmanImageID = model.ImageDigestID("sha256:" + bareImageID)

// A layer is committed through libpod in the OCI format, unpaused, its labels as LABEL
// changes in key order, and libpod's bare id spelled as a digest; the comment is dropped.
// A commit that fails names its container.
func TestEngine_CommitContainer(t *testing.T) {
	errRefused := errors.New(`libpod answered 500 Internal Server Error: {"cause":"no such container"}`)
	tests := []struct {
		name       string
		comment    string
		labels     map[string]string
		wantCommit libpodCommit
		committed  string
		libpodErr  error
		want       model.ImageDigestID
		wantMsg    string
		wantIs     error
	}{
		{
			name:    "labels in key order, the comment dropped",
			comment: "a comment OCI cannot carry",
			labels:  map[string]string{"io.arcave.b": "2", "io.arcave.a": "one two"},
			wantCommit: libpodCommit{
				Container: "c1",
				Format:    "oci",
				Pause:     false,
				Changes:   []string{`LABEL "io.arcave.a"="one two"`, `LABEL "io.arcave.b"="2"`},
			},
			committed: bareImageID,
			want:      podmanImageID,
		},
		{
			name: "libpod refuses",
			wantCommit: libpodCommit{
				Container: "c1",
				Format:    "oci",
				Pause:     false,
				Changes:   []string{},
			},
			libpodErr: errRefused,
			wantMsg:   `commit container c1: libpod answered 500 Internal Server Error: {"cause":"no such container"}`,
			wantIs:    errRefused,
		},
		{
			name: "an id that is no digest",
			wantCommit: libpodCommit{
				Container: "c1",
				Format:    "oci",
				Pause:     false,
				Changes:   []string{},
			},
			committed: "not-an-id",
			wantMsg:   "commit container c1: ",
			wantIs:    model.ErrInvalidDigestID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			libpod := &mockLibpod{}
			libpod.On("commitContainer", mock.Anything, tt.wantCommit).Return(tt.committed, tt.libpodErr).Once()
			e := &Engine{libpod: libpod}

			// Act
			got, err := e.CommitContainer(context.Background(), "c1", tt.comment, tt.labels)

			// Assert
			if tt.wantIs != nil {
				require.ErrorContains(t, err, tt.wantMsg)
				require.ErrorIs(t, err, tt.wantIs)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			libpod.AssertExpectations(t)
		})
	}
}

// An image is saved through libpod as an OCI archive, the stream handed on as it is; a
// save libpod refuses names the image and carries libpod's own message.
func TestEngine_SaveImage(t *testing.T) {
	tests := []struct {
		name      string
		libpodErr error
		wantMsg   string
	}{
		{name: "the archive streams back"},
		{
			name:      "libpod refuses",
			libpodErr: errors.New(`libpod answered 404 Not Found: {"cause":"image not known"}`),
			wantMsg:   "save image " + podmanImageID.String() + `: libpod answered 404 Not Found: {"cause":"image not known"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			libpod := &mockLibpod{}
			exported := libpod.On("exportImage", mock.Anything, podmanImageID.String(), "oci-archive").Once()
			if tt.libpodErr != nil {
				exported.Return(nil, tt.libpodErr)
			} else {
				exported.Return(io.NopCloser(strings.NewReader("oci-archive-bytes")), nil)
			}
			e := &Engine{libpod: libpod}

			// Act
			archive, err := e.SaveImage(context.Background(), podmanImageID)

			// Assert
			libpod.AssertExpectations(t)
			if tt.libpodErr != nil {
				require.EqualError(t, err, tt.wantMsg)
				require.ErrorIs(t, err, tt.libpodErr)
				assert.Nil(t, archive)
				return
			}
			require.NoError(t, err)
			defer archive.Close()
			got, err := io.ReadAll(archive)
			require.NoError(t, err)
			assert.Equal(t, "oci-archive-bytes", string(got))
		})
	}
}

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

// A volume comes out as the tar a container of arcave's own writes, since Podman's archive
// API hands every file to root; a tar that fails fails the stream's last read with its
// stderr, cut at maxExportStderrBytes.
func TestEngine_OpenVolume(t *testing.T) {
	tests := []struct {
		name       string
		runErr     error
		stderr     string
		wantSuffix string
	}{
		{name: "the archive streams out"},
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

			// Act
			rc, openErr := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"})
			require.NoError(t, openErr)
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

	// Act
	rc, openErr := e.OpenVolume(context.Background(), model.MountSource{Type: model.MountSourceVolume, Source: "demo_pgdata"})
	require.NoError(t, openErr)
	_, readErr := rc.Read(make([]byte, 16))
	closed := make(chan error, 1)
	go func() { closed <- rc.Close() }()

	// Assert
	require.NoError(t, readErr)
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

// NewEngine extends the engine it is given and runs the volume export through it.
func TestNewEngine(t *testing.T) {
	// Arrange
	engine := docker.NewEngine(nil)
	libpod := &mockLibpod{}

	// Act
	e := NewEngine(engine, libpod)

	// Assert
	assert.Same(t, engine, e.Engine)
	assert.Same(t, engine, e.runner)
	assert.Same(t, libpod, e.libpod)
}
