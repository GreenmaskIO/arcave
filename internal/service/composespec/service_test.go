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
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/go-connections/nat"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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
	res := model.IntrospectionResult{Project: model.Project{
		Name:     "demo",
		Services: []model.Service{richService()},
		Networks: []model.Network{{Key: "back", Name: "demo_back"}},
	}}

	// Act
	cfg := newAssembly(res).serviceConfig(richService())

	// Assert
	assert.Equal(t, "postgres:16", cfg.Image)
	assert.Equal(t, "postgres", cfg.User)
	assert.Equal(t, "db-primary", cfg.Hostname)
	assert.Equal(t, "/var/lib/postgresql", cfg.WorkingDir)
	assert.Equal(t, types.ShellCommand{"postgres", "-c", "shared_buffers=256MB"}, cfg.Command)
	assert.Equal(t, types.ShellCommand{"docker-entrypoint.sh"}, cfg.Entrypoint)
	assert.Equal(t, "secret", *cfg.Environment["POSTGRES_PASSWORD"])
	// Compose exposes a published port itself; expose: carries only what nothing publishes.
	assert.Empty(t, cfg.Expose)
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
	cfg := newAssembly(model.IntrospectionResult{}).serviceConfig(richService())

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

// dbReplicaID is a full container id of the project's db service.
const dbReplicaID = "9a8b7c6d5e4f30211203f4e5d6c7b8a99a8b7c6d5e4f30211203f4e5d6c7b8a9"

func TestDeclaredNetworkMode(t *testing.T) {
	replicas := map[string]string{dbReplicaID: "db"}
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "a compose network is not a mode", mode: "demo_back", want: ""},
		{name: "default", mode: "default", want: "default"},
		{name: "bridge", mode: "bridge", want: "bridge"},
		{name: "host", mode: "host", want: "host"},
		{name: "none", mode: "none", want: "none"},
		{name: "a replica of the project", mode: "container:" + dbReplicaID, want: "service:db"},
		{name: "another container", mode: "container:abc", want: "container:abc"},
		{name: "another service", mode: "service:db", want: "service:db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, declaredNetworkMode(tt.mode, replicas))
		})
	}
}

func TestDeclaredNamespace(t *testing.T) {
	replicas := map[string]string{dbReplicaID: "db"}
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "a replica of the project", mode: "container:" + dbReplicaID, want: "service:db"},
		{name: "a foreign container id", mode: "container:0123456789ab", want: "container:0123456789ab"},
		{name: "a container name", mode: "container:sidecar", want: "container:sidecar"},
		{name: "host", mode: "host", want: "host"},
		{name: "empty", mode: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, declaredNamespace(tt.mode, replicas))
		})
	}
}

func TestDeclaredVolumesFrom(t *testing.T) {
	replicas := map[string]string{dbReplicaID: "db"}
	tests := []struct {
		name    string
		entries []string
		want    []string
	}{
		{name: "none", entries: nil, want: nil},
		{name: "a replica of the project", entries: []string{dbReplicaID}, want: []string{"db"}},
		{name: "a foreign container", entries: []string{"sidecar"}, want: []string{"container:sidecar"}},
		{
			name:    "a mix, modes kept",
			entries: []string{dbReplicaID + ":ro", "sidecar:rw"},
			want:    []string{"db:ro", "container:sidecar:rw"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, declaredVolumesFrom(tt.entries, replicas))
		})
	}
}

