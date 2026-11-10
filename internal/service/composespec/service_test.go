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
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
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
			{Type: model.VolumeTypeAnon, RawType: "volume", VolumeName: "9f8e7d", Target: "/tmp/scratch", Replica: 1},
			{Type: model.VolumeTypeAnon, RawType: "volume", VolumeName: "1a2b3c", Target: "/tmp/scratch", Replica: 2},
			{Type: model.VolumeTypeBind, RawType: "bind", Source: "/srv/demo/seed", Target: "/docker-entrypoint-initdb.d", ReadOnly: true,
				Bind: &model.BindMount{Propagation: "rprivate", CreateHostPath: true}},
		},
		Containers: []model.Container{{
			ID:     fixtureContainerID,
			Name:   "demo-db-1",
			Number: 1,
			Inspect: container.InspectResponse{
				ID:   fixtureContainerID,
				Name: "/demo-db-1",
				HostConfig: &container.HostConfig{
					PortBindings: network.PortMap{
						network.MustParsePort("5432/tcp"): []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "15432"}},
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
				Config: &container.Config{
					Image:        "postgres:16",
					Hostname:     "db-primary",
					User:         "postgres",
					Env:          []string{"POSTGRES_PASSWORD=secret", "PGDATA=/var/lib/postgresql/data"},
					Cmd:          []string{"postgres", "-c", "shared_buffers=256MB"},
					Entrypoint:   []string{"docker-entrypoint.sh"},
					WorkingDir:   "/var/lib/postgresql",
					ExposedPorts: network.PortSet{network.MustParsePort("5432/tcp"): struct{}{}},
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
							IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.28.0.10")},
						},
					},
				},
			},
		}},
	}
}

