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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	// nanoCPUs is the unit HostConfig.NanoCPUs counts in; compose spells the same
	// limit in whole CPUs.
	nanoCPUs = 1e9
	// portModeIngress is what compose's loader fills a published port with; the
	// assembler writes it so a spec equals the one parsed back from its artifact.
	portModeIngress = "ingress"
)

// serviceConfig renders one service off its first replica, which differs from the
// others only in its number. What the image contributed is declared verbatim.
func serviceConfig(svc model.Service, netKeys map[string]string, known map[string]bool) types.ServiceConfig {
	scale := len(svc.Containers)
	cfg := types.ServiceConfig{
		Name:       svc.Name,
		Image:      svc.Image,
		Labels:     userLabels(svc.Labels),
		Scale:      &scale,
		DependsOn:  dependsOn(svc.Labels[api.DependenciesLabel], known),
		PullPolicy: pullPolicyMissing,
		Volumes:    serviceVolumes(svc.Volumes),
	}
	if len(svc.Containers) == 0 {
		return cfg
	}

	inspect := svc.Containers[0].Inspect
	applyContainerConfig(&cfg, inspect)
	applyHostConfig(&cfg, inspect)
	applyNetworks(&cfg, svc.Name, inspect, netKeys)
	return cfg
}

// applyContainerConfig reads the container's own declaration.
func applyContainerConfig(cfg *types.ServiceConfig, inspect container.InspectResponse) {
	c := inspect.Config
	if c == nil {
		return
	}
	if c.Image != "" {
		cfg.Image = c.Image
	}
	cfg.Environment = types.NewMappingWithEquals(c.Env)
	cfg.Command = types.ShellCommand(c.Cmd)
	cfg.Entrypoint = types.ShellCommand(c.Entrypoint)
	cfg.WorkingDir = c.WorkingDir
	cfg.User = c.User
	cfg.DomainName = c.Domainname
	cfg.Hostname = declaredHostname(c.Hostname, inspect.ID)
	cfg.Tty = c.Tty
	cfg.StdinOpen = c.OpenStdin
	cfg.StopSignal = c.StopSignal
	cfg.Expose = exposedPorts(c.ExposedPorts)
	cfg.HealthCheck = healthCheck(c.Healthcheck)
	cfg.ContainerName = declaredContainerName(inspect.Name, c.Labels)
	if labels := userLabels(c.Labels); labels != nil {
		cfg.Labels = labels
	}
	if c.StopTimeout != nil {
		grace := types.Duration(time.Duration(*c.StopTimeout) * time.Second)
		cfg.StopGracePeriod = &grace
	}
}

// applyHostConfig reads what the container was created against the host with.
func applyHostConfig(cfg *types.ServiceConfig, inspect container.InspectResponse) {
	if inspect.ContainerJSONBase == nil || inspect.HostConfig == nil {
		return
	}
	h := inspect.HostConfig

	cfg.Ports = publishedPorts(h.PortBindings)
	cfg.Restart = restartPolicy(h.RestartPolicy)
	cfg.NetworkMode = declaredNetworkMode(string(h.NetworkMode))
	cfg.Logging = logging(h.LogConfig)
	cfg.ExtraHosts = extraHosts(h.ExtraHosts)
	cfg.Tmpfs = tmpfsMounts(h.Tmpfs, cfg.Volumes)

	cfg.CapAdd = h.CapAdd
	cfg.CapDrop = h.CapDrop
	cfg.GroupAdd = h.GroupAdd
	cfg.SecurityOpt = h.SecurityOpt
	cfg.DNS = types.StringList(h.DNS)
	cfg.DNSOpts = h.DNSOptions
	cfg.DNSSearch = types.StringList(h.DNSSearch)
	cfg.Links = h.Links
	cfg.VolumesFrom = h.VolumesFrom
	cfg.VolumeDriver = h.VolumeDriver
	cfg.Sysctls = types.Mapping(h.Sysctls)
	cfg.StorageOpt = h.StorageOpt
	cfg.Privileged = h.Privileged
	cfg.ReadOnly = h.ReadonlyRootfs
	cfg.Init = h.Init
	cfg.ShmSize = types.UnitBytes(h.ShmSize)
	cfg.Runtime = h.Runtime
	cfg.Isolation = string(h.Isolation)
	cfg.Ipc = string(h.IpcMode)
	cfg.Pid = string(h.PidMode)
	cfg.Uts = string(h.UTSMode)
	cfg.UserNSMode = string(h.UsernsMode)
	cfg.Cgroup = string(h.CgroupnsMode)
	cfg.OomScoreAdj = int64(h.OomScoreAdj)

	applyResources(cfg, h.Resources)
}

