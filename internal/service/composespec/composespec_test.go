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
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestAssembler_Assemble(t *testing.T) {
	// Arrange
	res := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{
			{
				Name:  "db",
				Image: "postgres:16",
				Labels: map[string]string{
					api.ServiceLabel: "db", "tier": "data",
					"desktop.docker.io/ports.scheme": "v2", "desktop.docker.io/ports/5432/tcp": ":5432",
				},
				Containers: []model.Container{{
					Name: "demo-db-1",
					Labels: map[string]string{
						api.WorkingDirLabel:  "/srv/demo",
						api.ConfigFilesLabel: "/srv/demo/compose.yaml,/srv/demo/compose.override.yaml",
					},
				}},
			},
			{
				Name:       "web",
				Image:      "nginx:1",
				Containers: []model.Container{{Name: "demo-web-1"}, {Name: "demo-web-2"}},
			},
		},
		Volumes: []model.NamedVolume{
			{
				Key: "pgdata", Name: "demo_pgdata", Driver: "local",
				DriverOpts: map[string]string{"type": "none"},
				Labels:     map[string]string{api.VolumeLabel: "pgdata", "keep": "yes"},
			},
			{Key: "shared", Name: "shared", External: true, Driver: "local"},
		},
		Networks: []model.Network{{
			Key:  "back",
			Name: "demo_back",
			Inspect: network.Inspect{
				Driver:   "bridge",
				Internal: true,
				Options:  map[string]string{"com.docker.network.bridge.name": "br-demo"},
				IPAM: network.IPAM{
					Driver: "default",
					Config: []network.IPAMConfig{{Subnet: "172.28.0.0/16", Gateway: "172.28.0.1"}},
				},
				Labels: map[string]string{api.NetworkLabel: "back"},
			},
		}},
	}}

	// Act
	project, err := NewAssembler().Assemble(context.Background(), res)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "demo", project.Name)
	assert.Equal(t, "/srv/demo", project.WorkingDir)
	assert.Equal(t, []string{"/srv/demo/compose.yaml", "/srv/demo/compose.override.yaml"}, project.ComposeFiles)

	require.Len(t, project.Services, 2)
	assert.Equal(t, "postgres:16", project.Services["db"].Image)
	assert.Equal(t, pullPolicyMissing, project.Services["db"].PullPolicy)
	// Compose and Docker Desktop re-stamp their own labels on create; only the user's survive.
	assert.Equal(t, types.Labels{"tier": "data"}, project.Services["db"].Labels)
	// Scale is the replica count: compose skips a service scaled to zero.
	require.NotNil(t, project.Services["db"].Scale)
	assert.Equal(t, 1, *project.Services["db"].Scale)
	require.NotNil(t, project.Services["web"].Scale)
	assert.Equal(t, 2, *project.Services["web"].Scale)

	assert.Equal(t, types.VolumeConfig{
		Name:       "demo_pgdata",
		Driver:     "local",
		DriverOpts: types.Options{"type": "none"},
		Labels:     types.Labels{"keep": "yes"},
	}, project.Volumes["pgdata"])
	// An external volume takes no other key: compose rejects a declaration of what it does not own.
	assert.Equal(t, types.VolumeConfig{Name: "shared", External: true}, project.Volumes["shared"])

	back := project.Networks["back"]
	assert.Equal(t, "demo_back", back.Name)
	assert.Equal(t, "bridge", back.Driver)
	assert.True(t, back.Internal)
	assert.Equal(t, types.Options{"com.docker.network.bridge.name": "br-demo"}, back.DriverOpts)
	// No container holds a static address in it, so the daemon picks the range again.
	assert.Equal(t, types.IPAMConfig{Driver: "default"}, back.Ipam)
}

