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

package introspect

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	stack     = model.StackName("demo")
	anonName  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ownedName = "demo_pgdata"
)

func TestInspector_Introspect(t *testing.T) {
	// Arrange
	daemon := &mockDockerAPI{}
	daemon.expectContainers(
		summary("c-db-1", "db", "postgres:16", 1,
			mountPoint(mount.TypeVolume, ownedName, "", "/var/lib/postgresql/data", true)),
		summary("c-app-1", "app", "busybox", 1,
			mountPoint(mount.TypeVolume, ownedName, "", "/mnt/data", false),
			mountPoint(mount.TypeBind, "", "/host/cfg", "/etc/cfg", true)),
	)
	daemon.expectInspect("c-db-1", "c-app-1")
	daemon.expectVolumes(ownedVolume(ownedName, "pgdata"), ownedVolume("demo_orphan", "orphan"))
	daemon.expectNetworks(network.Summary{Name: "demo_back", Labels: map[string]string{api.NetworkLabel: "back"}})
	inspector := NewInspector(daemon, stack)

	// Act
	got, err := inspector.Introspect(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "demo", got.Project.Name)

	require.Len(t, got.Project.Services, 2)
	assert.Equal(t, "app", got.Project.Services[0].Name, "services sort by name")
	assert.Equal(t, "db", got.Project.Services[1].Name)
	assert.Equal(t, []model.ServiceVolume{
		{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg",
			ReadOnly: true, Bind: &model.BindMount{}},
		{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/mnt/data",
			VolumeName: ownedName},
	}, got.Project.Services[0].Volumes, "mounts sort by target")

	require.Len(t, got.Project.Volumes, 2)
	assert.Equal(t, "orphan", got.Project.Volumes[0].Key)
	assert.Empty(t, got.Project.Volumes[0].UsedBy)
	assert.Equal(t, []model.ServiceReference{
		{Service: "app", Target: "/mnt/data"},
		{Service: "db", Target: "/var/lib/postgresql/data", ReadOnly: true},
	}, got.Project.Volumes[1].UsedBy, "users sort by (service, target)")

	assert.Equal(t, []model.Network{{Key: "back", Name: "demo_back",
		Inspect: network.Inspect{Name: "demo_back", Labels: map[string]string{api.NetworkLabel: "back"}}}},
		got.Project.Networks)
	daemon.AssertExpectations(t)
}

func TestInspector_Introspect_JoinsTheTwoVolumeLevels(t *testing.T) {
	// Arrange
	daemon := &mockDockerAPI{}
	daemon.expectContainers(summary("c-db-1", "db", "postgres:16", 1,
		mountPoint(mount.TypeVolume, ownedName, "", "/data", false)))
	daemon.expectInspect("c-db-1")
	daemon.expectVolumes(ownedVolume(ownedName, "pgdata"))
	daemon.expectNetworks()

	// Act
	got, err := NewInspector(daemon, stack).Introspect(context.Background())

	// Assert
	require.NoError(t, err)
	require.Len(t, got.Project.Volumes, 1)
	require.Len(t, got.Project.Services[0].Volumes, 1)
	assert.Equal(t,
		got.Project.Volumes[0].ObjectID(),
		got.Project.Services[0].Volumes[0].ObjectID("db"))
	daemon.AssertExpectations(t)
}

