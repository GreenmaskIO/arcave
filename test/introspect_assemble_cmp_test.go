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

package test

import (
	"cmp"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"

	"github.com/greenmaskio/arcave/internal/model"
)

// setFields are the spec's fields whose order carries no meaning; command, entrypoint and
// healthcheck.test keep theirs.
var setFields = []string{
	"CapAdd", "CapDrop", "DNS", "DNSOpts", "DNSSearch", "Links", "ExternalLinks", "GroupAdd",
	"SecurityOpt", "Expose", "Tmpfs", "Aliases", "DeviceCgroupRules", "VolumesFrom",
	"Ports", "Volumes", "Secrets", "Configs", "Sysctls",
}

// specOptions compare two compose specs: nil equals empty, and only the set fields are sorted.
func specOptions() gocmp.Options {
	return gocmp.Options{
		cmpopts.EquateEmpty(),
		gocmp.FilterPath(isSetField, cmpopts.SortSlices(lessByString)),
	}
}

// recreatedByRuntime are what an engine changes between two creations of one compose project
// beyond normalizeIntrospection: on Podman, a named volume's creation time.
func recreatedByRuntime(runtime model.ContainerRuntimeKind) gocmp.Options {
	if runtime != model.ContainerRuntimePodman {
		return nil
	}
	return gocmp.Options{cmpopts.IgnoreFields(volume.Volume{}, "CreatedAt")}
}

// restampedByRuntime reports an engine whose containers carry, created anew, what
// normalizeIntrospection then drops: HOSTNAME set to the id, the short ids of the containers
// joining a namespace among its owner's aliases, the ids in its volumes-from annotation, bind
// options in no fixed order, and an anonymous volume's mount request, exec option and z mode,
// which it keeps only when compose created the volume through the mount API.
func restampedByRuntime(runtime model.ContainerRuntimeKind) bool {
	return runtime == model.ContainerRuntimePodman
}

// dropHostnameEnv drops the HOSTNAME Podman sets to the container's own hostname.
func dropHostnameEnv(env []string, hostname string) []string {
	if env == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool { return entry == "HOSTNAME="+hostname })
}

// isBareVolumeRequest reports a request for a volume with no options at all.
func isBareVolumeRequest(request mount.Mount) bool {
	return request.Type == mount.TypeVolume && request.Source == "" && request.VolumeOptions == nil
}

// volumesFromAnnotation spells the container ids of Podman's volumes-from annotation as replicas.
func (n normalizer) volumesFromAnnotation(annotations map[string]string) map[string]string {
	const key = "io.podman.annotations.volumes-from"
	entries, ok := annotations[key]
	if !ok {
		return annotations
	}
	renamed := maps.Clone(annotations)
	renamed[key] = strings.Join(n.entries(strings.Split(entries, ",")), ",")
	return renamed
}

// isReplicaID reports a prefix of a replica's id, as an engine aliases a container by.
func (n normalizer) isReplicaID(alias string) bool {
	for id := range n.ids {
		if alias != "" && strings.HasPrefix(id, alias) {
			return true
		}
	}
	return false
}

// sortBindOptions spells each bind's options sorted, without the rw and exec Podman reports for some.
func sortBindOptions(binds []string) []string {
	if binds == nil {
		return nil
	}
	sorted := make([]string, len(binds))
	for i, bind := range binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) < 3 {
			sorted[i] = bind
			continue
		}
		options := slices.DeleteFunc(strings.Split(parts[2], ","), func(option string) bool { return option == "rw" || option == "exec" })
		slices.Sort(options)
		sorted[i] = parts[0] + ":" + parts[1] + ":" + strings.Join(options, ",")
	}
	return sorted
}

// isSetField reports a slice field named in setFields.
func isSetField(path gocmp.Path) bool {
	field, ok := path.Last().(gocmp.StructField)
	return ok && slices.Contains(setFields, field.Name())
}

// lessByString orders a set field's elements by a key spelling them whole.
func lessByString(a, b any) bool {
	return setKey(a) < setKey(b)
}

func setKey(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case types.ServicePortConfig:
		return strings.Join([]string{e.Protocol, strconv.Itoa(int(e.Target)), e.HostIP, e.Published}, "|")
	case types.ServiceVolumeConfig:
		return e.Target
	case types.ServiceSecretConfig:
		return e.Target + "|" + e.Source
	case types.ServiceConfigObjConfig:
		return e.Target + "|" + e.Source
	}
	return ""
}