func TestAssembler_Assemble_NetworkPools(t *testing.T) {
	pools := network.IPAM{
		Driver: "default",
		Config: []network.IPAMConfig{{Subnet: "172.28.0.0/16", Gateway: "172.28.0.1"}},
	}

	tests := []struct {
		name     string
		endpoint *network.EndpointSettings // the second replica's endpoint in demo_back
		want     types.IPAMConfig
	}{
		{
			name:     "an allocated address leaves the pool to the daemon",
			endpoint: &network.EndpointSettings{},
			want:     types.IPAMConfig{Driver: "default"},
		},
		{
			name:     "a static IPv4 address keeps the pool it came from",
			endpoint: &network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: "172.28.0.10"}},
			want: types.IPAMConfig{Driver: "default", Config: []*types.IPAMPool{
				{Subnet: "172.28.0.0/16", Gateway: "172.28.0.1"},
			}},
		},
		{
			name:     "a static IPv6 address keeps the pool too",
			endpoint: &network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{IPv6Address: "fd00::10"}},
			want: types.IPAMConfig{Driver: "default", Config: []*types.IPAMPool{
				{Subnet: "172.28.0.0/16", Gateway: "172.28.0.1"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			replica := func(id string, endpoint *network.EndpointSettings) model.Container {
				return model.Container{Name: id, Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{ID: id},
					NetworkSettings: &container.NetworkSettings{
						Networks: map[string]*network.EndpointSettings{"demo_back": endpoint},
					},
				}}
			}
			res := model.IntrospectionResult{Project: model.Project{
				Name: "demo",
				Services: []model.Service{{
					Name:  "web",
					Image: "nginx:1",
					Containers: []model.Container{
						replica("demo-web-1", &network.EndpointSettings{}),
						replica("demo-web-2", tt.endpoint),
					},
				}},
				Networks: []model.Network{{Key: "back", Name: "demo_back", Inspect: network.Inspect{IPAM: pools}}},
			}}

			// Act
			project, err := NewAssembler().Assemble(context.Background(), res)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, project.Networks["back"].Ipam)
		})
	}
}

func TestAssembler_Assemble_NetworkIPFamilies(t *testing.T) {
	enabled := func(b bool) *bool { return &b }

	tests := []struct {
		name     string
		inspect  network.Inspect
		wantOpts types.Options
		wantIPv4 *bool
		wantIPv6 *bool
	}{
		{
			name: "the options the daemon writes for a defaulted family are no declaration",
			inspect: network.Inspect{EnableIPv4: true, Options: map[string]string{
				enableIPv4Option: "true", enableIPv6Option: "false",
			}},
		},
		{
			name:     "a declared family carries no option, and is declared",
			inspect:  network.Inspect{EnableIPv4: true, EnableIPv6: true},
			wantIPv4: enabled(true),
			wantIPv6: enabled(true),
		},
		{
			name: "a driver option of the user's stays",
			inspect: network.Inspect{Options: map[string]string{
				"com.docker.network.bridge.name": "br-demo", enableIPv4Option: "true", enableIPv6Option: "false",
			}},
			wantOpts: types.Options{"com.docker.network.bridge.name": "br-demo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			res := model.IntrospectionResult{Project: model.Project{
				Name: "demo",
				Services: []model.Service{{Name: "app", Containers: []model.Container{{
					Inspect: container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "c1"}},
				}}}},
				Networks: []model.Network{{Key: "back", Name: "demo_back", Inspect: tt.inspect}},
			}}

			// Act
			project, err := NewAssembler().Assemble(context.Background(), res)

			// Assert
			require.NoError(t, err)
			back := project.Networks["back"]
			assert.Equal(t, tt.wantOpts, back.DriverOpts)
			assert.Equal(t, tt.wantIPv4, back.EnableIPv4)
			assert.Equal(t, tt.wantIPv6, back.EnableIPv6)
		})
	}
}

// A network the project attaches to but does not own carries no compose label, so
// only the containers name it.
func TestAssembler_Assemble_ForeignNetwork(t *testing.T) {
	// Arrange
	res := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{{
			Name:  "app",
			Image: "app:1",
			Containers: []model.Container{{
				Name: "demo-app-1",
				Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{ID: "abc"},
					NetworkSettings: &container.NetworkSettings{
						Networks: map[string]*network.EndpointSettings{
							"demo_back": {Aliases: []string{"app", "front"}},
							"corporate": {Aliases: []string{"app"}},
							"host":      {},
						},
					},
				},
			}},
		}},
		Networks: []model.Network{{Key: "back", Name: "demo_back"}},
	}}

	// Act
	project, err := NewAssembler().Assemble(context.Background(), res)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, types.NetworkConfig{Name: "corporate", External: true}, project.Networks["corporate"])
	assert.NotContains(t, project.Networks, "host")

	attached := project.Services["app"].Networks
	require.Contains(t, attached, "back")
	require.Contains(t, attached, "corporate")
	assert.NotContains(t, attached, "host")
	// The service name is compose's own alias, not a declaration.
	assert.Equal(t, []string{"front"}, attached["back"].Aliases)
}