// applyResources carries the cgroup limits. Only a limit that was set is
// declared: a zero is docker's "unset", not a limit of zero.
func applyResources(cfg *types.ServiceConfig, r container.Resources) {
	cfg.MemLimit = types.UnitBytes(r.Memory)
	cfg.MemReservation = types.UnitBytes(r.MemoryReservation)
	cfg.CPUShares = r.CPUShares
	cfg.CPUPeriod = r.CPUPeriod
	cfg.CPUQuota = r.CPUQuota
	cfg.CPURTPeriod = r.CPURealtimePeriod
	cfg.CPURTRuntime = r.CPURealtimeRuntime
	cfg.CPUSet = r.CpusetCpus
	cfg.CgroupParent = r.CgroupParent
	cfg.DeviceCgroupRules = r.DeviceCgroupRules
	cfg.Devices = devices(r.Devices)
	cfg.Ulimits = ulimits(r.Ulimits)

	if r.MemorySwap > 0 {
		cfg.MemSwapLimit = types.UnitBytes(r.MemorySwap)
	}
	if r.MemorySwappiness != nil && *r.MemorySwappiness >= 0 {
		cfg.MemSwappiness = types.UnitBytes(*r.MemorySwappiness)
	}
	if r.NanoCPUs > 0 {
		cfg.CPUS = float32(r.NanoCPUs) / nanoCPUs
	}
	if r.PidsLimit != nil && *r.PidsLimit > 0 {
		cfg.PidsLimit = *r.PidsLimit
	}
	if r.OomKillDisable != nil {
		cfg.OomKillDisable = *r.OomKillDisable
	}
}

// applyNetworks attaches the service to the project's networks. A container in a
// predefined network is described by network_mode, and compose refuses both.
func applyNetworks(cfg *types.ServiceConfig, svcName string, inspect container.InspectResponse, netKeys map[string]string) {
	if cfg.NetworkMode != "" || inspect.NetworkSettings == nil {
		return
	}
	generated := []string{svcName, strings.TrimPrefix(inspect.Name, "/")}
	attached := make(map[string]*types.ServiceNetworkConfig, len(inspect.NetworkSettings.Networks))
	for name, endpoint := range inspect.NetworkSettings.Networks {
		key, ok := netKeys[name]
		if !ok || endpoint == nil {
			continue
		}
		// MacAddress is left out: after the first run it is generated, not declared.
		attachment := &types.ServiceNetworkConfig{
			Aliases:         declaredAliases(endpoint.Aliases, inspect.ID, generated),
			DriverOpts:      endpoint.DriverOpts,
			GatewayPriority: endpoint.GwPriority,
		}
		if endpoint.IPAMConfig != nil {
			attachment.Ipv4Address = endpoint.IPAMConfig.IPv4Address
			attachment.Ipv6Address = endpoint.IPAMConfig.IPv6Address
			attachment.LinkLocalIPs = endpoint.IPAMConfig.LinkLocalIPs
		}
		attached[key] = attachment
	}
	if len(attached) > 0 {
		cfg.Networks = attached
	}
}

// declaredHostname drops the hostname the daemon generates from the container id.
func declaredHostname(hostname, containerID string) string {
	if hostname == "" || strings.HasPrefix(containerID, hostname) {
		return ""
	}
	return hostname
}

// declaredContainerName returns the name only when it is not the one compose
// generates: compose refuses container_name on a service scaled above one.
func declaredContainerName(name string, labels map[string]string) string {
	project, service, number := labels[api.ProjectLabel], labels[api.ServiceLabel], labels[api.ContainerNumberLabel]
	if project == "" || service == "" || number == "" {
		// Nothing to compare against, and compose refuses container_name on a scaled service.
		return ""
	}

	name = strings.TrimPrefix(name, "/")
	generated := strings.Join([]string{project, service, number}, "-")
	if name == generated || name == strings.ReplaceAll(generated, "-", "_") {
		return ""
	}
	return name
}

// declaredNetworkMode keeps only a mode the user could have written; a compose
// network's name lands there too, and the networks: section already carries it.
func declaredNetworkMode(mode string) string {
	if mode == "host" || mode == "none" ||
		strings.HasPrefix(mode, "container:") || strings.HasPrefix(mode, "service:") {
		return mode
	}
	return ""
}

