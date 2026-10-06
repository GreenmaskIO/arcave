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
	"maps"
	"slices"
	"strconv"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	composeProject = model.DockerComposeProjectName("demo")
	anonName       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ownedName      = "demo_pgdata"

	testImageID     = model.ImageDigestID("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	testBaseImageID = model.ImageDigestID("sha256:2222222222222222222222222222222222222222222222222222222222222222")
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
	daemon.expectImage(testImageID, nil)
	daemon.expectInfo()
	inspector := NewInspector(daemon, composeProject)

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
			ReadOnly: true, Bind: &model.BindMount{CreateHostPath: true}},
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

func TestInspector_Introspect_Referenced(t *testing.T) {
	const (
		vpnID  = "c-vpn"
		goneID = "c-gone"
	)
	inNamespaces := func(id string, h container.HostConfig) container.InspectResponse {
		r := inspected(id, testImageID.String())
		r.HostConfig = &h
		return r
	}
	vpn := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: vpnID, Name: "/vpn"}}

	tests := []struct {
		name     string
		replicas []container.InspectResponse
		inspects map[string]error // the foreign references inspected, each once; nil error answers vpn
		want     map[string]container.InspectResponse
	}{
		{
			name: "one inspect per distinct foreign container",
			replicas: []container.InspectResponse{
				inNamespaces("c-app-1", container.HostConfig{
					NetworkMode: "container:" + vpnID, IpcMode: "container:" + vpnID, PidMode: "container:" + vpnID,
				}),
				inNamespaces("c-app-2", container.HostConfig{NetworkMode: "container:" + vpnID}),
			},
			inspects: map[string]error{vpnID: nil},
			want:     map[string]container.InspectResponse{vpnID: vpn},
		},
		{
			name: "a replica of the project is no reference",
			replicas: []container.InspectResponse{
				inNamespaces("c-app-1", container.HostConfig{
					NetworkMode: "container:c-app-2", VolumesFrom: []string{"c-app-2:ro"},
				}),
				inNamespaces("c-app-2", container.HostConfig{}),
			},
			want: map[string]container.InspectResponse{},
		},
		{
			name: "volumes_from names a foreign container",
			replicas: []container.InspectResponse{
				inNamespaces("c-app-1", container.HostConfig{VolumesFrom: []string{vpnID + ":ro"}}),
			},
			inspects: map[string]error{vpnID: nil},
			want:     map[string]container.InspectResponse{vpnID: vpn},
		},
		{
			name: "a container the daemon no longer has is left out",
			replicas: []container.InspectResponse{
				inNamespaces("c-app-1", container.HostConfig{NetworkMode: "container:" + goneID}),
			},
			inspects: map[string]error{goneID: cerrdefs.ErrNotFound},
			want:     map[string]container.InspectResponse{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockDockerAPI{}
			summaries := make([]container.Summary, 0, len(tt.replicas))
			for n, r := range tt.replicas {
				summaries = append(summaries, summary(r.ID, "app", "busybox", n+1))
			}
			daemon.expectContainers(summaries...)
			daemon.expectInspected(tt.replicas...)
			for id, err := range tt.inspects {
				answer := vpn
				if err != nil {
					answer = container.InspectResponse{}
				}
				daemon.On("ContainerInspect", mock.Anything, id).Return(answer, err).Once()
			}
			daemon.expectVolumes()
			daemon.expectNetworks()
			daemon.expectImage(testImageID, nil)
			daemon.expectInfo()

			// Act
			got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Referenced)
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_JoinsTheTwoVolumeLevels(t *testing.T) {
	// Arrange
	daemon := &mockDockerAPI{}
	daemon.expectContainers(summary("c-db-1", "db", "postgres:16", 1,
		mountPoint(mount.TypeVolume, ownedName, "", "/data", false)))
	daemon.expectInspect("c-db-1")
	daemon.expectVolumes(ownedVolume(ownedName, "pgdata"))
	daemon.expectNetworks()
	daemon.expectImage(testImageID, nil)
	daemon.expectInfo()

	// Act
	got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

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
				Source: "/host/cfg", Target: "/etc/cfg", Bind: &model.BindMount{CreateHostPath: true}},
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
			daemon.expectImage(testImageID, nil)
			daemon.expectInfo()
			if tt.mount.Type == mount.TypeVolume && tt.mount.Name != ownedName {
				daemon.On("VolumeInspect", mock.Anything, tt.mount.Name).
					Return(volume.Volume{Name: tt.mount.Name, Labels: tt.labels}, nil).Once()
			}

			// Act
			got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []model.ServiceVolume{want}, got.Project.Services[0].Volumes)
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_MountOptions(t *testing.T) {
	tests := []struct {
		name  string
		mount container.MountPoint
		host  container.HostConfig
		want  model.ServiceVolume
	}{
		{
			name:  "the mount API carries a volume's nocopy, subpath and labels",
			mount: mountPoint(mount.TypeVolume, ownedName, "", "/data", false),
			host: container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: ownedName,
				Target: "/data", VolumeOptions: &mount.VolumeOptions{
					NoCopy: true, Subpath: "pg", Labels: map[string]string{"tier": "db"},
				}}}},
			want: model.ServiceVolume{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata",
				Target: "/data", VolumeName: ownedName, Volume: &model.VolumeMount{
					NoCopy: true, Subpath: "pg", Labels: map[string]string{"tier": "db"},
				}},
		},
		{
			name:  "empty volume options are kept",
			mount: mountPoint(mount.TypeVolume, ownedName, "", "/data", false),
			host: container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: ownedName,
				Target: "/data", VolumeOptions: &mount.VolumeOptions{}}}},
			want: model.ServiceVolume{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata",
				Target: "/data", VolumeName: ownedName, Volume: &model.VolumeMount{}},
		},
		{
			name:  "a volume requested without options has none",
			mount: mountPoint(mount.TypeVolume, ownedName, "", "/data", false),
			host: container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: ownedName,
				Target: "/data"}}},
			want: model.ServiceVolume{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata",
				Target: "/data", VolumeName: ownedName},
		},
		{
			name:  "the bind API's nocopy flag",
			mount: mountPoint(mount.TypeVolume, ownedName, "", "/data", false),
			host:  container.HostConfig{Binds: []string{ownedName + ":/data:rw,nocopy"}},
			want: model.ServiceVolume{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata",
				Target: "/data", VolumeName: ownedName, Volume: &model.VolumeMount{NoCopy: true}},
		},
		{
			name:  "the bind API's SELinux relabel",
			mount: mountPoint(mount.TypeBind, "", "/host/cfg", "/etc/cfg", true),
			host:  container.HostConfig{Binds: []string{"/host/cfg:/etc/cfg:ro,Z"}},
			want: model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg",
				Target: "/etc/cfg", ReadOnly: true, Bind: &model.BindMount{SELinux: "Z", CreateHostPath: true}},
		},
		{
			name:  "a bind through the mount API creates no host path unless asked",
			mount: mountPoint(mount.TypeBind, "", "/host/cfg", "/etc/cfg", false),
			host: container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/host/cfg",
				Target: "/etc/cfg"}}},
			want: model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg",
				Target: "/etc/cfg", Bind: &model.BindMount{}},
		},
		{
			name:  "a bind's recursion and host path creation through the mount API",
			mount: mountPoint(mount.TypeBind, "", "/host/cfg", "/etc/cfg", false),
			host: container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/host/cfg",
				Target: "/etc/cfg", BindOptions: &mount.BindOptions{
					CreateMountpoint: true, ReadOnlyNonRecursive: true,
				}}}},
			want: model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg",
				Target: "/etc/cfg", Bind: &model.BindMount{CreateHostPath: true, Recursive: "writable"}},
		},
		{
			name:  "a bind's source is the path compose gave, not the one Docker Desktop reports",
			mount: mountPoint(mount.TypeBind, "", "/host_mnt/srv/cfg", "/etc/cfg", false),
			host:  container.HostConfig{Binds: []string{"/srv/cfg:/etc/cfg:rw"}},
			want: model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind", Source: "/srv/cfg",
				Target: "/etc/cfg", Bind: &model.BindMount{CreateHostPath: true}},
		},
		{
			name:  "a tmpfs mount's size and mode",
			mount: mountPoint(mount.TypeTmpfs, "", "", "/run", false),
			host: container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeTmpfs, Target: "/run",
				TmpfsOptions: &mount.TmpfsOptions{SizeBytes: 1 << 20, Mode: 0o1777}}}},
			want: model.ServiceVolume{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/run",
				Tmpfs: &model.TmpfsMount{SizeBytes: 1 << 20, Mode: 0o1777}},
		},
		{
			name:  "an image mount's reference and subpath",
			mount: mountPoint(mount.TypeImage, "", "", "/assets", true),
			host: container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeImage, Source: "assets:1",
				Target: "/assets", ReadOnly: true, ImageOptions: &mount.ImageOptions{Subpath: "static"}}}},
			want: model.ServiceVolume{Type: model.VolumeTypeImage, RawType: "image", Source: "assets:1",
				Target: "/assets", ReadOnly: true, Image: &model.ImageMount{Subpath: "static"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			host := tt.host
			inspect := inspected("c-db-1", testImageID.String(), tt.mount)
			inspect.HostConfig = &host
			daemon := &mockDockerAPI{}
			daemon.expectContainers(summary("c-db-1", "db", "busybox", 1, tt.mount))
			daemon.expectInspected(inspect)
			daemon.expectVolumes(ownedVolume(ownedName, "pgdata"))
			daemon.expectNetworks()
			daemon.expectImage(testImageID, nil)
			daemon.expectInfo()

			// Act
			got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []model.ServiceVolume{tt.want}, got.Project.Services[0].Volumes)
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_DesktopBindSource(t *testing.T) {
	// Arrange
	bind := mountPoint(mount.TypeBind, "", "/host_mnt/private/var/conf", "/conf", true)
	inspect := inspected("c-db-1", testImageID.String(), bind)
	inspect.HostConfig = &container.HostConfig{Binds: []string{"/host_mnt/private/var/conf:/conf:ro"}}
	listed := summary("c-db-1", "db", "busybox", 1, bind)
	// Docker Desktop shows its labels in the container list only.
	listed.Labels["desktop.docker.io/binds/0/Source"] = "/var/conf"
	listed.Labels["desktop.docker.io/binds/0/SourceKind"] = "hostFile"
	listed.Labels["desktop.docker.io/binds/0/Target"] = "/conf"
	daemon := &mockDockerAPI{}
	daemon.expectContainers(listed)
	daemon.expectInspected(inspect)
	daemon.expectVolumes()
	daemon.expectNetworks()
	daemon.expectImage(testImageID, nil)
	daemon.expectInfo()

	// Act
	got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.ServiceVolume{{Type: model.VolumeTypeBind, RawType: "bind", Source: "/var/conf",
		Target: "/conf", ReadOnly: true, Bind: &model.BindMount{CreateHostPath: true}}},
		got.Project.Services[0].Volumes)
	daemon.AssertExpectations(t)
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
	daemon.expectImage(testImageID, nil)
	daemon.expectInfo()

	// Act
	got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

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
	oneoff := summary("c-web-run", "web", "nginx:1", 1)
	oneoff.Labels[api.OneoffLabel] = "True"

	tests := []struct {
		name       string
		containers []container.Summary
	}{
		{name: "the daemon holds none"},
		{name: "only one-off containers are left", containers: []container.Summary{oneoff}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockDockerAPI{}
			daemon.expectContainers(tt.containers...)

			// Act
			_, err := NewInspector(daemon, composeProject).Introspect(context.Background())

			// Assert
			require.Error(t, err)
			assert.Contains(t, err.Error(), `read containers of compose project "demo": no containers found`)
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_DaemonFailures(t *testing.T) {
	daemonErr := errors.New("daemon refused")

	tests := []struct {
		name    string
		setup   func(daemon *mockDockerAPI)
		wantErr error  // daemonErr when nil
		wantMsg string // the context the error carries
	}{
		{
			name:    "listing containers",
			wantMsg: `read containers of compose project "demo"`,
			setup: func(daemon *mockDockerAPI) {
				daemon.On("ContainerList", mock.Anything, mock.Anything).
					Return([]container.Summary(nil), daemonErr).Once()
			},
		},
		{
			name:    "listing volumes",
			wantMsg: `read volumes of compose project "demo"`,
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.On("VolumeList", mock.Anything, mock.Anything).
					Return(volume.ListResponse{}, daemonErr).Once()
			},
		},
		{
			name:    "inspecting a container",
			wantMsg: `read services of compose project "demo": service "db": inspect container c-db-1`,
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.On("ContainerInspect", mock.Anything, "c-db-1").
					Return(container.InspectResponse{}, daemonErr).Once()
			},
		},
		{
			name:    "inspecting a mounted volume",
			wantMsg: `read services of compose project "demo": service "db": inspect volume "shared"`,
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1,
					mountPoint(mount.TypeVolume, "shared", "", "/data", false)))
				daemon.expectVolumes()
				daemon.expectInspect("c-db-1")
				daemon.On("VolumeInspect", mock.Anything, "shared").
					Return(volume.Volume{}, daemonErr).Once()
			},
		},
		{
			name:    "listing networks",
			wantMsg: `read networks of compose project "demo"`,
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.expectInspect("c-db-1")
				daemon.On("NetworkList", mock.Anything, mock.Anything).
					Return([]network.Summary(nil), daemonErr).Once()
			},
		},
		{
			name:    "inspecting a container's image",
			wantMsg: `read images of compose project "demo": inspect image ` + testImageID.String(),
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.expectInspect("c-db-1")
				daemon.expectNetworks()
				daemon.On("ImageInspect", mock.Anything, testImageID.String()).
					Return(image.InspectResponse{}, daemonErr).Once()
			},
		},
		{
			name:    "a container's image the daemon does not hold",
			wantMsg: `inspect image ` + testImageID.String(),
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.expectInspect("c-db-1")
				daemon.expectNetworks()
				daemon.On("ImageInspect", mock.Anything, testImageID.String()).
					Return(image.InspectResponse{}, cerrdefs.ErrNotFound).Once()
			},
			wantErr: cerrdefs.ErrNotFound,
		},
		{
			name:    "a container whose image is no id",
			wantMsg: `read images of compose project "demo": image of container c-db-1`,
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.expectInspected(inspected("c-db-1", "busybox"))
				daemon.expectNetworks()
			},
			wantErr: model.ErrInvalidDigestID,
		},
		{
			name:    "inspecting a base image",
			wantMsg: `inspect base image ` + testBaseImageID.String() + ` of image ` + testImageID.String(),
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.expectInspect("c-db-1")
				daemon.expectNetworks()
				daemon.expectImage(testImageID, map[string]string{model.BaseImageIDLabel: testBaseImageID.String()})
				daemon.On("ImageInspect", mock.Anything, testBaseImageID.String()).
					Return(image.InspectResponse{}, daemonErr).Once()
			},
		},
		{
			name:    "reading daemon info",
			wantMsg: `read docker daemon info`,
			setup: func(daemon *mockDockerAPI) {
				daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
				daemon.expectVolumes()
				daemon.expectInspect("c-db-1")
				daemon.expectNetworks()
				daemon.expectImage(testImageID, nil)
				daemon.On("Info", mock.Anything).Return(system.Info{}, daemonErr).Once()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockDockerAPI{}
			tt.setup(daemon)

			// Act
			_, err := NewInspector(daemon, composeProject).Introspect(context.Background())

			// Assert
			wantErr := tt.wantErr
			if wantErr == nil {
				wantErr = daemonErr
			}
			require.ErrorIs(t, err, wantErr)
			assert.Contains(t, err.Error(), tt.wantMsg)
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_Images(t *testing.T) {
	labelledBase := map[string]string{model.BaseImageIDLabel: testBaseImageID.String()}

	tests := []struct {
		name     string
		replicas []replica
		images   map[model.ImageDigestID]map[string]string // inspected once each, with these labels
		missing  []model.ImageDigestID                     // inspected once each, not found
		want     []model.ImageDigestID
	}{
		{
			name: "replicas and services on one image read it once",
			replicas: []replica{
				{id: "c-web-1", service: "web", image: testImageID.String()},
				{id: "c-web-2", service: "web", image: testImageID.String()},
				{id: "c-worker-1", service: "worker", image: testImageID.String()},
			},
			images: map[model.ImageDigestID]map[string]string{testImageID: nil},
			want:   []model.ImageDigestID{testImageID},
		},
		{
			name:     "a base named by the label is read",
			replicas: []replica{{id: "c-web-1", service: "web", image: testImageID.String()}},
			images:   map[model.ImageDigestID]map[string]string{testImageID: labelledBase, testBaseImageID: nil},
			want:     []model.ImageDigestID{testImageID, testBaseImageID},
		},
		{
			name: "a base a container already runs on is not read again",
			replicas: []replica{
				{id: "c-web-1", service: "web", image: testImageID.String()},
				{id: "c-plain-1", service: "plain", image: testBaseImageID.String()},
			},
			images: map[model.ImageDigestID]map[string]string{testImageID: labelledBase, testBaseImageID: nil},
			want:   []model.ImageDigestID{testImageID, testBaseImageID},
		},
		{
			name:     "a base the daemon no longer holds is left out",
			replicas: []replica{{id: "c-web-1", service: "web", image: testImageID.String()}},
			images:   map[model.ImageDigestID]map[string]string{testImageID: labelledBase},
			missing:  []model.ImageDigestID{testBaseImageID},
			want:     []model.ImageDigestID{testImageID},
		},
		{
			name:     "a label that is no id is not followed",
			replicas: []replica{{id: "c-web-1", service: "web", image: testImageID.String()}},
			images: map[model.ImageDigestID]map[string]string{
				testImageID: {model.BaseImageIDLabel: "postgres:16"},
			},
			want: []model.ImageDigestID{testImageID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockDockerAPI{}
			daemon.expectReplicas(tt.replicas...)
			daemon.expectVolumes()
			daemon.expectNetworks()
			for id, labels := range tt.images {
				daemon.expectImage(id, labels)
			}
			for _, id := range tt.missing {
				daemon.On("ImageInspect", mock.Anything, id.String()).
					Return(image.InspectResponse{}, cerrdefs.ErrNotFound).Once()
			}
			daemon.expectInfo()

			// Act
			got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

			// Assert
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, slices.Collect(maps.Keys(got.Images)))
			for _, id := range tt.want {
				assert.Equal(t, imageInspect(id, tt.images[id]), got.Images[id], "image %s verbatim", id)
			}
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_Volumes(t *testing.T) {
	shared := volume.Volume{Name: "shared", Driver: "local"}
	anonFirst := volume.Volume{Name: anonName, Labels: map[string]string{anonymousVolumeLabel: ""}}
	anonSecond := volume.Volume{Name: "second-anon", Labels: map[string]string{anonymousVolumeLabel: ""}}

	tests := []struct {
		name     string
		replicas []replica
		owned    []volume.Volume
		foreign  []volume.Volume // inspected once each
		want     []volume.Volume
	}{
		{
			name:     "the project's own volumes are indexed from the list",
			replicas: []replica{{id: "c-db-1", service: "db", image: testImageID.String()}},
			owned:    []volume.Volume{ownedVolume(ownedName, "pgdata"), ownedVolume("demo_orphan", "orphan")},
			want:     []volume.Volume{ownedVolume(ownedName, "pgdata"), ownedVolume("demo_orphan", "orphan")},
		},
		{
			name: "a foreign volume two services mount is read once",
			replicas: []replica{
				{id: "c-a-1", service: "a", image: testImageID.String(),
					mounts: []container.MountPoint{mountPoint(mount.TypeVolume, "shared", "", "/a", false)}},
				{id: "c-b-1", service: "b", image: testImageID.String(),
					mounts: []container.MountPoint{mountPoint(mount.TypeVolume, "shared", "", "/b", false)}},
			},
			foreign: []volume.Volume{shared},
			want:    []volume.Volume{shared},
		},
		{
			name: "the anonymous volume of a second replica is read",
			replicas: []replica{
				{id: "c-web-1", service: "web", image: testImageID.String(),
					mounts: []container.MountPoint{mountPoint(mount.TypeVolume, anonFirst.Name, "", "/cache", false)}},
				{id: "c-web-2", service: "web", image: testImageID.String(),
					mounts: []container.MountPoint{mountPoint(mount.TypeVolume, anonSecond.Name, "", "/cache", false)}},
			},
			foreign: []volume.Volume{anonFirst, anonSecond},
			want:    []volume.Volume{anonFirst, anonSecond},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockDockerAPI{}
			daemon.expectReplicas(tt.replicas...)
			daemon.expectVolumes(tt.owned...)
			for _, v := range tt.foreign {
				daemon.On("VolumeInspect", mock.Anything, v.Name).Return(v, nil).Once()
			}
			daemon.expectNetworks()
			daemon.expectImage(testImageID, nil)
			daemon.expectInfo()

			want := make(map[string]volume.Volume, len(tt.want))
			for _, v := range tt.want {
				want[v.Name] = v
			}

			// Act
			got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, want, got.Volumes)
			daemon.AssertExpectations(t)
		})
	}
}

func TestInspector_Introspect_Daemon(t *testing.T) {
	// Arrange
	info := system.Info{ID: "daemon-1", ServerVersion: "28.5.2", Driver: "overlay2"}
	daemon := &mockDockerAPI{}
	daemon.expectContainers(summary("c-db-1", "db", "busybox", 1))
	daemon.expectInspect("c-db-1")
	daemon.expectVolumes()
	daemon.expectNetworks()
	daemon.expectImage(testImageID, nil)
	daemon.On("Info", mock.Anything).Return(info, nil).Once()

	// Act
	got, err := NewInspector(daemon, composeProject).Introspect(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, info, got.Daemon)
	daemon.AssertExpectations(t)
}

// replica is one container of the compose project: its summary and its inspect agree on the mounts.
type replica struct {
	id, service, image string
	mounts             []container.MountPoint
}

// summary builds the container the daemon reports for one replica.
func summary(id, service, image string, number int, mounts ...container.MountPoint) container.Summary {
	return container.Summary{
		ID:    id,
		Names: []string{"/" + id},
		Image: image,
		Labels: map[string]string{
			api.ProjectLabel:         composeProject.String(),
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

// inspected is what `docker container inspect` reports for one container.
func inspected(id, imageID string, mounts ...container.MountPoint) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: id, Image: imageID},
		Mounts:            mounts,
	}
}

// imageInspect is what `docker image inspect` reports for an image carrying labels.
func imageInspect(id model.ImageDigestID, labels map[string]string) image.InspectResponse {
	return image.InspectResponse{
		ID:     id.String(),
		Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: labels}},
	}
}

// ownedVolume is a volume compose created for the project, carrying its compose key.
func ownedVolume(name, key string) volume.Volume {
	return volume.Volume{Name: name, Labels: map[string]string{
		api.ProjectLabel: composeProject.String(),
		api.VolumeLabel:  key,
	}}
}

type mockDockerAPI struct{ mock.Mock }

func (m *mockDockerAPI) expectContainers(summaries ...container.Summary) {
	m.On("ContainerList", mock.Anything, mock.MatchedBy(func(opts container.ListOptions) bool {
		return opts.All && len(opts.Filters.Get("label")) == 1
	})).Return(summaries, nil).Once()
}

// expectInspect answers each container as running on testImageID.
func (m *mockDockerAPI) expectInspect(ids ...string) {
	for _, id := range ids {
		m.expectInspected(inspected(id, testImageID.String()))
	}
}

func (m *mockDockerAPI) expectInspected(responses ...container.InspectResponse) {
	for _, r := range responses {
		m.On("ContainerInspect", mock.Anything, r.ID).Return(r, nil).Once()
	}
}

// expectReplicas lists the replicas, numbered per service in order, and inspects each.
func (m *mockDockerAPI) expectReplicas(replicas ...replica) {
	numbers := map[string]int{}
	summaries := make([]container.Summary, 0, len(replicas))
	for _, r := range replicas {
		numbers[r.service]++
		summaries = append(summaries, summary(r.id, r.service, "img", numbers[r.service], r.mounts...))
		m.expectInspected(inspected(r.id, r.image, r.mounts...))
	}
	m.expectContainers(summaries...)
}

func (m *mockDockerAPI) expectImage(id model.ImageDigestID, labels map[string]string) {
	m.On("ImageInspect", mock.Anything, id.String()).Return(imageInspect(id, labels), nil).Once()
}

func (m *mockDockerAPI) expectInfo() {
	m.On("Info", mock.Anything).Return(system.Info{ID: "daemon"}, nil).Once()
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

func (m *mockDockerAPI) ImageInspect(
	ctx context.Context, imageID string, _ ...client.ImageInspectOption,
) (image.InspectResponse, error) {
	args := m.Called(ctx, imageID)
	return args.Get(0).(image.InspectResponse), args.Error(1)
}

func (m *mockDockerAPI) Info(ctx context.Context) (system.Info, error) {
	args := m.Called(ctx)
	return args.Get(0).(system.Info), args.Error(1)
}

func TestContainerName(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{name: "none", names: nil, want: ""},
		{name: "the own name", names: []string{"/demo-db-1"}, want: "demo-db-1"},
		{
			name:  "a legacy link target lists the link paths first",
			names: []string{"/demo-app-1/demo-db-1", "/demo-app-1/database", "/demo-db-1"},
			want:  "demo-db-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := containerName(tt.names)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
