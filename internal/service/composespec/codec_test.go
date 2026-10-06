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

package composespec

import (
	"context"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// richResult is the compose project the round-trip is measured on: every section a rebuild
// needs, on a service that declares something of each kind.
func richResult() model.IntrospectionResult {
	svc := richService()
	svc.Volumes[0].Volume = &model.VolumeMount{NoCopy: true, Subpath: "pg", Labels: map[string]string{"tier": "db"}}
	svc.Volumes = append(svc.Volumes,
		model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind", Source: "/srv/demo/conf", Target: "/etc/conf",
			Bind: &model.BindMount{Propagation: "rshared", Recursive: "disabled"}},
		model.ServiceVolume{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/run/cache",
			Tmpfs: &model.TmpfsMount{SizeBytes: 1 << 20, Mode: 0o1777}},
		model.ServiceVolume{Type: model.VolumeTypeImage, RawType: "image", Source: "assets:1", Target: "/assets",
			ReadOnly: true, Image: &model.ImageMount{Subpath: "static"}},
	)
	svc.Containers[0].Labels = map[string]string{
		api.WorkingDirLabel:  "/srv/demo",
		api.ConfigFilesLabel: "/srv/demo/compose.yaml",
	}
	return model.IntrospectionResult{Project: model.Project{
		Name:     "demo",
		Services: []model.Service{svc},
		Volumes: []model.NamedVolume{{
			Key: "pgdata", Name: "demo_pgdata", Driver: "local",
			Labels: map[string]string{api.VolumeLabel: "pgdata"},
		}},
		Networks: []model.Network{{
			Key:  "back",
			Name: "demo_back",
			Inspect: network.Inspect{
				Driver: "bridge",
				IPAM: network.IPAM{
					Driver: "default",
					Config: []network.IPAMConfig{{Subnet: "172.28.0.0/16", Gateway: "172.28.0.1"}},
				},
				Labels: map[string]string{api.NetworkLabel: "back"},
			},
		}},
	}}
}

// The artifact has to survive its own loader: what Marshal writes is the only
// statement of the compose project a rollback doing `down` has left.
func TestAssembler_MarshalParse_RoundTrip(t *testing.T) {
	// Arrange
	assembler := NewAssembler()
	assembled, err := assembler.Assemble(context.Background(), richResult())
	require.NoError(t, err)

	// Act
	spec, err := assembler.Marshal(assembled)
	require.NoError(t, err)
	parsed, err := assembler.Parse(context.Background(), spec)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, assembled.Name, parsed.Name)
	assert.Equal(t, assembled.WorkingDir, parsed.WorkingDir)
	assert.Equal(t, assembled.ComposeFiles, parsed.ComposeFiles)

	before, after := assembled.Services["db"], parsed.Services["db"]
	assert.Equal(t, before.Image, after.Image)
	assert.Equal(t, before.Command, after.Command)
	assert.Equal(t, before.Entrypoint, after.Entrypoint)
	assert.Equal(t, before.Environment, after.Environment)
	assert.Equal(t, before.User, after.User)
	assert.Equal(t, before.Hostname, after.Hostname)
	assert.Equal(t, before.WorkingDir, after.WorkingDir)
	assert.Equal(t, before.Ports, after.Ports)
	assert.Equal(t, before.Restart, after.Restart)
	assert.Equal(t, before.HealthCheck, after.HealthCheck)
	assert.Equal(t, before.Labels, after.Labels)
	assert.Equal(t, before.Volumes, after.Volumes)
	assert.Equal(t, before.Tmpfs, after.Tmpfs)
	assert.Equal(t, before.ExtraHosts, after.ExtraHosts)
	assert.Equal(t, before.Sysctls, after.Sysctls)
	assert.Equal(t, before.Ulimits, after.Ulimits)
	assert.Equal(t, before.Devices, after.Devices)
	assert.Equal(t, before.MemLimit, after.MemLimit)
	assert.Equal(t, before.PidsLimit, after.PidsLimit)
	assert.Equal(t, before.CPUS, after.CPUS)
	assert.Equal(t, before.Privileged, after.Privileged)
	assert.Equal(t, before.ShmSize, after.ShmSize)
	assert.Equal(t, before.StopSignal, after.StopSignal)
	assert.Equal(t, before.StopGracePeriod, after.StopGracePeriod)
	assert.Equal(t, before.PullPolicy, after.PullPolicy)
	assert.Equal(t, before.DependsOn, after.DependsOn)
	assert.Equal(t, before.Networks, after.Networks)
	require.NotNil(t, after.Scale)
	assert.Equal(t, *before.Scale, *after.Scale)

	assert.Equal(t, assembled.Volumes["pgdata"].Name, parsed.Volumes["pgdata"].Name)
	assert.Equal(t, assembled.Networks["back"].Name, parsed.Networks["back"].Name)
	assert.Equal(t, assembled.Networks["back"].Ipam, parsed.Networks["back"].Ipam)

	// The artifact is a fixed point: what the loader normalises, the assembler
	// already wrote, so a re-render of the parsed project is the same document.
	again, err := assembler.Marshal(parsed)
	require.NoError(t, err)
	assert.Equal(t, string(spec), string(again))
}

