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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/model"
)

// bareImageID is an image id as libpod answers a commit with: no algorithm.
const bareImageID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const podmanImageID = model.ImageDigestID("sha256:" + bareImageID)

// newRecordedDaemon is a client of a daemon answering for c1 as a real Podman did, on compat and libpod.
func newRecordedDaemon(t *testing.T) *client.Client {
	t.Helper()
	compat, err := os.ReadFile("testdata/compat_inspect.json")
	require.NoError(t, err)
	stored, err := os.ReadFile("libpod/testdata/container_inspect.json")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/libpod/containers/c1/json"):
			_, _ = w.Write(stored)
		case strings.HasSuffix(r.URL.Path, "/containers/c1/json"):
			_, _ = w.Write(compat)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	api, err := client.New(
		client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")),
		client.WithHTTPClient(server.Client()),
		client.WithAPIVersion("1.44"),
	)
	require.NoError(t, err)
	return api
}

func newInspectOf(hostConfig *container.HostConfig) container.InspectResponse {
	return container.InspectResponse{HostConfig: hostConfig}
}

// What a real Podman answered, read off the wire, comes out as the mount requests and log size.
func TestAPI_ContainerInspect_RecordedDaemon(t *testing.T) {
	// Arrange
	daemon := newRecordedDaemon(t)
	libpodClient, err := libpod.NewClient(daemon, libpod.Options{Version: "6.1.4"})
	require.NoError(t, err)
	api := NewAPI(daemon, libpodClient)

	// Act
	inspected, err := api.ContainerInspect(context.Background(), "c1", client.ContainerInspectOptions{})

	// Assert
	require.NoError(t, err)
	got := inspected.Container
	require.NotNil(t, got.HostConfig)
	assert.Equal(t, []string{
		"/tmp:/short:rw,rprivate,nosuid,nodev,rbind",
		"arcave-fixture:/data:rw,rprivate,nosuid,nodev,rbind",
	}, got.HostConfig.Binds)
	assert.Equal(t, []mount.Mount{{
		Type:          mount.TypeVolume,
		Source:        "arcave-fixture",
		Target:        "/sub",
		ReadOnly:      true,
		VolumeOptions: &mount.VolumeOptions{Subpath: "sub"},
	}, {
		Type:        mount.TypeBind,
		Source:      "/srv",
		Target:      "/long",
		ReadOnly:    true,
		BindOptions: &mount.BindOptions{Propagation: mount.PropagationRShared},
	}}, got.HostConfig.Mounts)
	assert.Equal(t, container.LogConfig{
		Type:   "json-file",
		Config: map[string]string{"max-size": "1MB"},
	}, got.HostConfig.LogConfig)
}

// The compat inspect is asked first and libpod's answer goes into it; a compat refusal leaves libpod unasked.
func TestAPI_ContainerInspect(t *testing.T) {
	refused := errors.New(`libpod answered 404 Not Found: {"cause":"no such container"}`)
	tests := []struct {
		name      string
		compatErr error
		libpodErr error
		want      container.InspectResponse
		wantErr   string
		wantIs    error
	}{
		{
			name: "both answer",
			want: newInspectOf(&container.HostConfig{
				Binds:     []string{"pgdata:/data"},
				LogConfig: container.LogConfig{Config: map[string]string{"max-size": "1MB"}},
			}),
		},
		{
			name:      "compat refuses",
			compatErr: cerrdefs.ErrNotFound,
			wantErr:   cerrdefs.ErrNotFound.Error(),
			wantIs:    cerrdefs.ErrNotFound,
		},
		{
			name:      "libpod refuses",
			libpodErr: refused,
			wantErr:   `inspect container c1 through libpod: libpod answered 404 Not Found: {"cause":"no such container"}`,
			wantIs:    refused,
		},
		{
			name:      "libpod no longer has it, which still reads as not found",
			libpodErr: cerrdefs.ErrNotFound,
			wantErr:   "inspect container c1 through libpod: " + cerrdefs.ErrNotFound.Error(),
			wantIs:    cerrdefs.ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			compat := &mockCompatAPI{}
			stored := &mockLibpod{}
			inspected := compat.On("ContainerInspect", mock.Anything, "c1", client.ContainerInspectOptions{}).
				Return(client.ContainerInspectResult{
					Container: newInspectOf(&container.HostConfig{Binds: []string{"pgdata:/data"}}),
				}, tt.compatErr).Once()
			if tt.compatErr == nil {
				mock.InOrder(
					inspected,
					stored.On("InspectContainer", mock.Anything, "c1").
						Return(libpod.Container{
							HostConfig: libpod.HostConfig{LogConfig: libpod.LogConfig{Size: "1MB"}},
						}, tt.libpodErr).Once(),
				)
			}
			api := NewAPI(compat, stored)

			// Act
			got, err := api.ContainerInspect(context.Background(), "c1", client.ContainerInspectOptions{})

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.ErrorIs(t, err, tt.wantIs)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got.Container)
			compat.AssertExpectations(t)
			stored.AssertExpectations(t)
		})
	}
}

