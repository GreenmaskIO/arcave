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
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestNewDockerStamps(t *testing.T) {
	tests := []struct {
		name string
		info system.Info
		want func(t *testing.T, got EngineStamps)
	}{
		{
			name: "the runtime, log driver and host name are docker info's",
			info: system.Info{Name: "build-1", DefaultRuntime: "runc", LoggingDriver: "json-file", OperatingSystem: "Ubuntu 24.04 LTS"},
			want: func(t *testing.T, got EngineStamps) {
				assert.Equal(t, []string{"runc"}, got.Runtimes)
				assert.Equal(t, []string{"json-file"}, got.LogDrivers)
				assert.Equal(t, "build-1", got.HostName)
				assert.Zero(t, got.StopTimeout)
				assert.Equal(t, int64(64<<20), got.ShmSize)
			},
		},
		{
			name: "docker desktop stamps a one second stop timeout",
			info: system.Info{OperatingSystem: desktopOperatingSystem},
			want: func(t *testing.T, got EngineStamps) {
				assert.Equal(t, desktopStopTimeout, got.StopTimeout)
				assert.Nil(t, got.Runtimes, "no runtime docker info names none")
				assert.Nil(t, got.LogDrivers, "no log driver docker info names none")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewDockerStamps(tt.info)

			// Assert
			tt.want(t, got)
		})
	}
}

func TestNewPodmanStamps(t *testing.T) {
	tests := []struct {
		name string
		info system.Info
		want func(t *testing.T, got EngineStamps)
	}{
		{
			name: "podman keeps its own and adds what docker info names",
			info: system.Info{Name: "podman-host", DefaultRuntime: "crun", LoggingDriver: "journald"},
			want: func(t *testing.T, got EngineStamps) {
				assert.Equal(t, []string{"oci", "crun"}, got.Runtimes)
				assert.Equal(t, []string{"journald", "k8s-file", "journald"}, got.LogDrivers)
				assert.Equal(t, "podman-host", got.HostName)
				assert.Equal(t, 10, got.StopTimeout)
				assert.True(t, got.Networking.HostnameAliased)
			},
		},
		{
			name: "docker info naming nothing leaves podman's own",
			info: system.Info{},
			want: func(t *testing.T, got EngineStamps) {
				assert.Equal(t, []string{"oci"}, got.Runtimes)
				assert.Equal(t, []string{"journald", "k8s-file"}, got.LogDrivers)
				assert.Empty(t, got.HostName)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewPodmanStamps(tt.info)

			// Assert
			tt.want(t, got)
		})
	}
}