func TestNewServiceConfig(t *testing.T) {
	// Arrange
	introspection := model.IntrospectionResult{Project: model.Project{
		Name:     "demo",
		Services: []model.Service{richService()},
		Networks: []model.Network{{Key: "back", Name: "demo_back"}},
	}}

	// Act
	cfg := newAssembly(introspection, NewDockerStamps(system.Info{})).newServiceConfig(richService())

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
		{Target: 5432, Protocol: "tcp", Published: "15432", HostIP: "127.0.0.1"},
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

func TestNewServiceConfig_Volumes(t *testing.T) {
	// Act
	cfg := newAssembly(model.IntrospectionResult{}, NewDockerStamps(system.Info{})).newServiceConfig(richService())

	// Assert
	require.Len(t, cfg.Volumes, 3)
	assert.Equal(t, types.ServiceVolumeConfig{
		Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data",
	}, cfg.Volumes[0])
	// An anonymous volume renders as a bare target, once for all replicas: naming the
	// one `down` orphaned would make compose reuse it.
	assert.Equal(t, types.ServiceVolumeConfig{Type: "volume", Target: "/tmp/scratch"}, cfg.Volumes[1])
	assert.Equal(t, types.ServiceVolumeConfig{
		Type: "bind", Source: "/srv/demo/seed", Target: "/docker-entrypoint-initdb.d", ReadOnly: true,
		Bind: &types.ServiceVolumeBind{CreateHostPath: true},
	}, cfg.Volumes[2])
}

func TestNewServiceVolume_Options(t *testing.T) {
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
			got := newServiceVolume(tt.mount)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// Full container ids: a replica of the project's db service, and a container outside the project.
const (
	dbReplicaID = "9a8b7c6d5e4f30211203f4e5d6c7b8a99a8b7c6d5e4f30211203f4e5d6c7b8a9"
	vpnID       = "5e4f30211203f4e5d6c7b8a99a8b7c6d5e4f30211203f4e5d6c7b8a99a8b7c6d"
)

// Podman answers bridge for a container in a compose network: the spec declares
// the network, not the mode, and a bridge with no project network stays. Only on
// Podman is a network named podman the daemon's own.
func TestNewServiceConfig_PodmanBridgeMode(t *testing.T) {
	tests := []struct {
		name         string
		stamps       EngineStamps
		networks     map[string]*network.EndpointSettings
		wantMode     string
		wantNetworks []string
	}{
		{
			name:         "beside a project network",
			stamps:       NewPodmanStamps(system.Info{}),
			networks:     map[string]*network.EndpointSettings{"demo_back": {}},
			wantNetworks: []string{"back"},
		},
		{
			name:     "with no project network",
			stamps:   NewPodmanStamps(system.Info{}),
			networks: map[string]*network.EndpointSettings{"podman": {}},
			wantMode: "bridge",
		},
		{
			name:     "bridge beside a network on Docker, which reports the mode as declared",
			stamps:   NewDockerStamps(system.Info{}),
			networks: map[string]*network.EndpointSettings{"demo_back": {}},
			wantMode: "bridge",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			service := richService()
			service.Containers[0].Inspect.HostConfig.NetworkMode = "bridge"
			service.Containers[0].Inspect.NetworkSettings.Networks = tt.networks
			introspection := model.IntrospectionResult{
				Project: model.Project{
					Name:     "demo",
					Services: []model.Service{service},
					Networks: []model.Network{{Key: "back", Name: "demo_back"}},
				},
			}

			// Act
			cfg := newAssembly(introspection, tt.stamps).newServiceConfig(service)

			// Assert
			assert.Equal(t, tt.wantMode, cfg.NetworkMode)
			assert.ElementsMatch(t, tt.wantNetworks, slices.Collect(maps.Keys(cfg.Networks)))
		})
	}
}

func TestAssembly_declaredNetworkMode(t *testing.T) {
	var (
		projectNetwork = map[string]*network.EndpointSettings{"demo_back": {}}
		otherNetwork   = map[string]*network.EndpointSettings{"bridge": {}}
	)
	tests := []struct {
		name string
		mode string
		// networks is what the container joins; nil joins none.
		networks map[string]*network.EndpointSettings
		// docker is an engine that never answers bridge beside a project network.
		docker bool
		want   string
	}{
		{name: "a compose network is not a mode", mode: "demo_back", networks: projectNetwork, want: ""},
		{name: "default", mode: "default", networks: otherNetwork, want: "default"},
		{name: "bridge", mode: "bridge", networks: otherNetwork, want: "bridge"},
		{name: "host", mode: "host", networks: otherNetwork, want: "host"},
		{name: "none", mode: "none", networks: otherNetwork, want: "none"},
		{name: "bridge beside a project network, as Podman answers", mode: "bridge", networks: projectNetwork, want: ""},
		{name: "default beside a project network", mode: "default", networks: projectNetwork, want: ""},
		{name: "bridge beside a project network, as Docker answers", mode: "bridge", networks: projectNetwork, docker: true, want: "bridge"},
		{name: "host beside a project network stays", mode: "host", networks: projectNetwork, want: "host"},
		{name: "a replica of the project", mode: "container:" + dbReplicaID, networks: otherNetwork, want: "service:db"},
		{name: "another container", mode: "container:abc", networks: otherNetwork, want: "container:abc"},
		{name: "another service", mode: "service:db", networks: otherNetwork, want: "service:db"},
		{name: "Podman's pasta, joining no network", mode: "pasta", want: "pasta"},
		{name: "a namespace path, joining no network", mode: "ns:/run/netns/x", want: "ns:/run/netns/x"},
		{name: "no mode at all", mode: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			a := assembly{
				serviceByReplica: map[string]string{dbReplicaID: "db"},
				networkKeys:      map[string]string{"demo_back": "back"},
				engineStamps:     EngineStamps{Networking: EngineNetworking{BridgeModeBesideNetworks: !tt.docker}},
			}
			inspect := container.InspectResponse{NetworkSettings: &container.NetworkSettings{
				Networks: tt.networks,
			}}

			// Act
			got := a.declaredNetworkMode(tt.mode, inspect)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDeclaredNamespace(t *testing.T) {
	replicas := map[string]string{dbReplicaID: "db"}
	foreign := map[string]string{vpnID: "vpn", "vpn": "vpn"}
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "a replica of the project", mode: "container:" + dbReplicaID, want: "service:db"},
		{name: "a foreign container id, named", mode: "container:" + vpnID, want: "container:vpn"},
		{name: "a foreign container name", mode: "container:vpn", want: "container:vpn"},
		{name: "a foreign container id, gone", mode: "container:0123456789ab", want: "container:0123456789ab"},
		{name: "a container name", mode: "container:sidecar", want: "container:sidecar"},
		{name: "host", mode: "host", want: "host"},
		{name: "empty", mode: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, declaredNamespace(tt.mode, replicas, foreign))
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
func TestNewServiceConfig_SharedNamespaces(t *testing.T) {
	// Arrange
	db := model.Service{Name: "db", Containers: []model.Container{{ID: dbReplicaID}}}
	app := model.Service{Name: "app", Containers: []model.Container{{
		ID: "app1",
		Inspect: container.InspectResponse{
			// The namespace owner's hostname and the image's EXPOSE: the daemon refuses both here.
			Config: &container.Config{
				Image: "postgres:16", Hostname: dbReplicaID[:12],
				ExposedPorts: network.PortSet{network.MustParsePort("5432/tcp"): struct{}{}},
			},
			ID: "app1",
			HostConfig: &container.HostConfig{
				NetworkMode: container.NetworkMode("container:" + dbReplicaID),
				IpcMode:     container.IpcMode("container:" + dbReplicaID),
				PidMode:     container.PidMode("container:" + dbReplicaID),
				VolumesFrom: []string{dbReplicaID},
			},
		},
	}}}
	introspection := model.IntrospectionResult{Project: model.Project{Name: "demo", Services: []model.Service{app, db}}}

	// Act
	cfg := newAssembly(introspection, NewDockerStamps(system.Info{})).newServiceConfig(app)

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

func TestNewServiceConfig_Image(t *testing.T) {
	const (
		upstreamID = model.ImageDigestID("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
		layerID    = model.ImageDigestID("sha256:2222222222222222222222222222222222222222222222222222222222222222")
		layerRef   = "arcave/demo-db-1-postgres-16:sp-019e7044-8fd0-7000-8000-000000000000"
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
					string(model.SystemLabelBaseImageID): upstreamID.String(), string(model.SystemLabelBaseImageRef): "postgres:16",
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
			name:    "an image compose built under a bare name loses the :latest the engine tagged it with",
			running: upstreamID, ref: "demo-app:latest",
			images: map[model.ImageDigestID]image.InspectResponse{upstreamID: labelled(map[string]string{api.ProjectLabel: "demo"})},
			want:   "demo-app", wantPolicy: pullPolicyNever,
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
					ID: "c1", Image: tt.running.String(),
					Config: &container.Config{Image: tt.ref},
				},
			}}}
			introspection := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{svc}},
				Images:  tt.images,
			}

			// Act
			cfg := newAssembly(introspection, NewDockerStamps(system.Info{})).newServiceConfig(svc)

			// Assert
			assert.Equal(t, tt.want, cfg.Image)
			assert.Equal(t, tt.wantPolicy, cfg.PullPolicy)
		})
	}
}

