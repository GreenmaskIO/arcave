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
	"github.com/docker/docker/api/types/image"
	"github.com/docker/go-connections/nat"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	// nanoCPUs is the unit HostConfig.NanoCPUs counts in; compose spells the same
	// limit in whole CPUs.
	nanoCPUs = 1e9
	// defaultShmSize is the /dev/shm the daemon gives a container that asks for none.
	defaultShmSize = 64 << 20
	// desktopOperatingSystem is what `docker info` reports on Docker Desktop.
	desktopOperatingSystem = "Docker Desktop"
	// desktopStopTimeout is the stop timeout Docker Desktop stamps on every container
	// created without one.
	desktopStopTimeout = 1
	// labelDisable is the security option the daemon adds where the host's namespaces are shared.
	labelDisable = "label=disable"
)

// newServiceConfig renders one service off its first replica. What the image
// contributed is declared verbatim.
func (a assembly) newServiceConfig(svc model.Service) types.ServiceConfig {
	cfg := a.newBaseConfig(svc)
	if len(svc.Containers) > 0 {
		a.applyReplica(&cfg, svc.Name, svc.Containers[0])
	}
	return cfg
}

// newBaseConfig is what the service declares apart from any one replica.
func (a assembly) newBaseConfig(svc model.Service) types.ServiceConfig {
	scale := len(svc.Containers)
	return types.ServiceConfig{
		Name:       svc.Name,
		Image:      svc.Image,
		Labels:     userLabels(svc.Labels),
		Scale:      &scale,
		DependsOn:  a.parseDependsOn(svc.Labels[api.DependenciesLabel]),
		PullPolicy: pullPolicyMissing,
		Volumes:    newServiceVolumes(ownVolumes(declaredVolumes(svc))),
	}
}

// applyReplica reads one replica's container into cfg.
func (a assembly) applyReplica(cfg *types.ServiceConfig, service string, replica model.Container) {
	a.applyContainerConfig(cfg, replica.Inspect)
	a.applyImage(cfg, replica)
	a.applyHostConfig(cfg, replica.Inspect)
	cfg.Expose = unpublished(cfg.Expose, cfg.Ports)
	applyNetworks(cfg, service, replica.Inspect, a.networkKeys)
	a.applyLinks(cfg, replica)
	dropSharedNetworkFields(cfg)
	a.dropHostNetworkHostname(cfg)
	addSharedDependencies(cfg)
}

// applyContainerConfig reads the container's own declaration.
func (a assembly) applyContainerConfig(cfg *types.ServiceConfig, inspect container.InspectResponse) {
	config := inspect.Config
	if config == nil {
		return
	}
	cfg.Environment = types.NewMappingWithEquals(config.Env)
	cfg.Command = types.ShellCommand(config.Cmd)
	cfg.Entrypoint = types.ShellCommand(config.Entrypoint)
	cfg.WorkingDir = config.WorkingDir
	cfg.User = config.User
	cfg.DomainName = config.Domainname
	cfg.Hostname = declaredHostname(config.Hostname, inspect.ID)
	cfg.Tty = config.Tty
	cfg.StdinOpen = config.OpenStdin
	cfg.StopSignal = config.StopSignal
	cfg.Expose = toExposedPorts(config.ExposedPorts)
	cfg.HealthCheck = newHealthCheck(config.Healthcheck)
	cfg.ContainerName = declaredContainerName(inspect.Name, config.Labels)
	if labels := userLabels(config.Labels); labels != nil {
		cfg.Labels = labels
	}
	if config.StopTimeout != nil && !a.isDaemonStopTimeout(*config.StopTimeout) {
		grace := types.Duration(time.Duration(*config.StopTimeout) * time.Second)
		cfg.StopGracePeriod = &grace
	}
}