// A dependency the compose project no longer holds is dropped, so every one kept is required.
func TestAssembler_Assemble_DependsOn(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  types.DependsOnConfig
	}{
		{name: "no label", label: "", want: nil},
		{
			name:  "a service alone waits for it to run",
			label: "cache",
			want:  types.DependsOnConfig{"cache": {Condition: "service_started", Restart: true, Required: true}},
		},
		{
			name:  "a service alone waits for its healthcheck",
			label: "db",
			want:  types.DependsOnConfig{"db": {Condition: "service_healthy", Restart: true, Required: true}},
		},
		{
			name:  "service and condition",
			label: "db:service_healthy",
			want:  types.DependsOnConfig{"db": {Condition: "service_healthy", Restart: true, Required: true}},
		},
		{
			name:  "service, condition and restart",
			label: "db:service_healthy:false",
			want:  types.DependsOnConfig{"db": {Condition: "service_healthy", Restart: false, Required: true}},
		},
		{
			name:  "several dependencies",
			label: "db:service_healthy:true,cache:service_started:true",
			want: types.DependsOnConfig{
				"db":    {Condition: "service_healthy", Restart: true, Required: true},
				"cache": {Condition: "service_started", Restart: true, Required: true},
			},
		},
		{
			name:  "a dependency the compose project no longer holds is dropped",
			label: "db:service_healthy,gone:service_started",
			want:  types.DependsOnConfig{"db": {Condition: "service_healthy", Restart: true, Required: true}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			res := model.IntrospectionResult{Project: model.Project{
				Name: "demo",
				Services: []model.Service{
					{
						Name:       "app",
						Image:      "busybox",
						Labels:     map[string]string{api.DependenciesLabel: tt.label},
						Containers: []model.Container{{Name: "demo-app-1"}},
					},
					{Name: "db", Image: "busybox", Containers: []model.Container{{Name: "demo-db-1", Inspect: container.InspectResponse{
						ContainerJSONBase: &container.ContainerJSONBase{ID: "db-id"},
						Config:            &container.Config{Healthcheck: &container.HealthConfig{Test: []string{"CMD", "true"}}},
					}}}},
					{Name: "cache", Image: "busybox", Containers: []model.Container{{Name: "demo-cache-1", Inspect: container.InspectResponse{
						ContainerJSONBase: &container.ContainerJSONBase{ID: "cache-id"},
						Config:            &container.Config{Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}},
					}}}},
				},
			}}
			assembler := NewAssembler()

			// Act
			project, err := assembler.Assemble(context.Background(), res)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, project.Services["app"].DependsOn)
			// Every condition has to survive compose's own loader, or the savepoint cannot be rolled back to.
			codec := NewCodec()
			spec, err := codec.Marshal(project)
			require.NoError(t, err)
			parsed, err := codec.Parse(context.Background(), spec)
			require.NoError(t, err)
			assert.Equal(t, tt.want, parsed.Services["app"].DependsOn)
		})
	}
}