// A commit reaches libpod as OCI, its comment dropped; a reference, author or config is refused with libpod unasked.
func TestAPI_ContainerCommit(t *testing.T) {
	errRefused := errors.New(`libpod answered 500 Internal Server Error: {"cause":"no such container"}`)
	tests := []struct {
		name       string
		options    client.ContainerCommitOptions
		wantCommit *libpod.CommitOptions
		libpodErr  error
		want       client.ContainerCommitResult
		wantErr    string
	}{
		{
			name: "the changes and pause go through, the comment is dropped",
			options: client.ContainerCommitOptions{
				Comment: "a comment OCI cannot carry",
				Changes: []string{`LABEL "io.arcave.a"="one two"`},
			},
			wantCommit: &libpod.CommitOptions{
				Container: "c1",
				Format:    "oci",
				Pause:     true,
				Changes:   []string{`LABEL "io.arcave.a"="one two"`},
			},
			want: client.ContainerCommitResult{ID: bareImageID},
		},
		{
			name:    "libpod refuses",
			options: client.ContainerCommitOptions{NoPause: true},
			wantCommit: &libpod.CommitOptions{
				Container: "c1",
				Format:    "oci",
			},
			libpodErr: errRefused,
			wantErr:   errRefused.Error(),
		},
		{
			name:    "a reference is refused",
			options: client.ContainerCommitOptions{Reference: "repo:tag"},
			wantErr: "a commit through libpod takes no reference, author or config",
		},
		{
			name:    "an author is refused",
			options: client.ContainerCommitOptions{Author: "someone"},
			wantErr: "a commit through libpod takes no reference, author or config",
		},
		{
			name:    "a config is refused",
			options: client.ContainerCommitOptions{Config: &container.Config{}},
			wantErr: "a commit through libpod takes no reference, author or config",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			stored := &mockLibpod{}
			if tt.wantCommit != nil {
				stored.On("CommitContainer", mock.Anything, *tt.wantCommit).Return(tt.want.ID, tt.libpodErr).Once()
			}
			api := NewAPI(&mockCompatAPI{}, stored)

			// Act
			got, err := api.ContainerCommit(context.Background(), "c1", tt.options)

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.libpodErr != nil {
				require.ErrorIs(t, err, tt.libpodErr)
			}
			assert.Equal(t, tt.want, got)
			stored.AssertExpectations(t)
		})
	}
}

