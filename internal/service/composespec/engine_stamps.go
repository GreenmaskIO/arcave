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
	"slices"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	// desktopStopTimeout is the stop timeout Docker Desktop stamps on every container
	// created without one.
	desktopStopTimeout = 1
	// labelDisable is the security option a daemon adds where the host's namespaces are shared.
	labelDisable = "label=disable"
	// podmanBridgeNetwork is the network Podman puts a network_mode: bridge container in.
	podmanBridgeNetwork = "podman"
)

// EngineStamps is what an engine stamps on a container its compose file did not ask
// for. A zero field is one the engine does not stamp.
type EngineStamps struct {
	// StopSignal, WorkingDir and HealthTimeout are stamped only where the image sets none.
	StopSignal    string
	StopTimeout   int
	HealthTimeout time.Duration
	WorkingDir    string
	Runtimes      []string
	PidMode       string
	UtsMode       string
	IpcMode       string
	ShmSize       int64
	CgroupParents []string
	// LogDrivers are the drivers a container logs to with no logging: declared.
	LogDrivers []string
	// HostName is the host's, which a container in the host's network reports.
	HostName string
	// EnvFixed are variables set to one value in every container.
	EnvFixed map[string]string
	// EnvFromHostname are variables set to the container's hostname.
	EnvFromHostname []string
	// EnvUnlessImage are variables set to one of the values where the image sets none.
	EnvUnlessImage map[string][]string
	// EnvUnderTTY are variables set in a container with a tty.
	EnvUnderTTY map[string]string
	// TmpfsOptions are appended to every tmpfs: entry's own.
	TmpfsOptions []string
	// MemorySwapOfMemory is the multiple of a memory limit given as swap when none is declared.
	MemorySwapOfMemory int64
	// SharedNamespaceSecurityOpt is added beside the host's pid or ipc namespace, or privileged.
	SharedNamespaceSecurityOpt string
	// Networking is how the engine treats networks, read where they are assembled.
	Networking EngineNetworking
}

// EngineNetworking is how an engine's networks behave past what a compose file asks for;
// a zero field is an engine that does not.
type EngineNetworking struct {
	// HostnameAliased reports an engine aliasing a container by its hostname in every network.
	HostnameAliased bool
	// StampedIPFamilies reports an engine stamping IPv4 on and IPv6 off on a network declaring neither.
	StampedIPFamilies bool
	// BridgeNetwork is the engine's own network a network_mode: bridge container joins, when
	// it is not named bridge.
	BridgeNetwork string
	// BridgeModeBesideNetworks reports an engine answering bridge as the mode of a container
	// in a project network.
	BridgeModeBesideNetworks bool
}

// NewDockerStamps is what Docker stamps, on this daemon as `docker info` reports it:
// its runtime, log driver and host name, and Docker Desktop's stop timeout.
func NewDockerStamps(info system.Info) EngineStamps {
	stopTimeout := 0
	if model.IsDockerDesktop(info) {
		stopTimeout = desktopStopTimeout
	}
	return EngineStamps{
		StopTimeout:                stopTimeout,
		Runtimes:                   nonEmpty(info.DefaultRuntime),
		LogDrivers:                 nonEmpty(info.LoggingDriver),
		HostName:                   info.Name,
		ShmSize:                    64 << 20,
		MemorySwapOfMemory:         2,
		SharedNamespaceSecurityOpt: labelDisable,
	}
}

// NewPodmanStamps is what Podman stamps, on this daemon as `docker info` reports it:
// its runtime and log driver beside Podman's own, and its host name.
func NewPodmanStamps(info system.Info) EngineStamps {
	return EngineStamps{
		StopSignal:      "SIGTERM",
		StopTimeout:     10,
		HealthTimeout:   30 * time.Second,
		WorkingDir:      "/",
		Runtimes:        nonEmpty("oci", info.DefaultRuntime),
		PidMode:         "private",
		UtsMode:         "private",
		IpcMode:         "shareable",
		ShmSize:         65536000,
		CgroupParents:   []string{"user.slice", "machine.slice"},
		LogDrivers:      nonEmpty("journald", "k8s-file", info.LoggingDriver),
		HostName:        info.Name,
		EnvFixed:        map[string]string{"container": "podman"},
		EnvFromHostname: []string{"HOSTNAME"},
		// root's, or / for a user with no passwd entry.
		EnvUnlessImage:             map[string][]string{"HOME": {"/root", "/"}},
		EnvUnderTTY:                map[string]string{"TERM": "xterm"},
		TmpfsOptions:               []string{"rprivate", "nosuid", "nodev", "tmpcopyup"},
		MemorySwapOfMemory:         2,
		SharedNamespaceSecurityOpt: labelDisable,
		Networking: EngineNetworking{
			HostnameAliased:          true,
			StampedIPFamilies:        true,
			BridgeNetwork:            podmanBridgeNetwork,
			BridgeModeBesideNetworks: true,
		},
	}
}