func TestNewServiceConfig_DaemonDefaults(t *testing.T) {
	stopTimeout := func(seconds int) *int { return &seconds }
	grace := func(d time.Duration) *types.Duration { converted := types.Duration(d); return &converted }

	tests := []struct {
		name   string
		info   system.Info
		host   container.HostConfig
		stop   *int
		verify func(t *testing.T, cfg types.ServiceConfig)
	}{
		{
			name: "the daemon's runtime, log driver and shm size are left out",
			info: system.Info{DefaultRuntime: "runc", LoggingDriver: "json-file"},
			host: container.HostConfig{Runtime: "runc", ShmSize: NewDockerStamps(system.Info{}).ShmSize,
				LogConfig: container.LogConfig{Type: "json-file"}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Empty(t, cfg.Runtime)
				assert.Nil(t, cfg.Logging)
				assert.Zero(t, cfg.ShmSize)
			},
		},
		{
			name: "what differs from the daemon's is declared",
			info: system.Info{DefaultRuntime: "runc", LoggingDriver: "json-file"},
			host: container.HostConfig{Runtime: "runsc", ShmSize: 128 << 20,
				LogConfig: container.LogConfig{Type: "local"}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, "runsc", cfg.Runtime)
				assert.Equal(t, &types.LoggingConfig{Driver: "local"}, cfg.Logging)
				assert.Equal(t, types.UnitBytes(128<<20), cfg.ShmSize)
			},
		},
		{
			name: "the daemon's log driver with options is declared",
			info: system.Info{LoggingDriver: "json-file"},
			host: container.HostConfig{LogConfig: container.LogConfig{
				Type: "json-file", Config: map[string]string{"max-size": "10m"},
			}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, &types.LoggingConfig{Driver: "json-file", Options: types.Options{"max-size": "10m"}}, cfg.Logging)
			},
		},
		{
			name: "Docker Desktop's stop timeout is left out",
			info: system.Info{OperatingSystem: "Docker Desktop"},
			stop: stopTimeout(desktopStopTimeout),
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Nil(t, cfg.StopGracePeriod)
			},
		},
		{
			name: "one second elsewhere is a declaration",
			info: system.Info{OperatingSystem: "Ubuntu 24.04 LTS"},
			stop: stopTimeout(1),
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
					ID: "c1", HostConfig: &host,
					Config: &container.Config{Image: "postgres:16", StopTimeout: tt.stop},
				},
			}}}
			introspection := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{svc}},
			}

			// Act
			cfg := newAssembly(introspection, NewDockerStamps(tt.info)).newServiceConfig(svc)

			// Assert
			tt.verify(t, cfg)
		})
	}
}