func TestAssembly_dropEngineStamps(t *testing.T) {
	grace := func(d time.Duration) *types.Duration { converted := types.Duration(d); return &converted }
	env := func(pairs ...string) types.MappingWithEquals { return types.NewMappingWithEquals(pairs) }
	stamps := EngineStamps{
		StopSignal:                 "SIGTERM",
		StopTimeout:                10,
		HealthTimeout:              30 * time.Second,
		WorkingDir:                 "/",
		Runtimes:                   []string{"oci"},
		PidMode:                    "private",
		UtsMode:                    "private",
		IpcMode:                    "shareable",
		ShmSize:                    65536000,
		CgroupParents:              []string{"user.slice"},
		LogDrivers:                 []string{"journald"},
		HostName:                   "host-1",
		EnvFixed:                   map[string]string{"container": "podman"},
		EnvFromHostname:            []string{"HOSTNAME"},
		EnvUnlessImage:             map[string][]string{"HOME": {"/root", "/"}},
		EnvUnderTTY:                map[string]string{"TERM": "xterm"},
		TmpfsOptions:               []string{"nosuid", "tmpcopyup"},
		MemorySwapOfMemory:         2,
		SharedNamespaceSecurityOpt: labelDisable,
	}
	tests := []struct {
		name  string
		cfg   types.ServiceConfig
		image dockerspec.DockerOCIImageConfig
		want  types.ServiceConfig
	}{
		{name: "the stop signal", cfg: types.ServiceConfig{StopSignal: "SIGTERM"}},
		{name: "another stop signal", cfg: types.ServiceConfig{StopSignal: "SIGINT"}, want: types.ServiceConfig{StopSignal: "SIGINT"}},
		{
			name:  "the stop signal over the image's own",
			cfg:   types.ServiceConfig{StopSignal: "SIGTERM"},
			image: dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{StopSignal: "SIGQUIT"}},
			want:  types.ServiceConfig{StopSignal: "SIGTERM"},
		},
		{name: "the stop timeout", cfg: types.ServiceConfig{StopGracePeriod: grace(10 * time.Second)}},
		{
			name: "another stop timeout",
			cfg:  types.ServiceConfig{StopGracePeriod: grace(25 * time.Second)},
			want: types.ServiceConfig{StopGracePeriod: grace(25 * time.Second)},
		},
		{
			name: "the healthcheck timeout",
			cfg:  types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{Timeout: grace(30 * time.Second)}},
			want: types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{}},
		},
		{
			name: "another healthcheck timeout",
			cfg:  types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{Timeout: grace(5 * time.Second)}},
			want: types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{Timeout: grace(5 * time.Second)}},
		},
		{
			name:  "the healthcheck timeout over an image healthcheck with none",
			cfg:   types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{Timeout: grace(30 * time.Second)}},
			image: dockerspec.DockerOCIImageConfig{Healthcheck: &dockerspec.HealthcheckConfig{Test: []string{"CMD", "true"}}},
			want:  types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{}},
		},
		{
			name:  "the healthcheck timeout over the image's own",
			cfg:   types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{Timeout: grace(30 * time.Second)}},
			image: dockerspec.DockerOCIImageConfig{Healthcheck: &dockerspec.HealthcheckConfig{Timeout: 5 * time.Second}},
			want:  types.ServiceConfig{HealthCheck: &types.HealthCheckConfig{Timeout: grace(30 * time.Second)}},
		},
		{name: "the working dir", cfg: types.ServiceConfig{WorkingDir: "/"}},
		{
			name:  "the working dir over the image's own",
			cfg:   types.ServiceConfig{WorkingDir: "/"},
			image: dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{WorkingDir: "/app"}},
			want:  types.ServiceConfig{WorkingDir: "/"},
		},
		{name: "the runtime", cfg: types.ServiceConfig{Runtime: "oci"}},
		{name: "another runtime", cfg: types.ServiceConfig{Runtime: "runsc"}, want: types.ServiceConfig{Runtime: "runsc"}},
		{name: "the namespace modes", cfg: types.ServiceConfig{Pid: "private", Uts: "private", Ipc: "shareable"}},
		{name: "another ipc mode", cfg: types.ServiceConfig{Ipc: "private"}, want: types.ServiceConfig{Ipc: "private"}},
		{name: "another pid and uts mode", cfg: types.ServiceConfig{Pid: "host", Uts: "host"}, want: types.ServiceConfig{Pid: "host", Uts: "host"}},
		{name: "the shm size", cfg: types.ServiceConfig{ShmSize: 65536000}},
		{name: "another shm size", cfg: types.ServiceConfig{ShmSize: 1 << 30}, want: types.ServiceConfig{ShmSize: 1 << 30}},
		{name: "the cgroup parent", cfg: types.ServiceConfig{CgroupParent: "user.slice"}},
		{name: "another cgroup parent", cfg: types.ServiceConfig{CgroupParent: "app.slice"}, want: types.ServiceConfig{CgroupParent: "app.slice"}},
		{name: "the log driver", cfg: types.ServiceConfig{Logging: &types.LoggingConfig{Driver: "journald"}}},
		{
			name: "the log driver with options",
			cfg:  types.ServiceConfig{Logging: &types.LoggingConfig{Driver: "journald", Options: types.Options{"tag": "x"}}},
			want: types.ServiceConfig{Logging: &types.LoggingConfig{Driver: "journald", Options: types.Options{"tag": "x"}}},
		},
		{name: "the host's hostname in the host's network", cfg: types.ServiceConfig{NetworkMode: "host", Hostname: "host-1"},
			want: types.ServiceConfig{NetworkMode: "host"}},
		{name: "the host's hostname elsewhere", cfg: types.ServiceConfig{Hostname: "host-1"}, want: types.ServiceConfig{Hostname: "host-1"}},
		{
			name: "the tmpfs options",
			cfg:  types.ServiceConfig{Tmpfs: types.StringList{"/run:size=1m,nosuid,tmpcopyup", "/tmp:nosuid", "/var"}},
			want: types.ServiceConfig{Tmpfs: types.StringList{"/run:size=1m", "/tmp", "/var"}},
		},
		{name: "twice the memory as swap", cfg: types.ServiceConfig{MemLimit: 64 << 20, MemSwapLimit: 128 << 20},
			want: types.ServiceConfig{MemLimit: 64 << 20}},
		{name: "a swap of its own", cfg: types.ServiceConfig{MemLimit: 64 << 20, MemSwapLimit: 256 << 20},
			want: types.ServiceConfig{MemLimit: 64 << 20, MemSwapLimit: 256 << 20}},
		{name: "the security option beside the host's pid namespace", cfg: types.ServiceConfig{Pid: "host", SecurityOpt: []string{labelDisable}},
			want: types.ServiceConfig{Pid: "host"}},
		{name: "the security option beside the host's ipc namespace", cfg: types.ServiceConfig{Ipc: "host", SecurityOpt: []string{labelDisable, "seccomp=unconfined"}},
			want: types.ServiceConfig{Ipc: "host", SecurityOpt: []string{"seccomp=unconfined"}}},
		{name: "the security option sharing nothing", cfg: types.ServiceConfig{SecurityOpt: []string{labelDisable}},
			want: types.ServiceConfig{SecurityOpt: []string{labelDisable}}},
		{name: "the fixed, hostname and home variables", cfg: types.ServiceConfig{Environment: env("container=podman", "HOSTNAME=db", "HOME=/root")}},
		{
			name: "variables of the compose file's",
			cfg:  types.ServiceConfig{Environment: env("container=docker", "HOSTNAME=other", "HOME=/home/app", "MODE=prod")},
			want: types.ServiceConfig{Environment: env("container=docker", "HOSTNAME=other", "HOME=/home/app", "MODE=prod")},
		},
		{
			name:  "home the image sets",
			cfg:   types.ServiceConfig{Environment: env("HOME=/root")},
			image: dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Env: []string{"HOME=/root"}}},
			want:  types.ServiceConfig{Environment: env("HOME=/root")},
		},
		{name: "home of a user with no passwd entry", cfg: types.ServiceConfig{Environment: env("HOME=/")}},
		{name: "the terminal under a tty", cfg: types.ServiceConfig{Tty: true, Environment: env("TERM=xterm")}, want: types.ServiceConfig{Tty: true}},
		{name: "the terminal with no tty", cfg: types.ServiceConfig{Environment: env("TERM=xterm")}, want: types.ServiceConfig{Environment: env("TERM=xterm")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			imageID := "sha256:" + strings.Repeat("d", 64)
			replica := model.Container{Inspect: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{Image: imageID},
				Config:            &container.Config{Hostname: "db", Image: "app:1"},
			}}
			introspection := model.IntrospectionResult{Images: map[model.ImageDigestID]image.InspectResponse{
				model.ImageDigestID(imageID): {ID: imageID, Config: &tt.image},
			}}
			cfg := tt.cfg

			// Act
			newAssembly(introspection, stamps).dropEngineStamps(&cfg, replica)

			// Assert
			assert.Equal(t, tt.want, cfg)
		})
	}
}

// An engine stamping nothing leaves every field as it is.
func TestAssembly_dropEngineStamps_NothingStamped(t *testing.T) {
	// Arrange
	cfg := types.ServiceConfig{
		StopSignal:  "SIGTERM",
		WorkingDir:  "/",
		Ipc:         "shareable",
		Environment: types.NewMappingWithEquals([]string{"container=podman", "TERM=xterm"}),
		Tty:         true,
		Tmpfs:       types.StringList{"/run:nosuid"},
		SecurityOpt: []string{labelDisable},
		Privileged:  true,
	}
	want := cfg

	// Act
	newAssembly(model.IntrospectionResult{}, EngineStamps{}).dropEngineStamps(&cfg, model.Container{})

	// Assert
	assert.Equal(t, want, cfg)
}
