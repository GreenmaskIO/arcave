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
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const fixtureContainerID = "3f1a2b3c4d5e6f708192a3b4c5d6e7f8"

// richService is the fixture every fidelity assertion below reads.
func richService() model.Service {
	stopTimeout := 25
	init := true
	pidsLimit := int64(512)
	return model.Service{
		Name:   "db",
		Image:  "postgres:16",
		Labels: map[string]string{api.ServiceLabel: "db", api.DependenciesLabel: "cache:service_healthy:true"},
		Volumes: []model.ServiceVolume{
			{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", VolumeName: "demo_pgdata", Target: "/var/lib/postgresql/data"},
			{Type: model.VolumeTypeAnon, RawType: "volume", VolumeName: "9f8e7d", Target: "/tmp/scratch"},
			{Type: model.VolumeTypeBind, RawType: "bind", Source: "/srv/demo/seed", Target: "/docker-entrypoint-initdb.d", ReadOnly: true,
				Bind: &model.BindMount{Propagation: "rprivate", CreateHostPath: true}},
		},
		Containers: []model.Container{{
			ID:     fixtureContainerID,
			Name:   "demo-db-1",
			Number: 1,
			Inspect: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID:   fixtureContainerID,
					Name: "/demo-db-1",
					HostConfig: &container.HostConfig{
						PortBindings: nat.PortMap{
							"5432/tcp": []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "15432"}},
						},
						RestartPolicy: container.RestartPolicy{Name: "on-failure", MaximumRetryCount: 3},
						NetworkMode:   "demo_back",
						LogConfig:     container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m"}},
						ExtraHosts:    []string{"api.local:10.0.0.5"},
						Tmpfs:         map[string]string{"/run/lock": "size=1m"},
						CapAdd:        []string{"SYS_NICE"},
						Privileged:    true,
						Init:          &init,
						ShmSize:       268435456,
						Sysctls:       map[string]string{"net.core.somaxconn": "1024"},
						IpcMode:       "private",
						Resources: container.Resources{
							Memory:    1073741824,
							NanoCPUs:  1500000000,
							PidsLimit: &pidsLimit,
							Ulimits:   []*container.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}},
							Devices:   []container.DeviceMapping{{PathOnHost: "/dev/fuse", PathInContainer: "/dev/fuse", CgroupPermissions: "rwm"}},
						},
					},
				},
				Config: &container.Config{
					Image:        "postgres:16",
					Hostname:     "db-primary",
					User:         "postgres",
					Env:          []string{"POSTGRES_PASSWORD=secret", "PGDATA=/var/lib/postgresql/data"},
					Cmd:          []string{"postgres", "-c", "shared_buffers=256MB"},
					Entrypoint:   []string{"docker-entrypoint.sh"},
					WorkingDir:   "/var/lib/postgresql",
					ExposedPorts: nat.PortSet{"5432/tcp": struct{}{}},
					StopSignal:   "SIGINT",
					StopTimeout:  &stopTimeout,
					Tty:          true,
					Labels: map[string]string{
						api.ProjectLabel: "demo", api.ServiceLabel: "db", api.ContainerNumberLabel: "1",
						"tier": "data",
					},
					Healthcheck: &container.HealthConfig{
						Test:        []string{"CMD-SHELL", "pg_isready"},
						Interval:    10 * time.Second,
						Timeout:     3 * time.Second,
						StartPeriod: 30 * time.Second,
						Retries:     5,
					},
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"demo_back": {
							Aliases:    []string{"db", "demo-db-1", fixtureContainerID[:12], "primary"},
							IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: "172.28.0.10"},
						},
					},
				},
			},
		}},
	}
}