// One image is saved through libpod as an OCI archive; any other count, or an option, is refused with libpod unasked.
func TestAPI_ImageSave(t *testing.T) {
	errRefused := errors.New(`libpod answered 404 Not Found: {"cause":"image not known"}`)
	tests := []struct {
		name       string
		imageIDs   []string
		opts       []client.ImageSaveOption
		wantExport bool
		libpodErr  error
		wantErr    string
	}{
		{name: "one image", imageIDs: []string{"i1"}, wantExport: true},
		{
			name:       "libpod refuses",
			imageIDs:   []string{"i1"},
			wantExport: true,
			libpodErr:  errRefused,
			wantErr:    errRefused.Error(),
		},
		{
			name:    "no image",
			wantErr: errUnsupportedSaveOptions.Error(),
		},
		{
			name:     "two images",
			imageIDs: []string{"i1", "i2"},
			wantErr:  errUnsupportedSaveOptions.Error(),
		},
		{
			name:     "an option",
			imageIDs: []string{"i1"},
			opts:     []client.ImageSaveOption{client.ImageSaveWithPlatforms()},
			wantErr:  errUnsupportedSaveOptions.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			stored := &mockLibpod{}
			if tt.wantExport {
				exported := stored.On("ExportImage", mock.Anything, "i1", "oci-archive").Once()
				if tt.libpodErr != nil {
					exported.Return(nil, tt.libpodErr)
				} else {
					exported.Return(io.NopCloser(strings.NewReader("oci-archive-bytes")), nil)
				}
			}
			api := NewAPI(&mockCompatAPI{}, stored)

			// Act
			archive, err := api.ImageSave(context.Background(), tt.imageIDs, tt.opts...)

			// Assert
			stored.AssertExpectations(t)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				if tt.libpodErr != nil {
					require.ErrorIs(t, err, tt.libpodErr)
				}
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

// docker.Engine over this API commits through libpod: labels as changes in key order, unpaused, the bare id a digest.
func TestAPI_ContainerCommit_ThroughEngine(t *testing.T) {
	// Arrange
	stored := &mockLibpod{}
	stored.On("CommitContainer", mock.Anything, libpod.CommitOptions{
		Container: "c1",
		Format:    "oci",
		Pause:     false,
		Changes:   []string{`LABEL "io.arcave.a"="one two"`, `LABEL "io.arcave.b"="2"`},
	}).Return(bareImageID, nil).Once()
	engine := docker.NewEngine(NewAPI(&mockCompatAPI{}, stored))

	// Act
	got, err := engine.CommitContainer(context.Background(), "c1", "a comment OCI cannot carry",
		map[string]string{"io.arcave.b": "2", "io.arcave.a": "one two"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, podmanImageID, got)
	stored.AssertExpectations(t)
}

// docker.Engine over this API saves through libpod, so it must ask with no option.
func TestAPI_ImageSave_ThroughEngine(t *testing.T) {
	// Arrange
	stored := &mockLibpod{}
	stored.On("ExportImage", mock.Anything, podmanImageID.String(), "oci-archive").
		Return(io.NopCloser(strings.NewReader("oci-archive-bytes")), nil).Once()
	engine := docker.NewEngine(NewAPI(&mockCompatAPI{}, stored))

	// Act
	archive, err := engine.SaveImage(context.Background(), podmanImageID)

	// Assert
	require.NoError(t, err)
	defer archive.Close()
	got, err := io.ReadAll(archive)
	require.NoError(t, err)
	assert.Equal(t, "oci-archive-bytes", string(got))
	stored.AssertExpectations(t)
}

// docker.Engine over this API rebuilds a replica from libpod's account: its subpath and mount-API
// bind as mount requests, out of Binds, and its log size.
func TestAPI_ContainerInspect_ThroughEngine(t *testing.T) {
	// Arrange
	const imageRef = "arcave/demo-app-2:sp-019e843e-1650-7000-8000-000000000000"
	compat := &mockCompatAPI{}
	stored := &mockLibpod{}
	mock.InOrder(
		compat.On("ContainerInspect", mock.Anything, "c1", client.ContainerInspectOptions{}).
			Return(client.ContainerInspectResult{Container: container.InspectResponse{
				ID:   "c1",
				Name: "/demo-app-2",
				HostConfig: &container.HostConfig{
					Binds: []string{"pgdata:/data:rw", "pgdata:/sub:ro", "/srv:/long:ro,rshared,bind"},
				},
				Config: &container.Config{},
			}}, nil).Once(),
		stored.On("InspectContainer", mock.Anything, "c1").Return(libpod.Container{
			Mounts: []libpod.Mount{
				{Type: "volume", Name: "pgdata", Destination: "/data", RW: true},
				{Type: "volume", Name: "pgdata", Destination: "/sub", SubPath: "sub"},
				{
					Type:        "bind",
					Source:      "/srv",
					Destination: "/long",
					Options:     []string{"bind"},
					Propagation: "rshared",
				},
			},
			HostConfig: libpod.HostConfig{LogConfig: libpod.LogConfig{Size: "1MB"}},
		}, nil).Once(),
		compat.On("ContainerRemove", mock.Anything, "c1", client.ContainerRemoveOptions{RemoveVolumes: true}).
			Return(nil).Once(),
		compat.On("ContainerCreate", mock.Anything, client.ContainerCreateOptions{
			Config: &container.Config{
				Image:  imageRef,
				Labels: map[string]string{"com.docker.compose.container-number": "2"},
			},
			HostConfig: &container.HostConfig{
				Binds: []string{"pgdata:/data:rw"},
				Mounts: []mount.Mount{{
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
				LogConfig: container.LogConfig{Config: map[string]string{"max-size": "1MB"}},
			},
			NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}},
			Name:             "demo-app-2",
		}).Return(client.ContainerCreateResult{ID: "new"}, nil).Once(),
	)
	engine := docker.NewEngine(NewAPI(compat, stored))

	// Act
	got, err := engine.RecreateWithImage(context.Background(), "c1", model.ReplicaIdentity{
		Name:   "demo-app-2",
		Number: 2,
	}, imageRef)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "new", got)
	compat.AssertExpectations(t)
	stored.AssertExpectations(t)
}

// A volume at a subpath and a bind of the mount API come back as mount requests after any the
// inspect carries, out of Binds, and the log's size as its max-size; every other mount stays in Binds.
func TestWithLibpodRequest(t *testing.T) {
	declared := mount.Mount{Type: mount.TypeTmpfs, Target: "/run"}
	tests := []struct {
		name    string
		inspect container.InspectResponse
		stored  libpod.Container
		want    container.InspectResponse
	}{
		{
			name: "a subpath, a bind of the mount API and a log size",
			inspect: newInspectOf(&container.HostConfig{
				Binds: []string{
					"/srv:/long:ro,rshared,bind",
					"/tmp:/short:rw,rprivate,rbind",
					"pgdata:/data:rw",
					"pgdata:/sub:ro",
				},
				Mounts: []mount.Mount{declared},
			}),
			stored: libpod.Container{
				Mounts: []libpod.Mount{
					{Type: "volume", Name: "pgdata", Destination: "/data", RW: true},
					{Type: "volume", Name: "pgdata", Destination: "/sub", SubPath: "sub"},
					{
						Type:        "bind",
						Source:      "/tmp",
						Destination: "/short",
						Options:     []string{"nosuid", "nodev", "rbind"},
						RW:          true,
						Propagation: "rprivate",
					},
					{
						Type:        "bind",
						Source:      "/srv",
						Destination: "/long",
						Options:     []string{"bind", "nosuid"},
						Propagation: "rshared",
					},
				},
				HostConfig: libpod.HostConfig{LogConfig: libpod.LogConfig{Size: "1MB"}},
			},
			want: newInspectOf(&container.HostConfig{
				Binds: []string{"/tmp:/short:rw,rprivate,rbind", "pgdata:/data:rw"},
				Mounts: []mount.Mount{declared, {
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
				LogConfig: container.LogConfig{Config: map[string]string{"max-size": "1MB"}},
			}),
		},
		{
			name:    "a bind naming no target stays, nothing covering it",
			inspect: newInspectOf(&container.HostConfig{Binds: []string{"/sub", "pgdata:/sub"}}),
			stored: libpod.Container{
				Mounts: []libpod.Mount{{Type: "volume", Name: "pgdata", Destination: "/sub", SubPath: "sub", RW: true}},
			},
			want: newInspectOf(&container.HostConfig{
				Binds: []string{"/sub"},
				Mounts: []mount.Mount{{
					Type:          mount.TypeVolume,
					Source:        "pgdata",
					Target:        "/sub",
					VolumeOptions: &mount.VolumeOptions{Subpath: "sub"},
				}},
			}),
		},
		{
			name:    "read-write mounts stay read-write",
			inspect: newInspectOf(&container.HostConfig{}),
			stored: libpod.Container{
				Mounts: []libpod.Mount{
					{Type: "volume", Name: "pgdata", Destination: "/sub", SubPath: "sub", RW: true},
					{Type: "bind", Source: "/srv", Destination: "/long", Options: []string{"bind"}, RW: true},
				},
			},
			want: newInspectOf(&container.HostConfig{
				Mounts: []mount.Mount{{
					Type:          mount.TypeVolume,
					Source:        "pgdata",
					Target:        "/sub",
					VolumeOptions: &mount.VolumeOptions{Subpath: "sub"},
				}, {
					Type:        mount.TypeBind,
					Source:      "/srv",
					Target:      "/long",
					BindOptions: &mount.BindOptions{},
				}},
			}),
		},
		{
			name:    "neither: libpod reports no size as a negative one",
			inspect: newInspectOf(&container.HostConfig{}),
			stored: libpod.Container{
				Mounts:     []libpod.Mount{{Type: "volume", Name: "pgdata", Destination: "/data", RW: true}},
				HostConfig: libpod.HostConfig{LogConfig: libpod.LogConfig{Size: "-1B"}},
			},
			want: newInspectOf(&container.HostConfig{}),
		},
		{
			name:    "no host config",
			inspect: newInspectOf(nil),
			stored:  libpod.Container{HostConfig: libpod.HostConfig{LogConfig: libpod.LogConfig{Size: "1MB"}}},
			want:    newInspectOf(nil),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := withLibpodRequest(tt.inspect, tt.stored)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// The inspect handed in keeps its binds and options, and its mounts' spare capacity is not written into.
func TestWithLibpodRequest_LeavesItsInput(t *testing.T) {
	// Arrange
	mounts := make([]mount.Mount, 1, 4)
	inspect := newInspectOf(&container.HostConfig{
		Binds:     []string{"pgdata:/sub", "pgdata:/data"},
		Mounts:    mounts,
		LogConfig: container.LogConfig{Config: map[string]string{"tag": "x"}},
	})
	stored := libpod.Container{
		Mounts:     []libpod.Mount{{Type: "volume", Name: "pgdata", Destination: "/sub", SubPath: "sub"}},
		HostConfig: libpod.HostConfig{LogConfig: libpod.LogConfig{Size: "1MB"}},
	}

	// Act
	got := withLibpodRequest(inspect, stored)

	// Assert
	assert.Len(t, got.HostConfig.Mounts, 2)
	assert.Equal(t, mount.Mount{}, mounts[:2][1], "the input's backing array must stay untouched")
	assert.Equal(t, []string{"pgdata:/data"}, got.HostConfig.Binds)
	assert.Equal(t, newInspectOf(&container.HostConfig{
		Binds:     []string{"pgdata:/sub", "pgdata:/data"},
		Mounts:    mounts,
		LogConfig: container.LogConfig{Config: map[string]string{"tag": "x"}},
	}), inspect)
}

// libpod's size becomes max-size beside any other option, unless it is none or unreadable.
func TestWithLogSize(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]string
		size    string
		want    map[string]string
	}{
		{name: "a size", size: "1MB", want: map[string]string{"max-size": "1MB"}},
		{name: "beside other options", options: map[string]string{"tag": "x"}, size: "1MB", want: map[string]string{"tag": "x", "max-size": "1MB"}},
		{name: "over a max-size compat carried", options: map[string]string{"max-size": "5MB"}, size: "1MB", want: map[string]string{"max-size": "1MB"}},
		{name: "zero", size: "0B"},
		{name: "none, reported as -1B, which no size parses as", size: "-1B"},
		{name: "unreadable", size: "lots"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := withLogSize(container.LogConfig{Type: "json-file", Config: tt.options}, tt.size)

			// Assert
			assert.Equal(t, container.LogConfig{Type: "json-file", Config: tt.want}, got)
		})
	}
}