// applyImage declares the upstream rather than the arcave tag a rollback ran the
// container on. An upstream no registry knows is never pulled.
func (a assembly) applyImage(cfg *types.ServiceConfig, replica model.Container) {
	if replica.Inspect.Config != nil && replica.Inspect.Config.Image != "" {
		cfg.Image = replica.Inspect.Config.Image
	}
	id, ref, ok := model.ResolveUpstream(a.introspection, replica)
	if !ok {
		return
	}
	if ref != "" {
		cfg.Image = ref
	}
	if inspect, found := a.introspection.Images[id]; found && isLocalImage(inspect) {
		cfg.PullPolicy = pullPolicyNever
	}
}

// isLocalImage reports an image no registry is known to hold: one compose built, or, on the
// classic image store, one without a repo digest. The containerd store gives every image one.
func isLocalImage(inspect image.InspectResponse) bool {
	if len(inspect.RepoDigests) == 0 {
		return true
	}
	return inspect.Config != nil && inspect.Config.Labels[api.ProjectLabel] != ""
}

// applyHostConfig reads what the container was created against the host with.
// What the daemon fills in by itself is left out.
func (a assembly) applyHostConfig(cfg *types.ServiceConfig, inspect container.InspectResponse) {
	if inspect.ContainerJSONBase == nil || inspect.HostConfig == nil {
		return
	}
	hostConfig := inspect.HostConfig
	daemon := a.introspection.Daemon

	cfg.Ports = newPublishedPorts(hostConfig.PortBindings)
	cfg.Restart = toRestartPolicy(hostConfig.RestartPolicy)
	cfg.NetworkMode = declaredNetworkMode(string(hostConfig.NetworkMode), a.serviceByReplica, a.foreignNames)
	cfg.Logging = newLogging(hostConfig.LogConfig, daemon.LoggingDriver)
	cfg.ExtraHosts = parseExtraHosts(hostConfig.ExtraHosts)
	cfg.Tmpfs = toTmpfsList(hostConfig.Tmpfs, cfg.Volumes)

	cfg.CapAdd = hostConfig.CapAdd
	cfg.CapDrop = hostConfig.CapDrop
	cfg.GroupAdd = hostConfig.GroupAdd
	cfg.SecurityOpt = declaredSecurityOpt(hostConfig)
	cfg.DNS = types.StringList(hostConfig.DNS)
	cfg.DNSOpts = hostConfig.DNSOptions
	cfg.DNSSearch = types.StringList(hostConfig.DNSSearch)
	cfg.VolumesFrom = declaredVolumesFrom(hostConfig.VolumesFrom, a.serviceByReplica)
	cfg.VolumeDriver = hostConfig.VolumeDriver
	cfg.Sysctls = types.Mapping(hostConfig.Sysctls)
	cfg.StorageOpt = hostConfig.StorageOpt
	cfg.Privileged = hostConfig.Privileged
	cfg.ReadOnly = hostConfig.ReadonlyRootfs
	cfg.Init = hostConfig.Init
	if hostConfig.ShmSize != defaultShmSize {
		cfg.ShmSize = types.UnitBytes(hostConfig.ShmSize)
	}
	if hostConfig.Runtime != daemon.DefaultRuntime {
		cfg.Runtime = hostConfig.Runtime
	}
	cfg.Isolation = string(hostConfig.Isolation)
	cfg.Ipc = declaredNamespace(string(hostConfig.IpcMode), a.serviceByReplica, a.foreignNames)
	cfg.Pid = declaredNamespace(string(hostConfig.PidMode), a.serviceByReplica, a.foreignNames)
	cfg.Uts = string(hostConfig.UTSMode)
	cfg.UserNSMode = string(hostConfig.UsernsMode)
	cfg.Cgroup = string(hostConfig.CgroupnsMode)
	cfg.OomScoreAdj = int64(hostConfig.OomScoreAdj)

	applyResources(cfg, hostConfig.Resources)
}