// The daemon tells an empty request from none, so the artifact has to as well: an empty
// volume: or network entry must not come back nil, nor a nil one empty.
func TestAssembler_MarshalParse_EmptyOptions(t *testing.T) {
	// Arrange
	assembler := NewAssembler()
	project := &types.Project{
		Name: "demo",
		Services: types.Services{"app": {
			Name:  "app",
			Image: "busybox",
			Volumes: []types.ServiceVolumeConfig{
				{Type: types.VolumeTypeVolume, Target: "/empty", Volume: &types.ServiceVolumeVolume{}},
				{Type: types.VolumeTypeVolume, Target: "/none"},
			},
			Networks: map[string]*types.ServiceNetworkConfig{"front": {}, "back": nil},
		}},
		Networks: types.Networks{"front": {Name: "demo_front"}, "back": {Name: "demo_back"}},
	}

	// Act
	spec, err := assembler.Marshal(project)
	require.NoError(t, err)
	parsed, err := assembler.Parse(context.Background(), spec)

	// Assert
	require.NoError(t, err)
	app := parsed.Services["app"]
	require.Len(t, app.Volumes, 2)
	assert.Equal(t, &types.ServiceVolumeVolume{}, app.Volumes[0].Volume)
	assert.Nil(t, app.Volumes[1].Volume)
	assert.Equal(t, map[string]*types.ServiceNetworkConfig{"front": {}, "back": nil}, app.Networks)
}

// A service reference, a predefined network mode and a link are what compose's loader
// validates against the services: they have to come back as written.
func TestAssembler_MarshalParse_SharedNamespaces(t *testing.T) {
	// Arrange
	const dbID = "9a8b7c6d5e4f30211203f4e5d6c7b8a99a8b7c6d5e4f30211203f4e5d6c7b8a9"
	replica := func(id, name string, host container.HostConfig) model.Container {
		return model.Container{ID: id, Name: name, Number: 1, Inspect: container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{ID: id, HostConfig: &host},
			Config:            &container.Config{Image: "alpine:3"},
		}}
	}
	res := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{
			{Name: "app", Containers: []model.Container{replica("app1", "demo-app-1", container.HostConfig{
				NetworkMode: container.NetworkMode("container:" + dbID),
				IpcMode:     container.IpcMode("container:" + dbID),
				PidMode:     container.PidMode("container:" + dbID),
			})}},
			{Name: "db", Containers: []model.Container{replica(dbID, "demo-db-1", container.HostConfig{NetworkMode: "bridge"})}},
			{Name: "backup", Containers: []model.Container{replica("backup1", "demo-backup-1", container.HostConfig{
				NetworkMode: "default", VolumesFrom: []string{dbID, "sidecar"},
				Links: []string{"/demo-db-1:/demo-backup-1/database", "/demo-db-1:/demo-backup-1/db-1"},
			})}},
		},
	}}
	assembler := NewAssembler()
	assembled, err := assembler.Assemble(context.Background(), res)
	require.NoError(t, err)

	// Act
	spec, err := assembler.Marshal(assembled)
	require.NoError(t, err)
	parsed, err := assembler.Parse(context.Background(), spec)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "service:db", parsed.Services["app"].NetworkMode)
	assert.Equal(t, "service:db", parsed.Services["app"].Ipc)
	assert.Equal(t, "service:db", parsed.Services["app"].Pid)
	assert.Equal(t, "bridge", parsed.Services["db"].NetworkMode)
	assert.Equal(t, "default", parsed.Services["backup"].NetworkMode)
	assert.Equal(t, []string{"db", "container:sidecar"}, parsed.Services["backup"].VolumesFrom)
	assert.Equal(t, []string{"db:database"}, parsed.Services["backup"].Links)
	for _, name := range []string{"app", "db", "backup"} {
		assert.Equal(t, assembled.Services[name].DependsOn, parsed.Services[name].DependsOn, name)
	}

	again, err := assembler.Marshal(parsed)
	require.NoError(t, err)
	assert.Equal(t, string(spec), string(again))
}

func TestAssembler_Marshal_NoProject(t *testing.T) {
	// Act
	_, err := NewAssembler().Marshal(nil)

	// Assert
	assert.ErrorIs(t, err, errNoProject)
}

func TestAssembler_Parse_Junk(t *testing.T) {
	// Act
	_, err := NewAssembler().Parse(context.Background(), []byte("services: [nope\n"))

	// Assert
	require.Error(t, err)
}