// anonVolumeName is the name the daemon generates for an anonymous volume.
var anonVolumeName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// The introspection fields the daemon issues anew for every container and network it
// creates; with normalizeIntrospection, the closed list pass 2 normalizes away.
var (
	ignoredContainerFields = cmpopts.IgnoreFields(container.InspectResponse{},
		"ID", "Created", "State", "ResolvConfPath", "HostnamePath", "HostsPath", "LogPath",
		"RestartCount", "GraphDriver", "Storage", "ExecIDs", "MountLabel", "ProcessLabel")
	ignoredSandboxFields = cmpopts.IgnoreFields(container.NetworkSettings{},
		"SandboxID", "SandboxKey")
	ignoredEndpointFields = cmpopts.IgnoreFields(network.EndpointSettings{},
		"EndpointID", "NetworkID")
	ignoredNetworkFields = cmpopts.IgnoreFields(network.Inspect{}, "Network.ID", "Network.Created", "Containers")
)

// introspectionOptions compare two introspections of one compose project created twice;
// declaredMACs are the MAC addresses its compose file pins.
func introspectionOptions(declaredMACs map[string]bool, runtime model.ContainerRuntimeKind) gocmp.Options {
	return gocmp.Options{
		cmpopts.EquateEmpty(),
		// The SDK's addresses and ports are values with unexported fields, equal by ==.
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}, network.Port{}),
		ignoredContainerFields, ignoredSandboxFields, ignoredEndpointFields,
		ignoredNetworkFields,
		gocmp.Transformer("replicaIdentity", func(res model.IntrospectionResult) model.IntrospectionResult {
			return normalizeIntrospection(res, declaredMACs, restampedByRuntime(runtime))
		}),
	}
}

// normalizeIntrospection renames container ids and anonymous volumes to service#replica
// identities and drops what the daemon allocated rather than the compose file declared.
func normalizeIntrospection(res model.IntrospectionResult, declaredMACs map[string]bool, restamped bool) model.IntrospectionResult {
	n := newNormalizer(res)
	n.declaredMACs = declaredMACs
	n.restamped = restamped
	out := res
	out.Project.Services = make([]model.Service, len(res.Project.Services))
	for i, svc := range res.Project.Services {
		out.Project.Services[i] = n.service(svc)
	}
	out.Project.Volumes = make([]model.NamedVolume, len(res.Project.Volumes))
	for i, v := range res.Project.Volumes {
		out.Project.Volumes[i] = n.namedVolume(v)
	}
	out.Project.Networks = make([]model.Network, len(res.Project.Networks))
	for i, network := range res.Project.Networks {
		network.Inspect.Labels = comparableLabels(network.Inspect.Labels)
		if !n.static[network.Name] {
			network.Inspect.IPAM.Config = nil
		}
		out.Project.Networks[i] = network
	}
	out.Volumes = make(map[string]volume.Volume, len(res.Volumes))
	for name, v := range res.Volumes {
		if _, anonymous := n.volumes[name]; anonymous {
			// Created anew with its container; a named volume must survive both passes.
			v.CreatedAt = ""
		}
		v.Name = n.volumeName(v.Name)
		v.Mountpoint = n.volumeName(v.Mountpoint)
		out.Volumes[n.volumeName(name)] = v
	}
	return out
}

// normalizer knows every replica's id and anonymous volume under its stable identity.
type normalizer struct {
	ids     map[string]string
	volumes map[string]string
	// randomPorts are the container ports published to a port the daemon picks: any, or one of a range.
	randomPorts map[string]bool
	// static are the networks a replica holds a static address in: only they keep their pools.
	static map[string]bool
	// declaredMACs are compared; any other MAC address was generated.
	declaredMACs map[string]bool
	// restamped is an engine restampedByRuntime names.
	restamped bool
}

func newNormalizer(res model.IntrospectionResult) normalizer {
	n := normalizer{
		ids: map[string]string{}, volumes: map[string]string{}, randomPorts: map[string]bool{}, static: map[string]bool{},
	}
	for _, svc := range res.Project.Services {
		for _, c := range svc.Containers {
			identity := svc.Name + "#" + strconv.Itoa(c.Number)
			n.ids[c.ID] = identity
			for _, m := range c.Inspect.Mounts {
				if m.Type == "volume" && anonVolumeName.MatchString(m.Name) {
					n.volumes[m.Name] = identity + ":" + m.Destination
				}
			}
			if c.Inspect.NetworkSettings != nil {
				for name, endpoint := range c.Inspect.NetworkSettings.Networks {
					if endpoint != nil && (hasStaticIPv4(endpoint) || hasStaticIPv6(endpoint)) {
						n.static[name] = true
					}
				}
			}
			if c.Inspect.HostConfig != nil {
				for port, bindings := range c.Inspect.HostConfig.PortBindings {
					for _, b := range bindings {
						if b.HostPort == "" || b.HostPort == "0" || strings.Contains(b.HostPort, "-") {
							n.randomPorts[port.String()] = true
						}
					}
				}
			}
		}
	}
	return n
}