// dropHostNetworkHostname clears the hostname a container in the host's network reports:
// it is the host's own.
func (a assembly) dropHostNetworkHostname(cfg *types.ServiceConfig) {
	if container.NetworkMode(cfg.NetworkMode).IsHost() && cfg.Hostname == a.introspection.Daemon.Name {
		cfg.Hostname = ""
	}
}

// isDaemonStopTimeout reports a stop timeout the daemon stamped rather than the user declared.
func (a assembly) isDaemonStopTimeout(seconds int) bool {
	return a.introspection.Daemon.OperatingSystem == desktopOperatingSystem && seconds == desktopStopTimeout
}

// applyResources carries the cgroup limits. Only a limit that was set is
// declared: a zero is docker's "unset", not a limit of zero.
func applyResources(cfg *types.ServiceConfig, resources container.Resources) {
	cfg.MemLimit = types.UnitBytes(resources.Memory)
	cfg.MemReservation = types.UnitBytes(resources.MemoryReservation)
	cfg.CPUShares = resources.CPUShares
	cfg.CPUPeriod = resources.CPUPeriod
	cfg.CPUQuota = resources.CPUQuota
	cfg.CPURTPeriod = resources.CPURealtimePeriod
	cfg.CPURTRuntime = resources.CPURealtimeRuntime
	cfg.CPUSet = resources.CpusetCpus
	cfg.CgroupParent = resources.CgroupParent
	cfg.DeviceCgroupRules = resources.DeviceCgroupRules
	cfg.Devices = newDevices(resources.Devices)
	cfg.Ulimits = newUlimits(resources.Ulimits)

	if resources.MemorySwap > 0 && resources.MemorySwap != 2*resources.Memory {
		// Twice the memory is the swap the daemon gives a limit declared without one.
		cfg.MemSwapLimit = types.UnitBytes(resources.MemorySwap)
	}
	if resources.MemorySwappiness != nil && *resources.MemorySwappiness >= 0 {
		cfg.MemSwappiness = types.UnitBytes(*resources.MemorySwappiness)
	}
	if resources.NanoCPUs > 0 {
		cfg.CPUS = float32(resources.NanoCPUs) / nanoCPUs
	}
	if resources.PidsLimit != nil && *resources.PidsLimit > 0 {
		cfg.PidsLimit = *resources.PidsLimit
	}
	if resources.OomKillDisable != nil {
		cfg.OomKillDisable = *resources.OomKillDisable
	}
}

// applyNetworks attaches the service to the project's networks. A container in a
// predefined network is described by network_mode, and compose refuses both.
func applyNetworks(cfg *types.ServiceConfig, service string, inspect container.InspectResponse, networkKeys map[string]string) {
	if cfg.NetworkMode != "" || inspect.NetworkSettings == nil {
		return
	}
	generated := []string{service, strings.TrimPrefix(inspect.Name, "/")}
	attached := make(map[string]*types.ServiceNetworkConfig, len(inspect.NetworkSettings.Networks))
	for name, endpoint := range inspect.NetworkSettings.Networks {
		key, ok := networkKeys[name]
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
		if endpoint.IPAMConfig == nil && isEmptyAttachment(attachment) {
			// compose creates the endpoint of a nil entry without an IPAM config, and of an
			// empty one with an empty config.
			attachment = nil
		}
		attached[key] = attachment
	}
	if len(attached) > 0 {
		cfg.Networks = attached
	}
}