// nonEmpty is values without the empty ones; nil when none is left.
func nonEmpty(values ...string) []string {
	kept := slices.DeleteFunc(slices.Clone(values), func(value string) bool { return value == "" })
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// dropEngineStamps takes out of cfg what the engine stamped on the container by itself.
func (a assembly) dropEngineStamps(cfg *types.ServiceConfig, replica model.Container) {
	stamps := a.engineStamps
	image := a.upstreamConfig(replica)

	cfg.Environment = dropEngineEnvironment(cfg.Environment, stamps, replica.Inspect, image.Env, cfg.Tty)
	if stamps.WorkingDir != "" && cfg.WorkingDir == stamps.WorkingDir && image.WorkingDir == "" {
		cfg.WorkingDir = ""
	}
	// A stamp is the engine's only where the image sets none: a value equal to it over
	// the image's own is the compose file's.
	if stamps.StopSignal != "" && cfg.StopSignal == stamps.StopSignal && image.StopSignal == "" {
		cfg.StopSignal = ""
	}
	if stamps.StopTimeout > 0 && cfg.StopGracePeriod != nil &&
		time.Duration(*cfg.StopGracePeriod) == time.Duration(stamps.StopTimeout)*time.Second {
		cfg.StopGracePeriod = nil
	}
	if stamps.HealthTimeout > 0 && cfg.HealthCheck != nil && cfg.HealthCheck.Timeout != nil &&
		time.Duration(*cfg.HealthCheck.Timeout) == stamps.HealthTimeout &&
		(image.Healthcheck == nil || image.Healthcheck.Timeout == 0) {
		cfg.HealthCheck.Timeout = nil
	}
	if slices.Contains(stamps.Runtimes, cfg.Runtime) {
		cfg.Runtime = ""
	}
	cfg.Pid = dropStamped(cfg.Pid, stamps.PidMode)
	cfg.Uts = dropStamped(cfg.Uts, stamps.UtsMode)
	cfg.Ipc = dropStamped(cfg.Ipc, stamps.IpcMode)
	if stamps.ShmSize > 0 && int64(cfg.ShmSize) == stamps.ShmSize {
		cfg.ShmSize = 0
	}
	if slices.Contains(stamps.CgroupParents, cfg.CgroupParent) {
		cfg.CgroupParent = ""
	}
	if cfg.Logging != nil && len(cfg.Logging.Options) == 0 && slices.Contains(stamps.LogDrivers, cfg.Logging.Driver) {
		cfg.Logging = nil
	}
	if container.NetworkMode(cfg.NetworkMode).IsHost() && cfg.Hostname == stamps.HostName {
		cfg.Hostname = ""
	}
	cfg.Tmpfs = dropTmpfsOptions(cfg.Tmpfs, stamps.TmpfsOptions)
	if stamps.MemorySwapOfMemory > 0 && cfg.MemLimit > 0 &&
		int64(cfg.MemSwapLimit) == stamps.MemorySwapOfMemory*int64(cfg.MemLimit) {
		cfg.MemSwapLimit = 0
	}
	if cfg.Pid == "host" || cfg.Ipc == "host" || cfg.Privileged {
		cfg.SecurityOpt = dropValue(cfg.SecurityOpt, stamps.SharedNamespaceSecurityOpt)
	}
}

// upstreamConfig is the config of the image the replica's upstream names, not a restored
// layer's: a commit keeps what the engine stamped.
func (a assembly) upstreamConfig(replica model.Container) dockerspec.DockerOCIImageConfig {
	id, _, ok := model.ResolveUpstream(a.introspection, replica)
	if !ok {
		return dockerspec.DockerOCIImageConfig{}
	}
	if inspected, found := a.introspection.Images[id]; found && inspected.Config != nil {
		return *inspected.Config
	}
	return dockerspec.DockerOCIImageConfig{}
}

// dropEngineEnvironment drops the variables the engine set itself; one the image sets is
// the image's, and stays. nil when nothing is left.
func dropEngineEnvironment(
	environment types.MappingWithEquals, stamps EngineStamps, inspect container.InspectResponse,
	imageEnv []string, tty bool,
) types.MappingWithEquals {
	if len(environment) == 0 {
		return environment
	}
	image := types.NewMappingWithEquals(imageEnv)
	hostname := ""
	if inspect.Config != nil {
		hostname = inspect.Config.Hostname
	}
	isStamped := func(key, value string) bool {
		if fixed, ok := stamps.EnvFixed[key]; ok && value == fixed {
			return true
		}
		if slices.Contains(stamps.EnvFromHostname, key) && value == hostname {
			return true
		}
		if under, ok := stamps.EnvUnderTTY[key]; ok && tty && value == under {
			return true
		}
		return slices.Contains(stamps.EnvUnlessImage[key], value)
	}

	kept := types.MappingWithEquals{}
	for key, value := range environment {
		if _, inImage := image[key]; !inImage && value != nil && isStamped(key, *value) {
			continue
		}
		kept[key] = value
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// dropStamped is value unless it is the engine's non-empty stamp.
func dropStamped(value, engine string) string {
	if engine != "" && value == engine {
		return ""
	}
	return value
}

// dropTmpfsOptions drops the options the engine appends to every tmpfs: entry.
func dropTmpfsOptions(tmpfs types.StringList, appended []string) types.StringList {
	if len(appended) == 0 || len(tmpfs) == 0 {
		return tmpfs
	}
	declared := make(types.StringList, 0, len(tmpfs))
	for _, entry := range tmpfs {
		target, options, found := strings.Cut(entry, ":")
		if !found {
			declared = append(declared, entry)
			continue
		}
		own := slices.DeleteFunc(strings.Split(options, ","), func(option string) bool {
			return slices.Contains(appended, option)
		})
		if len(own) == 0 {
			declared = append(declared, target)
			continue
		}
		declared = append(declared, target+":"+strings.Join(own, ","))
	}
	return declared
}

// dropValue is values without value; nil when nothing is left.
func dropValue(values []string, value string) []string {
	if value == "" || !slices.Contains(values, value) {
		return values
	}
	kept := slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == value })
	if len(kept) == 0 {
		return nil
	}
	return kept
}
