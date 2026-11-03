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
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// archivedEntry is what a test reads back of one tar entry.
type archivedEntry struct {
	name     string
	typeflag byte
	mode     int64
	uid, gid int
	content  string
}

// readArchive reads every entry of a tar.
func readArchive(t *testing.T, r io.Reader) []archivedEntry {
	t.Helper()

	var entries []archivedEntry
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		require.NoError(t, err)
		content, err := io.ReadAll(tr)
		require.NoError(t, err)
		entries = append(entries, archivedEntry{
			name:     header.Name,
			typeflag: header.Typeflag,
			mode:     header.Mode,
			uid:      header.Uid,
			gid:      header.Gid,
			content:  string(content),
		})
	}
}

func TestNewOneShotArchive(t *testing.T) {
	tests := []struct {
		name    string
		files   []model.OneShotFile
		want    []archivedEntry
		wantErr string
	}{
		{
			name: "a directory and a file in it, in order, owned by root",
			files: []model.OneShotFile{
				{Path: "/run/arcave", Mode: fs.ModeDir | fs.ModeSticky | 0o777},
				{Path: "/run/arcave/stdio.sh", Mode: 0o755, Content: []byte("#!/bin/sh\n")},
			},
			want: []archivedEntry{
				{name: "run/arcave/", typeflag: tar.TypeDir, mode: 0o1777},
				{name: "run/arcave/stdio.sh", typeflag: tar.TypeReg, mode: 0o755, content: "#!/bin/sh\n"},
			},
		},
		{
			name: "a plain directory, kept after the file listed first",
			files: []model.OneShotFile{
				{Path: "/etc/arcave.conf", Mode: 0o644, Content: []byte("x")},
				{Path: "/opt/arcave", Mode: fs.ModeDir | 0o755},
			},
			want: []archivedEntry{
				{name: "etc/arcave.conf", typeflag: tar.TypeReg, mode: 0o644, content: "x"},
				{name: "opt/arcave/", typeflag: tar.TypeDir, mode: 0o755},
			},
		},
		{name: "a symlink", files: []model.OneShotFile{{Path: "/run/x", Mode: fs.ModeSymlink | 0o777}}, wantErr: "only a directory"},
		{name: "a setuid file", files: []model.OneShotFile{{Path: "/run/x", Mode: fs.ModeSetuid | 0o755}}, wantErr: "only a directory"},
		{name: "a relative path", files: []model.OneShotFile{{Path: "run/x"}}, wantErr: "not an absolute"},
		{name: "an unclean path", files: []model.OneShotFile{{Path: "/run/../etc/x"}}, wantErr: "not an absolute"},
		{name: "the root itself", files: []model.OneShotFile{{Path: "/", Mode: fs.ModeDir}}, wantErr: "not an absolute"},
		{
			name:    "a directory with content",
			files:   []model.OneShotFile{{Path: "/run/arcave", Mode: fs.ModeDir, Content: []byte("x")}},
			wantErr: "a directory with content",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			archive, err := newOneShotArchive(tt.files)

			// Assert
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, readArchive(t, archive))
		})
	}
}

// Files are copied in after the create and before the start, so the entrypoint may be one of them.
func TestRunOneShotContainer_Files(t *testing.T) {
	// Arrange
	files := []model.OneShotFile{{Path: "/run/arcave/stdio.sh", Mode: 0o755, Content: []byte("script")}}
	api := &mockAPI{}
	copyCall, copied := expectCopy(t, api, nil)
	mock.InOrder(
		api.On("ImageInspect", mock.Anything, testImage).Return(image.InspectResponse{}, nil).Once(),
		expectOneShotCreate(api),
		copyCall,
		api.On("ContainerStart", mock.Anything, oneShotID, container.StartOptions{}).Return(nil).Once(),
		api.On("ContainerWait", mock.Anything, oneShotID, container.WaitConditionNotRunning).
			Return(newWaitStatus(0), (<-chan error)(make(chan error))).Once(),
		api.On("ContainerRemove", mock.Anything, oneShotID, container.RemoveOptions{Force: true}).Return(nil).Once(),
	)

	// Act
	err := runWithin(t, context.Background(), api, model.OneShotSpec{Image: testImage, Files: files})

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
	assert.Equal(t, []archivedEntry{
		{name: "run/arcave/stdio.sh", typeflag: tar.TypeReg, mode: 0o755, content: "script"},
	}, *copied)
}

// A copy the daemon refuses fails the run before the start, and the container still goes.
func TestRunOneShotContainer_FilesRefused(t *testing.T) {
	// Arrange
	refused := errors.New("read-only file system")
	api := &mockAPI{}
	copyCall, _ := expectCopy(t, api, refused)
	mock.InOrder(
		api.On("ImageInspect", mock.Anything, testImage).Return(image.InspectResponse{}, nil).Once(),
		expectOneShotCreate(api),
		copyCall,
		api.On("ContainerRemove", mock.Anything, oneShotID, container.RemoveOptions{Force: true}).Return(nil).Once(),
	)
	spec := model.OneShotSpec{
		Image: testImage,
		Files: []model.OneShotFile{{Path: "/run/arcave/stdio.sh", Mode: 0o755}},
	}

	// Act
	err := runWithin(t, context.Background(), api, spec)

	// Assert
	require.ErrorIs(t, err, refused)
	api.AssertExpectations(t)
}

// expectOneShotCreate expects the one create of a one-shot container with no stdio.
func expectOneShotCreate(api *mockAPI) *mock.Call {
	return api.On("ContainerCreate", mock.Anything, mock.MatchedBy(func(cfg *container.Config) bool {
		return cfg.Image == testImage
	}), &container.HostConfig{}, (*network.NetworkingConfig)(nil), (*ocispec.Platform)(nil), "").
		Return(container.CreateResponse{ID: oneShotID}, nil).Once()
}

// expectCopy expects one copy into the one-shot container, answered with err, and
// returns where the archive it was handed is read back to.
func expectCopy(t *testing.T, api *mockAPI, err error) (*mock.Call, *[]archivedEntry) {
	t.Helper()

	var copied []archivedEntry
	call := api.On("CopyToContainer", mock.Anything, oneShotID, "/", mock.AnythingOfType("*bytes.Buffer"),
		container.CopyToContainerOptions{}).
		Run(func(args mock.Arguments) { copied = readArchive(t, args.Get(3).(*bytes.Buffer)) }).
		Return(err).Once()
	return call, &copied
}