func TestServiceConfig(t *testing.T) {
	// Arrange
	netKeys := map[string]string{"demo_back": "back"}

	// Act
	cfg := serviceConfig(richService(), netKeys, map[string]bool{"db": true, "cache": true})

	// Assert
	assert.Equal(t, "postgres:16", cfg.Image)
	assert.Equal(t, "postgres", cfg.User)
	assert.Equal(t, "db-primary", cfg.Hostname)
	assert.Equal(t, "/var/lib/postgresql", cfg.WorkingDir)
	assert.Equal(t, types.ShellCommand{"postgres", "-c", "shared_buffers=256MB"}, cfg.Command)
	assert.Equal(t, types.ShellCommand{"docker-entrypoint.sh"}, cfg.Entrypoint)
	assert.Equal(t, "secret", *cfg.Environment["POSTGRES_PASSWORD"])
	assert.Equal(t, types.StringOrNumberList{"5432/tcp"}, cfg.Expose)
	assert.Equal(t, "SIGINT", cfg.StopSignal)
	assert.Equal(t, types.Duration(25*time.Second), *cfg.StopGracePeriod)
	assert.True(t, cfg.Tty)
	assert.Equal(t, types.Labels{"tier": "data"}, cfg.Labels)
	// The name compose generates is not a declaration.
	assert.Empty(t, cfg.ContainerName)

	assert.Equal(t, []types.ServicePortConfig{
		{Mode: portModeIngress, Target: 5432, Protocol: "tcp", Published: "15432", HostIP: "127.0.0.1"},
	}, cfg.Ports)
	assert.Equal(t, "on-failure:3", cfg.Restart)
	// A compose network's name is not a network_mode: the networks: section carries it.
	assert.Empty(t, cfg.NetworkMode)
	assert.Equal(t, &types.LoggingConfig{Driver: "json-file", Options: types.Options{"max-size": "10m"}}, cfg.Logging)
	assert.Equal(t, types.HostsList{"api.local": {"10.0.0.5"}}, cfg.ExtraHosts)
	assert.Equal(t, types.StringList{"/run/lock:size=1m"}, cfg.Tmpfs)
	assert.Equal(t, []string{"SYS_NICE"}, cfg.CapAdd)
	assert.True(t, cfg.Privileged)
	assert.Equal(t, types.UnitBytes(268435456), cfg.ShmSize)
	assert.Equal(t, types.Mapping{"net.core.somaxconn": "1024"}, cfg.Sysctls)

	assert.Equal(t, types.UnitBytes(1073741824), cfg.MemLimit)
	assert.InDelta(t, 1.5, cfg.CPUS, 0.0001)
	assert.Equal(t, int64(512), cfg.PidsLimit)
	assert.Equal(t, map[string]*types.UlimitsConfig{"nofile": {Soft: 1024, Hard: 4096}}, cfg.Ulimits)
	assert.Equal(t, []types.DeviceMapping{{Source: "/dev/fuse", Target: "/dev/fuse", Permissions: "rwm"}}, cfg.Devices)

	require.NotNil(t, cfg.HealthCheck)
	assert.Equal(t, types.HealthCheckTest{"CMD-SHELL", "pg_isready"}, cfg.HealthCheck.Test)
	assert.Equal(t, types.Duration(10*time.Second), *cfg.HealthCheck.Interval)
	assert.Equal(t, uint64(5), *cfg.HealthCheck.Retries)

	// Compose adds the service and container names back itself, and the short id
	// is nobody's declaration.
	require.Contains(t, cfg.Networks, "back")
	assert.Equal(t, []string{"primary"}, cfg.Networks["back"].Aliases)
	assert.Equal(t, "172.28.0.10", cfg.Networks["back"].Ipv4Address)
}

func TestServiceConfig_Volumes(t *testing.T) {
	// Act
	cfg := serviceConfig(richService(), nil, nil)

	// Assert
	require.Len(t, cfg.Volumes, 3)
	assert.Equal(t, types.ServiceVolumeConfig{
		Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data",
	}, cfg.Volumes[0])
	// An anonymous volume renders as a bare target: naming the one `down` orphaned
	// would make compose reuse it.
	assert.Equal(t, types.ServiceVolumeConfig{Type: "volume", Target: "/tmp/scratch"}, cfg.Volumes[1])
	assert.Equal(t, types.ServiceVolumeConfig{
		Type: "bind", Source: "/srv/demo/seed", Target: "/docker-entrypoint-initdb.d", ReadOnly: true,
		Bind: &types.ServiceVolumeBind{CreateHostPath: true},
	}, cfg.Volumes[2])
}