// A service reference is what compose wrote as the id of the replica it resolved;
// the spec declares it back, with the dependency the loader would infer.
func TestServiceConfig_SharedNamespaces(t *testing.T) {
	// Arrange
	db := model.Service{Name: "db", Containers: []model.Container{{ID: dbReplicaID}}}
	app := model.Service{Name: "app", Containers: []model.Container{{
		ID: "app1",
		Inspect: container.InspectResponse{
			// The namespace owner's hostname and the image's EXPOSE: the daemon refuses both here.
			Config: &container.Config{
				Image: "postgres:16", Hostname: dbReplicaID[:12],
				ExposedPorts: nat.PortSet{"5432/tcp": struct{}{}},
			},
			ContainerJSONBase: &container.ContainerJSONBase{
				ID: "app1",
				HostConfig: &container.HostConfig{
					NetworkMode: container.NetworkMode("container:" + dbReplicaID),
					IpcMode:     container.IpcMode("container:" + dbReplicaID),
					PidMode:     container.PidMode("container:" + dbReplicaID),
					VolumesFrom: []string{dbReplicaID},
				},
			},
		},
	}}}
	res := model.IntrospectionResult{Project: model.Project{Name: "demo", Services: []model.Service{app, db}}}

	// Act
	cfg := newAssembly(res).serviceConfig(app)

	// Assert
	assert.Equal(t, "service:db", cfg.NetworkMode)
	assert.Equal(t, "service:db", cfg.Ipc)
	assert.Equal(t, "service:db", cfg.Pid)
	assert.Equal(t, []string{"db"}, cfg.VolumesFrom)
	assert.Empty(t, cfg.Hostname)
	assert.Empty(t, cfg.Expose)
	assert.Equal(t, types.DependsOnConfig{
		"db": {Condition: types.ServiceConditionStarted, Restart: true, Required: true},
	}, cfg.DependsOn)
}

func TestServiceConfig_Image(t *testing.T) {
	const (
		upstreamID = model.ImageDigestID("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
		layerID    = model.ImageDigestID("sha256:2222222222222222222222222222222222222222222222222222222222222222")
		layerRef   = "arcave/demo-db-1-postgres-16:sp-1780000002"
	)
	pulled := image.InspectResponse{RepoDigests: []string{"postgres@sha256:dddd"}}
	labelled := func(labels map[string]string) image.InspectResponse {
		return image.InspectResponse{
			Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: labels}},
		}
	}

	tests := []struct {
		name       string
		running    model.ImageDigestID
		ref        string
		images     map[model.ImageDigestID]image.InspectResponse
		want       string
		wantPolicy string
	}{
		{
			name:    "a container on its upstream",
			running: upstreamID, ref: "postgres:16",
			images: map[model.ImageDigestID]image.InspectResponse{upstreamID: pulled},
			want:   "postgres:16", wantPolicy: pullPolicyMissing,
		},
		{
			name:    "a layer names its upstream by label",
			running: layerID, ref: layerRef,
			images: map[model.ImageDigestID]image.InspectResponse{
				layerID: labelled(map[string]string{
					model.BaseImageIDLabel: upstreamID.String(), model.BaseImageRefLabel: "postgres:16",
				}),
				upstreamID: pulled,
			},
			want: "postgres:16", wantPolicy: pullPolicyMissing,
		},
		{
			name:    "a layer carrying no labels keeps its tag",
			running: layerID, ref: layerRef,
			images: map[model.ImageDigestID]image.InspectResponse{layerID: labelled(nil)},
			want:   layerRef, wantPolicy: pullPolicyMissing,
		},
		{
			name:    "an upstream built locally is never pulled",
			running: upstreamID, ref: "app:dev",
			images: map[model.ImageDigestID]image.InspectResponse{upstreamID: {}},
			want:   "app:dev", wantPolicy: pullPolicyNever,
		},
		{
			name:    "an image compose built is never pulled, though the containerd store digests it",
			running: upstreamID, ref: "demo-app",
			images: map[model.ImageDigestID]image.InspectResponse{upstreamID: {
				RepoDigests: []string{"demo-app@sha256:eeee"},
				Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{
					Labels: map[string]string{api.ProjectLabel: "demo"},
				}},
			}},
			want: "demo-app", wantPolicy: pullPolicyNever,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := model.Service{Name: "db", Containers: []model.Container{{
				ID: "c1",
				Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{ID: "c1", Image: tt.running.String()},
					Config:            &container.Config{Image: tt.ref},
				},
			}}}
			res := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{svc}},
				Images:  tt.images,
			}

			// Act
			cfg := newAssembly(res).serviceConfig(svc)

			// Assert
			assert.Equal(t, tt.want, cfg.Image)
			assert.Equal(t, tt.wantPolicy, cfg.PullPolicy)
		})
	}
}