func TestAssembler_Assemble_NoServices(t *testing.T) {
	// Act
	_, err := NewAssembler().Assemble(context.Background(), model.IntrospectionResult{Project: model.Project{Name: "demo"}})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"demo"`)
}

func TestAssembler_Assemble_WarnsOnDivergentReplicas(t *testing.T) {
	tests := []struct {
		name string
		env  []string // the second replica's environment
		want []string // the fields warned about; nil for silence
	}{
		{name: "identical replicas are silent", env: []string{"MODE=a"}},
		{name: "a diverged environment is named", env: []string{"MODE=b"}, want: []string{"environment"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			replica := func(id string, number int, env []string) model.Container {
				return model.Container{ID: id, Number: number, Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: id, Name: "/demo-web-" + strconv.Itoa(number), HostConfig: &container.HostConfig{},
					},
					Config: &container.Config{Image: "nginx:1", Env: env, Hostname: id[:12], Labels: map[string]string{
						api.ProjectLabel: "demo", api.ServiceLabel: "web", api.ContainerNumberLabel: strconv.Itoa(number),
					}},
				}}
			}
			res := model.IntrospectionResult{Project: model.Project{
				Name: "demo",
				Services: []model.Service{{Name: "web", Containers: []model.Container{
					replica("aaaaaaaaaaaaaaaa", 1, []string{"MODE=a"}),
					replica("bbbbbbbbbbbbbbbb", 2, tt.env),
				}}},
			}}
			var logs bytes.Buffer
			ctx := zerolog.New(&logs).WithContext(context.Background())

			// Act
			_, err := NewAssembler().Assemble(ctx, res)

			// Assert
			require.NoError(t, err)
			if tt.want == nil {
				assert.Empty(t, logs.String())
				return
			}
			var entry struct {
				Level   string   `json:"level"`
				Service string   `json:"service"`
				Fields  []string `json:"fields"`
			}
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			assert.Equal(t, "warn", entry.Level)
			assert.Equal(t, "web", entry.Service)
			assert.Equal(t, tt.want, entry.Fields)
		})
	}
}

func TestAssembler_Assemble_ForeignNamespace(t *testing.T) {
	const goneID = "0123456789ab0123456789ab0123456789ab0123456789ab0123456789ab0123"
	tests := []struct {
		name       string
		referenced map[string]container.InspectResponse
		want       string
		wantWarn   bool
	}{
		{
			name: "a container outside the project is declared by name",
			referenced: map[string]container.InspectResponse{vpnID: {
				ContainerJSONBase: &container.ContainerJSONBase{ID: vpnID, Name: "/vpn"},
			}},
			want: "container:vpn",
		},
		{name: "a container the daemon no longer has keeps its id", want: "container:" + vpnID, wantWarn: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			replica := func(id string) model.Container {
				return model.Container{ID: id, Number: 1, Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: id, Name: "/demo-app-1", HostConfig: &container.HostConfig{
							NetworkMode: container.NetworkMode("container:" + vpnID),
							IpcMode:     container.IpcMode("container:" + vpnID),
						},
					},
					Config: &container.Config{Image: "busybox"},
				}}
			}
			res := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{
					{Name: "app", Containers: []model.Container{replica(goneID)}},
				}},
				Referenced: tt.referenced,
			}
			var logs bytes.Buffer
			ctx := zerolog.New(&logs).WithContext(context.Background())

			// Act
			project, err := NewAssembler().Assemble(ctx, res)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, project.Services["app"].NetworkMode)
			assert.Equal(t, tt.want, project.Services["app"].Ipc)
			if !tt.wantWarn {
				assert.Empty(t, logs.String())
				return
			}
			var entry struct {
				Level     string `json:"level"`
				Service   string `json:"service"`
				Container string `json:"container"`
			}
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry), "one warning for both namespaces")
			assert.Equal(t, "warn", entry.Level)
			assert.Equal(t, "app", entry.Service)
			assert.Equal(t, vpnID, entry.Container)
		})
	}
}

func TestUserLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   types.Labels
	}{
		{name: "none", labels: nil, want: nil},
		{
			name: "compose, Docker Desktop and arcave labels are dropped",
			labels: map[string]string{
				api.ServiceLabel:                 "db",
				"desktop.docker.io/ports.scheme": "v2",
				model.BaseImageIDLabel:           "sha256:cc",
				model.BaseImageRefLabel:          "postgres:16",
				"tier":                           "data",
			},
			want: types.Labels{"tier": "data"},
		},
		{name: "nothing left renders as nil", labels: map[string]string{model.BaseImageRefLabel: "postgres:16"}, want: nil},
		{
			// The capture policy travels in the spec, so a rollback restores it with the topology.
			name:   "the capture policy is kept",
			labels: map[string]string{model.LabelCapture: "false", model.LabelMountsExclude: "/app"},
			want:   types.Labels{model.LabelCapture: "false", model.LabelMountsExclude: "/app"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, userLabels(tt.labels))
		})
	}
}