// isEmptyAttachment reports an attachment declaring nothing beyond the network itself.
func isEmptyAttachment(a *types.ServiceNetworkConfig) bool {
	return len(a.Aliases) == 0 && len(a.DriverOpts) == 0 && a.GatewayPriority == 0 &&
		a.Ipv4Address == "" && a.Ipv6Address == "" && len(a.LinkLocalIPs) == 0
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

// declaredSecurityOpt drops the label=disable the daemon adds to a container sharing the
// host's pid or ipc namespace, or a privileged one.
func declaredSecurityOpt(hostConfig *container.HostConfig) []string {
	if !hostConfig.PidMode.IsHost() && !hostConfig.IpcMode.IsHost() && !hostConfig.Privileged {
		return hostConfig.SecurityOpt
	}
	declared := slices.DeleteFunc(slices.Clone(hostConfig.SecurityOpt), func(opt string) bool {
		return opt == labelDisable
	})
	if len(declared) == 0 {
		return nil
	}
	return declared
}

// declaredNetworkMode keeps only a mode the user could have written; a compose
// network's name lands there too, and the networks: section already carries it.
func declaredNetworkMode(mode string, serviceByReplica, foreignNames map[string]string) string {
	if slices.Contains(predefinedNetworks, mode) ||
		strings.HasPrefix(mode, types.ContainerPrefix) || strings.HasPrefix(mode, types.ServicePrefix) {
		return declaredNamespace(mode, serviceByReplica, foreignNames)
	}
	return ""
}

// declaredNamespace turns compose's container:<id> back into what it resolved: the service:<name>
// of a replica, since Down removes that container, or the name of one outside the project, since
// its id changes once it is recreated. An id neither resolves to stays as written.
func declaredNamespace(mode string, serviceByReplica, foreignNames map[string]string) string {
	id, ok := strings.CutPrefix(mode, types.ContainerPrefix)
	if !ok {
		return mode
	}
	if service, ok := serviceByReplica[id]; ok {
		return types.ServicePrefix + service
	}
	if name, ok := foreignNames[id]; ok {
		return types.ContainerPrefix + name
	}
	return mode
}

// declaredVolumesFrom turns the bare ids compose wrote back into service names, and
// prefixes any other container: unprefixed, the loader takes it for a service.
func declaredVolumesFrom(entries []string, serviceByReplica map[string]string) []string {
	if len(entries) == 0 {
		return nil
	}
	declared := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, mode, hasMode := strings.Cut(entry, ":")
		if service, ok := serviceByReplica[name]; ok {
			name = service
		} else {
			name = types.ContainerPrefix + name
		}
		if hasMode {
			name += ":" + mode
		}
		declared = append(declared, name)
	}
	return declared
}

// dropSharedNetworkFields clears what the daemon refuses beside another container's
// network namespace; what it reports there is the namespace owner's, or the image's.
func dropSharedNetworkFields(cfg *types.ServiceConfig) {
	if !strings.HasPrefix(cfg.NetworkMode, types.ServicePrefix) &&
		!strings.HasPrefix(cfg.NetworkMode, types.ContainerPrefix) {
		return
	}
	cfg.Hostname = ""
	cfg.Links = nil
	cfg.ExternalLinks = nil
	cfg.DNS = nil
	cfg.ExtraHosts = nil
	cfg.Ports = nil
	cfg.Expose = nil
}

// addSharedDependencies declares the dependencies compose's loader infers from a service
// reference or a link, so the spec is the one parsed back from its artifact.
func addSharedDependencies(cfg *types.ServiceConfig) {
	for _, mode := range []string{cfg.NetworkMode, cfg.Ipc, cfg.Pid} {
		if name, ok := strings.CutPrefix(mode, types.ServicePrefix); ok {
			addDependency(cfg, name, true)
		}
	}
	for _, link := range cfg.Links {
		name, _, _ := strings.Cut(link, ":")
		addDependency(cfg, name, true)
	}
	for _, entry := range cfg.VolumesFrom {
		if !strings.HasPrefix(entry, types.ContainerPrefix) {
			name, _, _ := strings.Cut(entry, ":")
			addDependency(cfg, name, false)
		}
	}
}