// What the daemon or compose added past the declaration is left out of the spec.
func TestNewServiceConfig_DaemonAdditions(t *testing.T) {
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
			name: "the label=disable the daemon adds beside a privileged container",
			host: container.HostConfig{Privileged: true, SecurityOpt: []string{labelDisable}},
			verify: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Nil(t, cfg.SecurityOpt)
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
			host: container.HostConfig{PortBindings: network.PortMap{
				network.MustParsePort("80/tcp"): []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "8080"}},
			}},
			config: container.Config{ExposedPorts: network.PortSet{network.MustParsePort("80/tcp"): {}, network.MustParsePort("80/udp"): {}, network.MustParsePort("9090/tcp"): {}}},
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
					ID: "c1", HostConfig: &host,
					Config: &config,
				},
			}}}
			introspection := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{svc}},
			}
			stamps := NewDockerStamps(system.Info{Name: "docker-desktop"})

			// Act
			cfg := newAssembly(introspection, stamps).newServiceConfig(svc)

			// Assert
			tt.verify(t, cfg)
		})
	}
}

func TestNewServiceConfig_NetworkAttachment(t *testing.T) {
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
		{
			name: "static addresses are spelled",
			endpoint: network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{
				IPv4Address:  netip.MustParseAddr("172.28.0.10"),
				IPv6Address:  netip.MustParseAddr("fd00::10"),
				LinkLocalIPs: []netip.Addr{netip.MustParseAddr("169.254.0.5")},
			}},
			want: &types.ServiceNetworkConfig{
				Ipv4Address:  "172.28.0.10",
				Ipv6Address:  "fd00::10",
				LinkLocalIPs: []string{"169.254.0.5"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			endpoint := tt.endpoint
			svc := model.Service{Name: "app", Containers: []model.Container{{
				ID: "c1",
				Inspect: container.InspectResponse{
					ID: "c1", HostConfig: &container.HostConfig{},
					Config: &container.Config{},
					NetworkSettings: &container.NetworkSettings{
						Networks: map[string]*network.EndpointSettings{"demo_default": &endpoint},
					},
				},
			}}}
			introspection := model.IntrospectionResult{Project: model.Project{
				Name:     "demo",
				Services: []model.Service{svc},
				Networks: []model.Network{{Key: "default", Name: "demo_default"}},
			}}

			// Act
			cfg := newAssembly(introspection, NewDockerStamps(system.Info{})).newServiceConfig(svc)

			// Assert
			require.Contains(t, cfg.Networks, "default")
			assert.Equal(t, tt.want, cfg.Networks["default"])
		})
	}
}

func TestNewServiceConfig_DNS(t *testing.T) {
	tests := []struct {
		name string
		dns  []netip.Addr
		want types.StringList
	}{
		{name: "none", dns: nil, want: nil},
		{name: "an unset address is none", dns: []netip.Addr{{}}, want: nil},
		{
			name: "each server is spelled",
			dns:  []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2001:4860:4860::8888")},
			want: types.StringList{"8.8.8.8", "2001:4860:4860::8888"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := model.Service{Name: "app", Containers: []model.Container{{
				ID: "c1",
				Inspect: container.InspectResponse{
					ID: "c1", HostConfig: &container.HostConfig{DNS: tt.dns},
					Config: &container.Config{},
				},
			}}}
			introspection := model.IntrospectionResult{
				Project: model.Project{Name: "demo", Services: []model.Service{svc}},
			}

			// Act
			cfg := newAssembly(introspection, NewDockerStamps(system.Info{})).newServiceConfig(svc)

			// Assert
			assert.Equal(t, tt.want, cfg.DNS)
		})
	}
}