func TestServiceConfig_DaemonDefaults(t *testing.T) {
	stopTimeout := func(seconds int) *int { return &seconds }
	grace := func(d time.Duration) *types.Duration { converted := types.Duration(d); return &converted }

	tests := []struct {
		name   string
		daemon system.Info
		host   container.HostConfig
		stop   *int
		verify func(t *testing.T, cfg types.ServiceConfig)
	}{
		{
			name:   "the daemon's runtime, log driver and shm size are left out",
			daemon: system.Info{DefaultRuntime: "runc", LoggingDriver: "json-file"},
			host: container.HostConfig{Runtime: "runc", ShmSize: defaultShmSize,
				LogConfig: container.LogConfig{Type: "json-file"}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Empty(t, cfg.Runtime)
				assert.Nil(t, cfg.Logging)
				assert.Zero(t, cfg.ShmSize)
			},
		},
		{
			name:   "what differs from the daemon's is declared",
			daemon: system.Info{DefaultRuntime: "runc", LoggingDriver: "json-file"},
			host: container.HostConfig{Runtime: "runsc", ShmSize: 128 << 20,
				LogConfig: container.LogConfig{Type: "local"}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, "runsc", cfg.Runtime)
				assert.Equal(t, &types.LoggingConfig{Driver: "local"}, cfg.Logging)
				assert.Equal(t, types.UnitBytes(128<<20), cfg.ShmSize)
			},
		},
		{
			name:   "the daemon's log driver with options is declared",
			daemon: system.Info{LoggingDriver: "json-file"},
			host: container.HostConfig{LogConfig: container.LogConfig{
				Type: "json-file", Config: map[string]string{"max-size": "10m"},
			}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, &types.LoggingConfig{Driver: "json-file", Options: types.Options{"max-size": "10m"}}, cfg.Logging)
			},
		},
		{
			name:   "Docker Desktop's stop timeout is left out",
			daemon: system.Info{OperatingSystem: desktopOperatingSystem},
			stop:   stopTimeout(desktopStopTimeout),
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Nil(t, cfg.StopGracePeriod)
			},
		},
		{
			name:   "one second elsewhere is a declaration",
			daemon: system.Info{OperatingSystem: "Ubuntu 24.04 LTS"},
			stop:   stopTimeout(1),
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, grace(time.Second), cfg.StopGracePeriod)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			host := tt.host
			svc := model.Service{Name: "db", Containers: []model.Container{{
				ID: "c1",
				Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{ID: "c1", HostConfig: &host},
					Config:            &container.Config{Image: "postgres:16", StopTimeout: tt.stop},
				},
			}}}
			res := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{svc}},
				Daemon:  tt.daemon,
			}

			// Act
			cfg := newAssembly(res).serviceConfig(svc)

			// Assert
			tt.verify(t, cfg)
		})
	}
}

// What the daemon or compose added past the declaration is left out of the spec.
func TestServiceConfig_DaemonAdditions(t *testing.T) {
	tests := []struct {
		name   string
		host   container.HostConfig
		config container.Config
		verify func(t *testing.T, cfg types.ServiceConfig)
	}{
		{
			name: "the swap the daemon gives a memory limit declared without one",
			host: container.HostConfig{Resources: container.Resources{Memory: 64 << 20, MemorySwap: 128 << 20}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, types.UnitBytes(64<<20), cfg.MemLimit)
				assert.Zero(t, cfg.MemSwapLimit)
			},
		},
		{
			name: "a swap limit of its own is declared",
			host: container.HostConfig{Resources: container.Resources{Memory: 64 << 20, MemorySwap: 256 << 20}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, types.UnitBytes(256<<20), cfg.MemSwapLimit)
			},
		},
		{
			name: "the label=disable the daemon adds beside the host's pid namespace",
			host: container.HostConfig{PidMode: "host", SecurityOpt: []string{labelDisable}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, "host", cfg.Pid)
				assert.Nil(t, cfg.SecurityOpt)
			},
		},
		{
			name: "a container sharing nothing of the host keeps label=disable",
			host: container.HostConfig{SecurityOpt: []string{labelDisable, "no-new-privileges:true"}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, []string{labelDisable, "no-new-privileges:true"}, cfg.SecurityOpt)
			},
		},
		{
			name:   "the host's hostname in the host's network",
			host:   container.HostConfig{NetworkMode: "host"},
			config: container.Config{Hostname: "docker-desktop"},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, "host", cfg.NetworkMode)
				assert.Empty(t, cfg.Hostname)
			},
		},
		{
			name:   "a hostname of its own in the host's network",
			host:   container.HostConfig{NetworkMode: "host"},
			config: container.Config{Hostname: "edge"},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, "edge", cfg.Hostname)
			},
		},
		{
			name: "a port compose exposed because it is published",
			host: container.HostConfig{PortBindings: nat.PortMap{
				"80/tcp": []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "8080"}},
			}},
			config: container.Config{ExposedPorts: nat.PortSet{"80/tcp": {}, "80/udp": {}, "9090/tcp": {}}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, types.StringOrNumberList{"80/udp", "9090/tcp"}, cfg.Expose)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			host, config := tt.host, tt.config
			svc := model.Service{Name: "app", Containers: []model.Container{{
				ID: "c1",
				Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{ID: "c1", HostConfig: &host},
					Config:            &config,
				},
			}}}
			res := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{svc}},
				Daemon:  system.Info{Name: "docker-desktop"},
			}

			// Act
			cfg := newAssembly(res).serviceConfig(svc)

			// Assert
			tt.verify(t, cfg)
		})
	}
}