func TestInspector_Introspect_ClassifiesMounts(t *testing.T) {
	tests := []struct {
		name   string
		mount  container.MountPoint
		labels map[string]string // what VolumeInspect reports for an unowned volume
		want   model.ServiceVolume
	}{
		{
			name:  "a volume the project owns is named, keyed by its compose key",
			mount: mountPoint(mount.TypeVolume, ownedName, "", "/data", false),
			want: model.ServiceVolume{Type: model.VolumeTypeNamed, RawType: "volume",
				Source: "pgdata", Target: "/data", VolumeName: ownedName},
		},
		{
			name:   "the daemon's own anonymous label wins",
			mount:  mountPoint(mount.TypeVolume, "short-name", "", "/data", false),
			labels: map[string]string{anonymousVolumeLabel: "true"},
			// The generated name is carried: a reaper has no other way to name the volume.
			want: model.ServiceVolume{Type: model.VolumeTypeAnon, RawType: "volume",
				Target: "/data", VolumeName: "short-name"},
		},
		{
			name:  "a generated name is anonymous where the label is missing",
			mount: mountPoint(mount.TypeVolume, anonName, "", "/data", false),
			want: model.ServiceVolume{Type: model.VolumeTypeAnon, RawType: "volume",
				Target: "/data", VolumeName: anonName},
		},
		{
			name:  "an unowned, unlabelled volume is an external named one",
			mount: mountPoint(mount.TypeVolume, "shared", "", "/data", false),
			want: model.ServiceVolume{Type: model.VolumeTypeNamed, RawType: "volume",
				Source: "shared", Target: "shared-placeholder", VolumeName: "shared"},
		},
		{
			name:  "a bind carries the host path and its propagation",
			mount: mountPoint(mount.TypeBind, "", "/host/cfg", "/etc/cfg", false),
			want: model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind",
				Source: "/host/cfg", Target: "/etc/cfg", Bind: &model.BindMount{}},
		},
		{
			name:  "tmpfs",
			mount: mountPoint(mount.TypeTmpfs, "", "", "/run", false),
			want:  model.ServiceVolume{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/run"},
		},
		{
			name:  "npipe",
			mount: mountPoint(mount.TypeNamedPipe, "", `\\.\pipe\d`, "/pipe", false),
			want: model.ServiceVolume{Type: model.VolumeTypeNPipe, RawType: "npipe",
				Target: "/pipe"},
		},
		{
			name:  "an unrecognised type is reported, diagnosed by RawType",
			mount: mountPoint("newfangled", "", "", "/x", false),
			want:  model.ServiceVolume{Type: model.VolumeTypeUnknown, RawType: "newfangled", Target: "/x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			want := tt.want
			if want.Target == "shared-placeholder" {
				want.Target = "/data"
			}
			daemon := &mockDockerAPI{}
			daemon.expectContainers(summary("c-db-1", "db", "busybox", 1, tt.mount))
			daemon.expectInspect("c-db-1")
			daemon.expectVolumes(ownedVolume(ownedName, "pgdata"))
			daemon.expectNetworks()
			if tt.mount.Type == mount.TypeVolume && tt.mount.Name != ownedName {
				daemon.On("VolumeInspect", mock.Anything, tt.mount.Name).
					Return(volume.Volume{Name: tt.mount.Name, Labels: tt.labels}, nil).Once()
			}

			// Act
			got, err := NewInspector(daemon, stack).Introspect(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []model.ServiceVolume{want}, got.Project.Services[0].Volumes)
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_GroupsReplicas(t *testing.T) {
	// Arrange
	second := summary("c-web-2", "web", "nginx:1", 2)
	first := summary("c-web-1", "web", "nginx:1", 1)
	oneoff := summary("c-web-run", "web", "nginx:1", 1)
	oneoff.Labels[api.OneoffLabel] = "True"

	daemon := &mockDockerAPI{}
	daemon.expectContainers(second, oneoff, first)
	daemon.expectInspect("c-web-1", "c-web-2")
	daemon.expectVolumes()
	daemon.expectNetworks()

	// Act
	got, err := NewInspector(daemon, stack).Introspect(context.Background())

	// Assert
	require.NoError(t, err)
	require.Len(t, got.Project.Services, 1)
	require.Len(t, got.Project.Services[0].Containers, 2, "the one-off is not a replica")
	assert.Equal(t, 1, got.Project.Services[0].Containers[0].Number, "replicas sort by number")
	assert.Equal(t, 2, got.Project.Services[0].Containers[1].Number)
	assert.Equal(t, "c-web-1", got.Project.Services[0].Containers[0].Name)
	daemon.AssertExpectations(t)
}

func TestInspector_Introspect_NoContainers(t *testing.T) {
	// Arrange
	daemon := &mockDockerAPI{}
	daemon.expectContainers()

	// Act
	_, err := NewInspector(daemon, stack).Introspect(context.Background())

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"demo"`)
	daemon.AssertExpectations(t)
}

func TestInspector_Introspect_DaemonFailures(t *testing.T) {
	daemonErr := errors.New("daemon refused")

	tests := []struct {
		name  string
		setup func(daemon *mockDockerAPI)
	}{
		{
			name: "listing containers",
			setup: func(daemon *mockDockerAPI) {
				daemon.On("ContainerList", mock.Anything, mock.Anything).
					Return([]container.Summary(nil), daemonErr).Once()
			},
		},
		{
			name: "listing volumes",
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.On("VolumeList", mock.Anything, mock.Anything).
					Return(volume.ListResponse{}, daemonErr).Once()
			},
		},
		{
			name: "inspecting a container",
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.On("ContainerInspect", mock.Anything, "c-db-1").
					Return(container.InspectResponse{}, daemonErr).Once()
			},
		},
		{
			name: "listing networks",
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.expectInspect("c-db-1")
				daemon.On("NetworkList", mock.Anything, mock.Anything).
					Return([]network.Summary(nil), daemonErr).Once()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockDockerAPI{}
			tt.setup(daemon)

			// Act
			_, err := NewInspector(daemon, stack).Introspect(context.Background())

			// Assert
			require.ErrorIs(t, err, daemonErr)
			daemon.AssertExpectations(t)
		})
	}
}

// summary builds the container the daemon reports for one replica.
func summary(id, service, image string, number int, mounts ...container.MountPoint) container.Summary {
	return container.Summary{
		ID:    id,
		Names: []string{"/" + id},
		Image: image,
		Labels: map[string]string{
			api.ProjectLabel:         stack.String(),
			api.ServiceLabel:         service,
			api.ContainerNumberLabel: strconv.Itoa(number),
		},
		Mounts: mounts,
	}
}

func mountPoint(t mount.Type, name, source, destination string, readOnly bool) container.MountPoint {
	return container.MountPoint{
		Type:        t,
		Name:        name,
		Source:      source,
		Destination: destination,
		RW:          !readOnly,
	}
}

// ownedVolume is a volume compose created for the project, carrying its compose key.
func ownedVolume(name, key string) volume.Volume {
	return volume.Volume{Name: name, Labels: map[string]string{
		api.ProjectLabel: stack.String(),
		api.VolumeLabel:  key,
	}}
}

type mockDockerAPI struct{ mock.Mock }

func (m *mockDockerAPI) expectContainers(summaries ...container.Summary) {
	m.On("ContainerList", mock.Anything, mock.MatchedBy(func(opts container.ListOptions) bool {
		return opts.All && len(opts.Filters.Get("label")) == 1
	})).Return(summaries, nil).Once()
}

func (m *mockDockerAPI) expectInspect(ids ...string) {
	for _, id := range ids {
		m.On("ContainerInspect", mock.Anything, id).
			Return(container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id}}, nil).Once()
	}
}

func (m *mockDockerAPI) expectVolumes(volumes ...volume.Volume) {
	listed := make([]*volume.Volume, 0, len(volumes))
	for i := range volumes {
		listed = append(listed, &volumes[i])
	}
	m.On("VolumeList", mock.Anything, mock.Anything).
		Return(volume.ListResponse{Volumes: listed}, nil).Once()
}

func (m *mockDockerAPI) expectNetworks(summaries ...network.Summary) {
	m.On("NetworkList", mock.Anything, mock.Anything).Return(summaries, nil).Once()
}

func (m *mockDockerAPI) ContainerList(ctx context.Context, opts container.ListOptions) ([]container.Summary, error) {
	args := m.Called(ctx, opts)
	summaries, _ := args.Get(0).([]container.Summary)
	return summaries, args.Error(1)
}

func (m *mockDockerAPI) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(container.InspectResponse), args.Error(1)
}

func (m *mockDockerAPI) VolumeList(ctx context.Context, opts volume.ListOptions) (volume.ListResponse, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).(volume.ListResponse), args.Error(1)
}

func (m *mockDockerAPI) VolumeInspect(ctx context.Context, name string) (volume.Volume, error) {
	args := m.Called(ctx, name)
	return args.Get(0).(volume.Volume), args.Error(1)
}

func (m *mockDockerAPI) NetworkList(ctx context.Context, opts network.ListOptions) ([]network.Summary, error) {
	args := m.Called(ctx, opts)
	summaries, _ := args.Get(0).([]network.Summary)
	return summaries, args.Error(1)
}