// declaredAliases drops the aliases compose and docker add of their own: the
// service name, the container's name and its id.
func declaredAliases(aliases []string, containerID string, generated []string) []string {
	kept := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if alias == "" || strings.HasPrefix(containerID, alias) || slices.Contains(generated, alias) {
			continue
		}
		kept = append(kept, alias)
	}
	if len(kept) == 0 {
		return nil
	}
	sort.Strings(kept)
	return kept
}

// exposedPorts renders EXPOSE, sorted so one compose project renders one spec.
func exposedPorts(set nat.PortSet) types.StringOrNumberList {
	if len(set) == 0 {
		return nil
	}
	exposed := make([]string, 0, len(set))
	for port := range set {
		exposed = append(exposed, string(port))
	}
	sort.Strings(exposed)
	return exposed
}

// publishedPorts renders the ports: section from the bindings, not from the running
// container: an empty published port asks for a random one and must not be pinned.
func publishedPorts(bindings nat.PortMap) []types.ServicePortConfig {
	published := make([]types.ServicePortConfig, 0, len(bindings))
	for port, hosts := range bindings {
		target, err := strconv.ParseUint(port.Port(), 10, 32)
		if err != nil {
			continue
		}
		for _, host := range hosts {
			published = append(published, types.ServicePortConfig{
				Mode:      portModeIngress,
				Target:    uint32(target),
				Protocol:  port.Proto(),
				Published: host.HostPort,
				HostIP:    host.HostIP,
			})
		}
	}
	if len(published) == 0 {
		return nil
	}
	sort.Slice(published, func(i, j int) bool {
		if published[i].Target != published[j].Target {
			return published[i].Target < published[j].Target
		}
		return published[i].Published < published[j].Published
	})
	return published
}

// restartPolicy renders the restart: shorthand; docker's "no" is compose's default.
func restartPolicy(policy container.RestartPolicy) string {
	name := string(policy.Name)
	if name == "" || name == "no" {
		return ""
	}
	if policy.MaximumRetryCount > 0 {
		return name + ":" + strconv.Itoa(policy.MaximumRetryCount)
	}
	return name
}

// healthCheck renders healthcheck:; a test of NONE is compose's disable.
func healthCheck(from *container.HealthConfig) *types.HealthCheckConfig {
	if from == nil || len(from.Test) == 0 {
		return nil
	}
	if from.Test[0] == "NONE" {
		return &types.HealthCheckConfig{Disable: true}
	}

	check := &types.HealthCheckConfig{Test: from.Test}
	if from.Interval > 0 {
		check.Interval = duration(from.Interval)
	}
	if from.Timeout > 0 {
		check.Timeout = duration(from.Timeout)
	}
	if from.StartPeriod > 0 {
		check.StartPeriod = duration(from.StartPeriod)
	}
	if from.StartInterval > 0 {
		check.StartInterval = duration(from.StartInterval)
	}
	if from.Retries > 0 {
		retries := uint64(from.Retries)
		check.Retries = &retries
	}
	return check
}

func duration(d time.Duration) *types.Duration {
	converted := types.Duration(d)
	return &converted
}

func logging(from container.LogConfig) *types.LoggingConfig {
	if from.Type == "" {
		return nil
	}
	return &types.LoggingConfig{Driver: from.Type, Options: from.Config}
}

// extraHosts inverts docker's "host:ip" list into compose's host-to-addresses map.
func extraHosts(hosts []string) types.HostsList {
	if len(hosts) == 0 {
		return nil
	}
	list := types.HostsList{}
	for _, entry := range hosts {
		host, address, found := strings.Cut(entry, ":")
		if !found {
			continue
		}
		list[host] = append(list[host], address)
	}
	if len(list) == 0 {
		return nil
	}
	return list
}

func devices(mapped []container.DeviceMapping) []types.DeviceMapping {
	if len(mapped) == 0 {
		return nil
	}
	devices := make([]types.DeviceMapping, 0, len(mapped))
	for _, d := range mapped {
		devices = append(devices, types.DeviceMapping{
			Source:      d.PathOnHost,
			Target:      d.PathInContainer,
			Permissions: d.CgroupPermissions,
		})
	}
	return devices
}

func ulimits(limits []*container.Ulimit) map[string]*types.UlimitsConfig {
	if len(limits) == 0 {
		return nil
	}
	ulimits := make(map[string]*types.UlimitsConfig, len(limits))
	for _, limit := range limits {
		if limit == nil {
			continue
		}
		ulimits[limit.Name] = &types.UlimitsConfig{Soft: int(limit.Soft), Hard: int(limit.Hard)}
	}
	return ulimits
}