// addDependency adds a started dependency unless the service already declares one.
func addDependency(cfg *types.ServiceConfig, name string, restart bool) {
	if _, ok := cfg.DependsOn[name]; ok {
		return
	}
	if cfg.DependsOn == nil {
		cfg.DependsOn = types.DependsOnConfig{}
	}
	cfg.DependsOn[name] = types.ServiceDependency{
		Condition: types.ServiceConditionStarted, Restart: restart, Required: true,
	}
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

// toExposedPorts renders EXPOSE, sorted so one compose project renders one spec.
func toExposedPorts(set nat.PortSet) types.StringOrNumberList {
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

// unpublished drops from expose what compose exposed because a port publishes it.
func unpublished(expose types.StringOrNumberList, ports []types.ServicePortConfig) types.StringOrNumberList {
	if len(expose) == 0 || len(ports) == 0 {
		return expose
	}
	kept := make(types.StringOrNumberList, 0, len(expose))
	for _, port := range expose {
		if !slices.ContainsFunc(ports, func(p types.ServicePortConfig) bool {
			return port == strconv.FormatUint(uint64(p.Target), 10)+"/"+p.Protocol
		}) {
			kept = append(kept, port)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// newPublishedPorts renders the ports: section from the bindings, not from the running
// container: an empty published port asks for a random one and must not be pinned.
func newPublishedPorts(bindings nat.PortMap) []types.ServicePortConfig {
	published := make([]types.ServicePortConfig, 0, len(bindings))
	for port, hosts := range bindings {
		target, err := strconv.ParseUint(port.Port(), 10, 32)
		if err != nil {
			continue
		}
		for _, host := range hosts {
			published = append(published, types.ServicePortConfig{
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

// toRestartPolicy renders the restart: shorthand; docker's "no" is compose's default.
func toRestartPolicy(policy container.RestartPolicy) string {
	name := string(policy.Name)
	if name == "" || name == "no" {
		return ""
	}
	if policy.MaximumRetryCount > 0 {
		return name + ":" + strconv.Itoa(policy.MaximumRetryCount)
	}
	return name
}

// newHealthCheck renders healthcheck:; a test of NONE is compose's disable.
func newHealthCheck(from *container.HealthConfig) *types.HealthCheckConfig {
	if from == nil || len(from.Test) == 0 {
		return nil
	}
	if from.Test[0] == "NONE" {
		return &types.HealthCheckConfig{Disable: true}
	}

	check := &types.HealthCheckConfig{Test: from.Test}
	if from.Interval > 0 {
		check.Interval = toDuration(from.Interval)
	}
	if from.Timeout > 0 {
		check.Timeout = toDuration(from.Timeout)
	}
	if from.StartPeriod > 0 {
		check.StartPeriod = toDuration(from.StartPeriod)
	}
	if from.StartInterval > 0 {
		check.StartInterval = toDuration(from.StartInterval)
	}
	if from.Retries > 0 {
		retries := uint64(from.Retries)
		check.Retries = &retries
	}
	return check
}

func toDuration(d time.Duration) *types.Duration {
	converted := types.Duration(d)
	return &converted
}

// newLogging renders newLogging:, unless it is the daemon's own driver with no options.
func newLogging(from container.LogConfig, daemonDriver string) *types.LoggingConfig {
	if from.Type == "" || (from.Type == daemonDriver && len(from.Config) == 0) {
		return nil
	}
	return &types.LoggingConfig{Driver: from.Type, Options: from.Config}
}

// parseExtraHosts inverts docker's "host:ip" list into compose's host-to-addresses map.
func parseExtraHosts(hosts []string) types.HostsList {
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

func newDevices(mapped []container.DeviceMapping) []types.DeviceMapping {
	if len(mapped) == 0 {
		return nil
	}
	devices := make([]types.DeviceMapping, 0, len(mapped))
	for _, device := range mapped {
		devices = append(devices, types.DeviceMapping{
			Source:      device.PathOnHost,
			Target:      device.PathInContainer,
			Permissions: device.CgroupPermissions,
		})
	}
	return devices
}

func newUlimits(limits []*container.Ulimit) map[string]*types.UlimitsConfig {
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