func TestNewServiceConfig_VolumesFrom(t *testing.T) {
	tests := []struct {
		name          string
		inheritedFrom string
		want          []string
	}{
		{
			name:          "a mount inherited through volumes_from is not the service's own",
			inheritedFrom: "db-id",
			want:          nil,
		},
		{
			name: "a mount the container asked for itself stays",
			want: []string{"/cache"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := model.Service{
				Name: "app",
				Volumes: []model.ServiceVolume{{
					Type: model.VolumeTypeAnon, RawType: "volume", Target: "/cache", Replica: 1,
					InheritedFrom: tt.inheritedFrom,
				}},
				Containers: []model.Container{{
					ID:     "c1",
					Number: 1,
					Inspect: container.InspectResponse{
						ID: "c1", HostConfig: &container.HostConfig{VolumesFrom: []string{"db-id"}},
						Config: &container.Config{},
					},
				}},
			}
			introspection := model.IntrospectionResult{Project: model.Project{Name: "demo", Services: []model.Service{svc}}}

			// Act
			cfg := newAssembly(introspection, NewDockerStamps(system.Info{})).newServiceConfig(svc)

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

func TestToRestartPolicy(t *testing.T) {
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
			assert.Equal(t, tt.want, toRestartPolicy(tt.policy))
		})
	}
}

// Podman aliases a container by its hostname itself: there the hostname is not declared,
// or a rebuild would carry it twice. Docker adds no such alias, so one there is the user's.
func TestNewServiceConfig_HostnameAlias(t *testing.T) {
	tests := []struct {
		name        string
		stamps      EngineStamps
		wantAliases []string
	}{
		{name: "docker", stamps: NewDockerStamps(system.Info{}), wantAliases: []string{"db-primary", "primary"}},
		{name: "podman", stamps: NewPodmanStamps(system.Info{}), wantAliases: []string{"primary"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			service := richService()
			endpoint := service.Containers[0].Inspect.NetworkSettings.Networks["demo_back"]
			endpoint.Aliases = append(endpoint.Aliases, "db-primary")
			introspection := model.IntrospectionResult{
				Project: model.Project{
					Name:     "demo",
					Services: []model.Service{service},
					Networks: []model.Network{{Key: "back", Name: "demo_back"}},
				},
			}

			// Act
			cfg := newAssembly(introspection, tt.stamps).newServiceConfig(service)

			// Assert
			require.Contains(t, cfg.Networks, "back")
			assert.Equal(t, tt.wantAliases, cfg.Networks["back"].Aliases)
		})
	}
}

// A value equal to Podman's default stays when the image sets its own: the compose
// file asked for it over the image's.
func TestNewServiceConfig_PodmanDefaultOverImage(t *testing.T) {
	tests := []struct {
		name            string
		image           dockerspec.DockerOCIImageConfig
		wantStopSignal  string
		wantTimeoutKept bool
	}{
		{
			name: "the image sets none",
		},
		{
			name: "the image sets its own",
			image: dockerspec.DockerOCIImageConfig{
				ImageConfig: ocispec.ImageConfig{StopSignal: "SIGINT"},
				Healthcheck: &dockerspec.HealthcheckConfig{Timeout: 5 * time.Second},
			},
			wantStopSignal:  "SIGTERM",
			wantTimeoutKept: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			service := richService()
			inspect := &service.Containers[0].Inspect
			inspect.Config.StopSignal = "15"
			inspect.Config.Healthcheck.Timeout = 30 * time.Second
			inspect.Image = "sha256:" + strings.Repeat("c", 64)
			introspection := model.IntrospectionResult{
				Project: model.Project{
					Name:     "demo",
					Services: []model.Service{service},
					Networks: []model.Network{{Key: "back", Name: "demo_back"}},
				},
				Images: map[model.ImageDigestID]image.InspectResponse{
					model.ImageDigestID(inspect.Image): {ID: inspect.Image, Config: &tt.image},
				},
			}

			// Act
			cfg := newAssembly(introspection, NewPodmanStamps(system.Info{})).newServiceConfig(service)

			// Assert
			assert.Equal(t, tt.wantStopSignal, cfg.StopSignal)
			require.NotNil(t, cfg.HealthCheck)
			assert.Equal(t, tt.wantTimeoutKept, cfg.HealthCheck.Timeout != nil)
		})
	}
}