func TestServiceConfig_NetworkAttachment(t *testing.T) {
	tests := []struct {
		name     string
		endpoint network.EndpointSettings
		want     *types.ServiceNetworkConfig
	}{
		{
			name:     "an endpoint created without an IPAM config is a nil entry",
			endpoint: network.EndpointSettings{},
			want:     nil,
		},
		{
			name:     "an endpoint created with an empty IPAM config is an empty entry",
			endpoint: network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{}},
			want:     &types.ServiceNetworkConfig{},
		},
		{
			name:     "an alias is declared",
			endpoint: network.EndpointSettings{Aliases: []string{"api"}},
			want:     &types.ServiceNetworkConfig{Aliases: []string{"api"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			endpoint := tt.endpoint
			svc := model.Service{Name: "app", Containers: []model.Container{{
				ID: "c1",
				Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{ID: "c1", HostConfig: &container.HostConfig{}},
					Config:            &container.Config{},
					NetworkSettings: &container.NetworkSettings{
						Networks: map[string]*network.EndpointSettings{"demo_default": &endpoint},
					},
				},
			}}}
			res := model.IntrospectionResult{Project: model.Project{
				Name:     "demo",
				Services: []model.Service{svc},
				Networks: []model.Network{{Key: "default", Name: "demo_default"}},
			}}

			// Act
			cfg := newAssembly(res).serviceConfig(svc)

			// Assert
			require.Contains(t, cfg.Networks, "default")
			assert.Equal(t, tt.want, cfg.Networks["default"])
		})
	}
}

func TestServiceConfig_VolumesFrom(t *testing.T) {
	tests := []struct {
		name        string
		volumesFrom []string
		host        container.HostConfig
		config      container.Config
		want        []string
	}{
		{
			name:        "a mount inherited through volumes_from is not the service's own",
			volumesFrom: []string{"db-id"},
			want:        nil,
		},
		{
			name:        "a mount the container asked for itself stays",
			volumesFrom: []string{"db-id"},
			host:        container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Target: "/cache"}}},
			want:        []string{"/cache"},
		},
		{
			name:        "an image VOLUME stays",
			volumesFrom: []string{"db-id"},
			config:      container.Config{Volumes: map[string]struct{}{"/cache": {}}},
			want:        []string{"/cache"},
		},
		{
			name: "without volumes_from every mount is the service's own",
			want: []string{"/cache"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			host, config := tt.host, tt.config
			host.VolumesFrom = tt.volumesFrom
			svc := model.Service{
				Name:    "app",
				Volumes: []model.ServiceVolume{{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/cache"}},
				Containers: []model.Container{{
					ID: "c1",
					Inspect: container.InspectResponse{
						ContainerJSONBase: &container.ContainerJSONBase{ID: "c1", HostConfig: &host},
						Config:            &config,
					},
				}},
			}
			res := model.IntrospectionResult{Project: model.Project{Name: "demo", Services: []model.Service{svc}}}

			// Act
			cfg := newAssembly(res).serviceConfig(svc)

			// Assert
			var targets []string
			for _, v := range cfg.Volumes {
				targets = append(targets, v.Target)
			}
			assert.Equal(t, tt.want, targets)
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