func TestServiceVolume_Options(t *testing.T) {
	tests := []struct {
		name  string
		mount model.ServiceVolume
		want  types.ServiceVolumeConfig
	}{
		{
			name: "a named volume's options",
			mount: model.ServiceVolume{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata",
				Target: "/data", Volume: &model.VolumeMount{
					NoCopy: true, Subpath: "pg", Labels: map[string]string{"tier": "db"},
				}},
			want: types.ServiceVolumeConfig{Type: "volume", Source: "pgdata", Target: "/data",
				Volume: &types.ServiceVolumeVolume{
					NoCopy: true, Subpath: "pg", Labels: types.Mapping{"tier": "db"},
				}},
		},
		{
			name: "a bind's options, the default propagation left out",
			mount: model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host",
				Target: "/etc/cfg", Bind: &model.BindMount{
					SELinux: "z", Propagation: "rprivate", CreateHostPath: true, Recursive: "readonly",
				}},
			want: types.ServiceVolumeConfig{Type: "bind", Source: "/host", Target: "/etc/cfg",
				Bind: &types.ServiceVolumeBind{SELinux: "z", CreateHostPath: true, Recursive: "readonly"}},
		},
		{
			name: "a bind that must not create its host path",
			mount: model.ServiceVolume{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host",
				Target: "/etc/cfg", Bind: &model.BindMount{Propagation: "rshared"}},
			want: types.ServiceVolumeConfig{Type: "bind", Source: "/host", Target: "/etc/cfg",
				Bind: &types.ServiceVolumeBind{Propagation: "rshared"}},
		},
		{
			name: "a tmpfs mount's size and mode",
			mount: model.ServiceVolume{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/run",
				Tmpfs: &model.TmpfsMount{SizeBytes: 1 << 20, Mode: 0o1777}},
			want: types.ServiceVolumeConfig{Type: "tmpfs", Target: "/run",
				Tmpfs: &types.ServiceVolumeTmpfs{Size: 1 << 20, Mode: 0o1777}},
		},
		{
			name: "an image mount's subpath",
			mount: model.ServiceVolume{Type: model.VolumeTypeImage, RawType: "image", Source: "assets:1",
				Target: "/assets", ReadOnly: true, Image: &model.ImageMount{Subpath: "static"}},
			want: types.ServiceVolumeConfig{Type: "image", Source: "assets:1", Target: "/assets",
				ReadOnly: true, Image: &types.ServiceVolumeImage{SubPath: "static"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := serviceVolume(tt.mount)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDeclaredNetworkMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "a compose network is not a mode", mode: "demo_back", want: ""},
		{name: "default bridge", mode: "default", want: ""},
		{name: "host", mode: "host", want: "host"},
		{name: "none", mode: "none", want: "none"},
		{name: "another container", mode: "container:abc", want: "container:abc"},
		{name: "another service", mode: "service:db", want: "service:db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, declaredNetworkMode(tt.mode))
		})
	}
}

func TestDeclaredContainerName(t *testing.T) {
	labels := map[string]string{
		api.ProjectLabel: "demo", api.ServiceLabel: "db", api.ContainerNumberLabel: "1",
	}
	tests := []struct {
		name  string
		given string
		want  string
	}{
		{name: "compose's own name", given: "/demo-db-1", want: ""},
		{name: "compose's legacy separator", given: "/demo_db_1", want: ""},
		{name: "a name the user chose", given: "/primary-db", want: "primary-db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, declaredContainerName(tt.given, labels))
		})
	}
}

func TestRestartPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy container.RestartPolicy
		want   string
	}{
		{name: "unset", policy: container.RestartPolicy{}, want: ""},
		{name: "docker's default is compose's", policy: container.RestartPolicy{Name: "no"}, want: ""},
		{name: "always", policy: container.RestartPolicy{Name: "always"}, want: "always"},
		{
			name:   "on-failure with a bound",
			policy: container.RestartPolicy{Name: "on-failure", MaximumRetryCount: 3},
			want:   "on-failure:3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, restartPolicy(tt.policy))
		})
	}
}