func (n normalizer) service(svc model.Service) model.Service {
	out := svc
	out.Labels = comparableLabels(svc.Labels)
	out.Volumes = make([]model.ServiceVolume, len(svc.Volumes))
	for i, v := range svc.Volumes {
		v.VolumeName = n.volumeName(v.VolumeName)
		v.InheritedFrom = n.id(v.InheritedFrom)
		if v.Type == model.VolumeTypeAnon {
			v.Source = n.volumeName(v.Source)
		}
		out.Volumes[i] = v
	}
	out.Containers = make([]model.Container, len(svc.Containers))
	for i, c := range svc.Containers {
		out.Containers[i] = n.container(c)
	}
	return out
}

func (n normalizer) container(c model.Container) model.Container {
	out := c
	out.ID = n.id(c.ID)
	out.Labels = comparableLabels(c.Labels)
	out.Inspect = n.inspect(c.Inspect)
	return out
}

func (n normalizer) inspect(in container.InspectResponse) container.InspectResponse {
	out := in
	if in.HostConfig != nil {
		host := *in.HostConfig
		host.NetworkMode = container.NetworkMode(n.namespace(string(host.NetworkMode)))
		host.IpcMode = container.IpcMode(n.namespace(string(host.IpcMode)))
		host.PidMode = container.PidMode(n.namespace(string(host.PidMode)))
		host.VolumesFrom = n.entries(host.VolumesFrom)
		host.Binds = n.binds(host.Binds)
		// compose writes these out of maps, in no fixed order.
		host.ExtraHosts = sortedOrNil(host.ExtraHosts)
		host.Links = sortedOrNil(host.Links)
		host.Mounts = slices.Clone(host.Mounts)
		if n.restamped {
			host.Binds = sortBindOptions(host.Binds)
			host.Mounts = slices.DeleteFunc(host.Mounts, isBareVolumeRequest)
			host.Annotations = n.volumesFromAnnotation(host.Annotations)
		}
		host.Binds = sortedOrNil(host.Binds)
		slices.SortFunc(host.Mounts, func(a, b mount.Mount) int { return cmp.Compare(a.Target, b.Target) })
		host.Ulimits = slices.Clone(host.Ulimits)
		slices.SortFunc(host.Ulimits, func(a, b *container.Ulimit) int { return cmp.Compare(a.Name, b.Name) })
		out.HostConfig = &host
	}
	if in.Config != nil {
		cfg := *in.Config
		cfg.Hostname = n.hostname(cfg.Hostname)
		cfg.Labels = comparableLabels(cfg.Labels)
		if n.restamped {
			cfg.Env = dropHostnameEnv(cfg.Env, in.Config.Hostname)
		}
		cfg.Env = sortedOrNil(cfg.Env)
		out.Config = &cfg
	}
	out.Mounts = make([]container.MountPoint, len(in.Mounts))
	for i, m := range in.Mounts {
		m.Name = n.volumeName(m.Name)
		m.Source = hostPath(m.Source)
		if anonVolumeName.MatchString(in.Mounts[i].Name) {
			m.Source = ""
			if n.restamped {
				m.Mode = ""
			}
		}
		out.Mounts[i] = m
	}
	slices.SortFunc(out.Mounts, func(a, b container.MountPoint) int { return cmp.Compare(a.Destination, b.Destination) })
	if in.NetworkSettings != nil {
		out.NetworkSettings = n.networkSettings(in.ID, *in.NetworkSettings)
	}
	return out
}