// What Podman stamps on every container is its own default and leaves the spec; the
// same values on Docker were declared and stay.
func TestNewServiceConfig_PodmanDefaults(t *testing.T) {
	grace := func(d time.Duration) *types.Duration { converted := types.Duration(d); return &converted }
	tests := []struct {
		name   string
		stamps EngineStamps
		want   func(t *testing.T, cfg types.ServiceConfig)
	}{
		{
			name:   "podman",
			stamps: NewPodmanStamps(system.Info{}),
			want: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, types.NewMappingWithEquals([]string{"PATH=/bin", "MODE=prod"}), cfg.Environment)
				assert.Empty(t, cfg.WorkingDir)
				assert.Empty(t, cfg.StopSignal)
				assert.Nil(t, cfg.StopGracePeriod)
				assert.Empty(t, cfg.Runtime)
				assert.Empty(t, cfg.Pid)
				assert.Empty(t, cfg.Uts)
				assert.Empty(t, cfg.Ipc)
				assert.Zero(t, cfg.ShmSize)
				assert.Empty(t, cfg.CgroupParent)
				assert.Nil(t, cfg.Logging)
				assert.Equal(t, map[string]*types.UlimitsConfig{"nofile": {Soft: 1024, Hard: 2048}}, cfg.Ulimits)
			},
		},
		{
			name:   "docker",
			stamps: NewDockerStamps(system.Info{}),
			want: func(t *testing.T, cfg types.ServiceConfig) {
				assert.Equal(t, types.NewMappingWithEquals([]string{
					"PATH=/bin", "MODE=prod", "container=podman", "HOME=/root", "HOSTNAME=db-primary",
				}), cfg.Environment)
				assert.Equal(t, "/", cfg.WorkingDir)
				assert.Equal(t, "SIGTERM", cfg.StopSignal)
				assert.Equal(t, grace(10*time.Second), cfg.StopGracePeriod)
				assert.Equal(t, "private", cfg.Pid)
				assert.Equal(t, "private", cfg.Uts)
				assert.Equal(t, types.UnitBytes(65536000), cfg.ShmSize)
				assert.Equal(t, map[string]*types.UlimitsConfig{"nofile": {Soft: 1024, Hard: 2048}}, cfg.Ulimits)
				assert.Equal(t, "oci", cfg.Runtime)
				assert.Equal(t, "shareable", cfg.Ipc)
				assert.Equal(t, "user.slice", cfg.CgroupParent)
				assert.Equal(t, &types.LoggingConfig{Driver: "journald"}, cfg.Logging)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			stopTimeout := 10
			service := richService()
			inspect := &service.Containers[0].Inspect
			inspect.Config.Env = []string{"PATH=/bin", "MODE=prod", "container=podman", "HOME=/root", "HOSTNAME=db-primary"}
			inspect.Config.WorkingDir = "/"
			inspect.Config.StopSignal = "15"
			inspect.Config.StopTimeout = &stopTimeout
			inspect.Image = "sha256:" + strings.Repeat("c", 64)
			host := inspect.HostConfig
			host.Runtime, host.PidMode, host.UTSMode, host.IpcMode = "oci", "private", "private", "shareable"
			host.ShmSize = 65536000
			host.CgroupParent = "user.slice"
			host.LogConfig = container.LogConfig{Type: "journald"}
			host.Ulimits = []*container.Ulimit{{Name: "RLIMIT_NOFILE", Soft: 1024, Hard: 2048}}
			introspection := model.IntrospectionResult{
				Project: model.Project{
					Name:     "demo",
					Services: []model.Service{service},
					Networks: []model.Network{{Key: "back", Name: "demo_back"}},
				},
				Images: map[model.ImageDigestID]image.InspectResponse{
					model.ImageDigestID(inspect.Image): {
						ID:     inspect.Image,
						Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Env: []string{"PATH=/bin"}}},
					},
				},
			}

			// Act
			cfg := newAssembly(introspection, tt.stamps).newServiceConfig(service)

			// Assert
			tt.want(t, cfg)
		})
	}
}