func (n normalizer) networkSettings(id string, in container.NetworkSettings) *container.NetworkSettings {
	out := in
	out.Ports = network.PortMap{}
	for port, bindings := range in.Ports {
		if n.randomPorts[port.String()] {
			bindings = slices.Clone(bindings)
			for i := range bindings {
				bindings[i].HostPort = ""
			}
		}
		out.Ports[port] = bindings
	}
	out.Networks = make(map[string]*network.EndpointSettings, len(in.Networks))
	for name, endpoint := range in.Networks {
		if endpoint == nil {
			out.Networks[name] = nil
			continue
		}
		e := *endpoint
		e.DNSNames = slices.DeleteFunc(slices.Clone(e.DNSNames), func(dns string) bool {
			return strings.HasPrefix(id, dns)
		})
		e.Aliases = slices.DeleteFunc(slices.Clone(e.Aliases), func(alias string) bool {
			return strings.HasPrefix(id, alias) || n.restamped && n.isReplicaID(alias)
		})
		e.Links = sortedOrNil(e.Links)
		if !n.static[name] {
			e.Gateway, e.IPv6Gateway = netip.Addr{}, netip.Addr{}
		}
		if !hasStaticIPv4(endpoint) {
			e.IPAddress, e.IPPrefixLen = netip.Addr{}, 0
		}
		if !hasStaticIPv6(endpoint) {
			e.GlobalIPv6Address, e.GlobalIPv6PrefixLen = netip.Addr{}, 0
		}
		if !n.declaredMACs[e.MacAddress.String()] {
			e.MacAddress = nil
		}
		out.Networks[name] = &e
	}
	return &out
}

func hasStaticIPv4(e *network.EndpointSettings) bool {
	return e.IPAMConfig != nil && e.IPAMConfig.IPv4Address.IsValid()
}

func hasStaticIPv6(e *network.EndpointSettings) bool {
	return e.IPAMConfig != nil && e.IPAMConfig.IPv6Address.IsValid()
}

func (n normalizer) namedVolume(v model.NamedVolume) model.NamedVolume {
	out := v
	out.Name = n.volumeName(v.Name)
	return out
}

// hostname is the identity of the replica whose id the daemon made the hostname of, or the
// hostname as given; a container in another's network namespace takes the owner's.
func (n normalizer) hostname(hostname string) string {
	if hostname == "" {
		return ""
	}
	for id, identity := range n.ids {
		if strings.HasPrefix(id, hostname) {
			return identity
		}
	}
	return hostname
}

// id is the replica's identity, or the id as given when it is none of the project's.
func (n normalizer) id(id string) string {
	if identity, ok := n.ids[id]; ok {
		return identity
	}
	return id
}

func (n normalizer) entries(entries []string) []string {
	if entries == nil {
		return nil
	}
	out := make([]string, len(entries))
	for i, entry := range entries {
		id, mode, hasMode := strings.Cut(entry, ":")
		out[i] = n.id(id)
		if hasMode {
			out[i] += ":" + mode
		}
	}
	return out
}

func (n normalizer) namespace(mode string) string {
	if id, ok := strings.CutPrefix(mode, types.ContainerPrefix); ok {
		return types.ContainerPrefix + n.id(id)
	}
	return mode
}

func (n normalizer) binds(binds []string) []string {
	if binds == nil {
		return nil
	}
	out := make([]string, len(binds))
	for i, bind := range binds {
		source, rest, _ := strings.Cut(bind, ":")
		out[i] = hostPath(n.volumeName(source)) + ":" + rest
	}
	return out
}

// hostPath undoes Docker Desktop's spelling of a host path in its VM, which it writes for
// some binds and not for others.
func hostPath(path string) string {
	if rest, ok := strings.CutPrefix(path, "/host_mnt"); ok {
		return strings.TrimPrefix(rest, "/private")
	}
	return path
}

// volumeName renames an anonymous volume, or any path naming one, after its replica and target.
func (n normalizer) volumeName(name string) string {
	for generated, identity := range n.volumes {
		name = strings.ReplaceAll(name, generated, identity)
	}
	return name
}

// comparableLabels drops the hash compose stamps of the spec a container was created from
// and Docker Desktop's mirror of the mounts, numbered in the order compose happened to send
// them; it sorts the dependency label, which compose joins in map order.
func comparableLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		switch {
		case k == api.ConfigHashLabel || strings.HasPrefix(k, "desktop.docker.io/"):
		case k == api.DependenciesLabel:
			entries := strings.Split(v, ",")
			slices.Sort(entries)
			out[k] = strings.Join(entries, ",")
		default:
			out[k] = v
		}
	}
	return out
}

// declaredMACs are the MAC addresses a compose project pins on its services.
func declaredMACs(project *types.Project) map[string]bool {
	macs := map[string]bool{}
	for _, svc := range project.Services {
		if svc.MacAddress != "" {
			macs[strings.ToLower(svc.MacAddress)] = true
		}
		for _, attachment := range svc.Networks {
			if attachment != nil && attachment.MacAddress != "" {
				macs[strings.ToLower(attachment.MacAddress)] = true
			}
		}
	}
	return macs
}

func sortedOrNil(list []string) []string {
	if list == nil {
		return nil
	}
	return slices.Sorted(slices.Values(list))
}
