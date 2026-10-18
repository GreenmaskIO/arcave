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

// Package composespec builds the compose spec of a live compose project from
// model.IntrospectionResult, complete enough to create it anew. It reads no file.
package composespec

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	// pullPolicyMissing keeps a vanished image loud: create must fail rather than
	// quietly build or start something else.
	pullPolicyMissing = "missing"
	// pullPolicyNever guards an upstream no registry holds: a same-named image there is
	// someone else's.
	pullPolicyNever = "never"
)

// The options the daemon writes into a network created without enable_ipv4 or enable_ipv6,
// recording the default it applied; a network that declared the field carries none.
const (
	enableIPv4Option = "com.docker.network.enable_ipv4"
	enableIPv6Option = "com.docker.network.enable_ipv6"
)

var daemonNetworkOptions = []string{enableIPv4Option, enableIPv6Option}

// predefinedNetworks are the daemon's own; a container in one is described by
// network_mode, never by a networks: entry.
var predefinedNetworks = []string{"host", "none", "bridge", "default"}

// Assembler turns an introspection result into a compose project.
type Assembler struct{}

func NewAssembler() *Assembler { return &Assembler{} }

// assembly is what rendering one service reads beyond the service itself.
type assembly struct {
	introspection model.IntrospectionResult
	networkKeys   map[string]string
	known         map[string]bool
	// healthchecked are the services whose first replica runs a healthcheck.
	healthchecked map[string]bool
	// serviceByReplica maps a replica's container id to its service: compose writes a service
	// reference as the id of the replica it resolved it to.
	serviceByReplica map[string]string
	// serviceByName maps a replica's container name to its service: a link names its target by name.
	serviceByName map[string]string
	// foreignNames maps a container outside the project, by id and by name, to its name: compose
	// writes a container: namespace as the id it resolved the name to.
	foreignNames map[string]string
}

func newAssembly(introspection model.IntrospectionResult) assembly {
	a := assembly{
		introspection:    introspection,
		networkKeys:      networkKeys(introspection.Project.Networks, introspection.Project.Services),
		known:            make(map[string]bool, len(introspection.Project.Services)),
		healthchecked:    map[string]bool{},
		serviceByReplica: map[string]string{},
		serviceByName:    map[string]string{},
		foreignNames:     map[string]string{},
	}
	for id, inspect := range introspection.Referenced {
		if name := strings.TrimPrefix(inspect.Name, "/"); name != "" {
			a.foreignNames[id] = name
			a.foreignNames[name] = name
		}
	}
	for _, svc := range introspection.Project.Services {
		a.known[svc.Name] = true
		if len(svc.Containers) > 0 && hasHealthCheck(svc.Containers[0].Inspect.Config) {
			a.healthchecked[svc.Name] = true
		}
		for _, replica := range svc.Containers {
			if replica.ID != "" {
				a.serviceByReplica[replica.ID] = svc.Name
			}
			if replica.Name != "" {
				a.serviceByName[strings.TrimPrefix(replica.Name, "/")] = svc.Name
			}
		}
	}
	return a
}

// Assemble builds the compose spec of the introspected compose project.
func (*Assembler) Assemble(ctx context.Context, introspection model.IntrospectionResult) (*types.Project, error) {
	if len(introspection.Project.Services) == 0 {
		return nil, fmt.Errorf("compose project %q has no services to act on", introspection.Project.Name)
	}

	a := newAssembly(introspection)
	a.warnOnMissingContainers(ctx)
	workingDir, files := projectOrigin(introspection.Project.Services)
	return &types.Project{
		Name:         introspection.Project.Name,
		WorkingDir:   workingDir,
		ComposeFiles: files,
		Services:     a.newServices(ctx),
		Volumes:      newVolumes(introspection.Project.Volumes),
		Networks:     newNetworks(introspection.Project.Networks, a.networkKeys, staticAddressNetworks(introspection.Project.Services)),
	}, nil
}

// newServices renders one ServiceConfig per introspected service.
func (a assembly) newServices(ctx context.Context) types.Services {
	services := make(types.Services, len(a.introspection.Project.Services))
	for _, svc := range a.introspection.Project.Services {
		services[svc.Name] = a.newServiceConfig(svc)
		a.warnOnDivergentReplicas(ctx, svc, services[svc.Name])
	}
	return services
}

// warnOnMissingContainers reports, once each, a container: namespace naming a container the
// daemon no longer has: the spec keeps the id, and a rollback fails to create on it.
func (a assembly) warnOnMissingContainers(ctx context.Context) {
	var warned []string
	for _, svc := range a.introspection.Project.Services {
		for _, replica := range svc.Containers {
			if replica.Inspect.ContainerJSONBase == nil || replica.Inspect.HostConfig == nil {
				continue
			}
			hostConfig := replica.Inspect.HostConfig
			for _, mode := range []string{string(hostConfig.NetworkMode), string(hostConfig.IpcMode), string(hostConfig.PidMode)} {
				ref, ok := strings.CutPrefix(mode, types.ContainerPrefix)
				if !ok || a.serviceByReplica[ref] != "" || a.foreignNames[ref] != "" || slices.Contains(warned, ref) {
					continue
				}
				warned = append(warned, ref)
				zerolog.Ctx(ctx).Warn().Str("service", svc.Name).Str("container", ref).
					Msg("a namespace refers to a container the daemon no longer has; a rollback cannot create the service until it is back")
			}
		}
	}
}

// parseDependsOn parses compose's `service:condition:restart` dependency label. A dependency the
// compose project no longer holds is dropped, so every one kept exists and is required, as compose's default.
func (a assembly) parseDependsOn(label string) types.DependsOnConfig {
	if label == "" {
		return nil
	}
	depends := types.DependsOnConfig{}
	for _, entry := range strings.Split(label, ",") {
		parts := strings.Split(entry, ":")
		if !a.known[parts[0]] {
			continue
		}
		dependency := types.ServiceDependency{Condition: a.defaultCondition(parts[0]), Restart: true, Required: true}
		if len(parts) > 1 {
			dependency.Condition = parts[1]
		}
		if len(parts) > 2 {
			// compose wrote the label; a flag it cannot spell is no reason to fail a rollback.
			if restart, err := strconv.ParseBool(parts[2]); err == nil {
				dependency.Restart = restart
			}
		}
		depends[parts[0]] = dependency
	}
	if len(depends) == 0 {
		return nil
	}
	return depends
}

// defaultCondition is the condition of a dependency whose label names none. compose reads it as
// "healthy if the service has a healthcheck, else running", which no compose file can spell.
func (a assembly) defaultCondition(service string) string {
	if a.healthchecked[service] {
		return types.ServiceConditionHealthy
	}
	return types.ServiceConditionStarted
}

// hasHealthCheck reports a healthcheck the container runs; NONE is compose's disable.
func hasHealthCheck(config *container.Config) bool {
	return config != nil && config.Healthcheck != nil && len(config.Healthcheck.Test) > 0 &&
		config.Healthcheck.Test[0] != "NONE"
}

// newVolumes renders the newVolumes: section. Name is always explicit: it is the docker
// name the capture was taken from, and compose must not prefix it a second time.
func newVolumes(introspected []model.NamedVolume) types.Volumes {
	volumes := make(types.Volumes, len(introspected))
	for _, volume := range introspected {
		if volume.External {
			// compose rejects a declaration of a volume it does not own.
			volumes[volume.Key] = types.VolumeConfig{Name: volume.Name, External: true}
			continue
		}
		volumes[volume.Key] = types.VolumeConfig{
			Name:       volume.Name,
			Driver:     volume.Driver,
			DriverOpts: volume.DriverOpts,
			Labels:     userLabels(volume.Labels),
		}
	}
	return volumes
}

// newNetworks renders the newNetworks: section, foreign ones keyed by docker name and external.
// Address pools are declared only where a container holds a static address.
func newNetworks(introspected []model.Network, keys map[string]string, static map[string]bool) types.Networks {
	owned := make(map[string]bool, len(introspected))
	networks := make(types.Networks, len(keys))
	for _, projectNetwork := range introspected {
		owned[projectNetwork.Key] = true
		networks[projectNetwork.Key] = types.NetworkConfig{
			Name:       projectNetwork.Name,
			Driver:     projectNetwork.Inspect.Driver,
			DriverOpts: networkOptions(projectNetwork.Inspect.Options),
			Ipam:       newIPAM(projectNetwork.Inspect.IPAM, static[projectNetwork.Name]),
			Internal:   projectNetwork.Inspect.Internal,
			Attachable: projectNetwork.Inspect.Attachable,
			EnableIPv4: declaredIPFamily(projectNetwork.Inspect.Options, enableIPv4Option, projectNetwork.Inspect.EnableIPv4),
			EnableIPv6: declaredIPFamily(projectNetwork.Inspect.Options, enableIPv6Option, projectNetwork.Inspect.EnableIPv6),
			Labels:     userLabels(projectNetwork.Inspect.Labels),
		}
	}
	for name, key := range keys {
		if !owned[key] {
			networks[key] = types.NetworkConfig{Name: name, External: true}
		}
	}
	return networks
}

// networkOptions drops the options the daemon writes itself; nil when nothing is left.
func networkOptions(options map[string]string) types.Options {
	kept := types.Options{}
	for key, value := range options {
		if !slices.Contains(daemonNetworkOptions, key) {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// declaredIPFamily is the declared enable_ipv4 or enable_ipv6, or nil for one the daemon defaulted.
func declaredIPFamily(options map[string]string, defaulted string, enabled bool) *bool {
	if _, ok := options[defaulted]; ok {
		return nil
	}
	return &enabled
}

// newIPAM carries the driver, and the pools verbatim only when withPools: a static address needs
// the pool it was allocated from, while pinning an auto-assigned one risks an overlap on create.
func newIPAM(from network.IPAM, withPools bool) types.IPAMConfig {
	config := types.IPAMConfig{Driver: from.Driver}
	if !withPools {
		return config
	}
	for _, pool := range from.Config {
		config.Config = append(config.Config, &types.IPAMPool{
			Subnet:             pool.Subnet,
			Gateway:            pool.Gateway,
			IPRange:            pool.IPRange,
			AuxiliaryAddresses: types.Mapping(pool.AuxAddress),
		})
	}
	return config
}

// staticAddressNetworks are the docker names of the networks in which any replica holds a
// static address.
func staticAddressNetworks(services []model.Service) map[string]bool {
	static := map[string]bool{}
	for _, svc := range services {
		for _, replica := range svc.Containers {
			if replica.Inspect.NetworkSettings == nil {
				continue
			}
			for name, endpoint := range replica.Inspect.NetworkSettings.Networks {
				if hasStaticAddress(endpoint) {
					static[name] = true
				}
			}
		}
	}
	return static
}

// hasStaticAddress reports whether an endpoint was given an address rather than allocated one.
func hasStaticAddress(endpoint *network.EndpointSettings) bool {
	if endpoint == nil || endpoint.IPAMConfig == nil {
		return false
	}
	return endpoint.IPAMConfig.IPv4Address != "" || endpoint.IPAMConfig.IPv6Address != ""
}

// networkKeys maps a network's docker name to its key in the project. A network
// the project does not own carries no compose label, so it keys under its name.
func networkKeys(introspected []model.Network, services []model.Service) map[string]string {
	keys := make(map[string]string, len(introspected))
	for _, n := range introspected {
		keys[n.Name] = n.Key
	}
	for _, svc := range services {
		for _, replica := range svc.Containers {
			if replica.Inspect.NetworkSettings == nil {
				continue
			}
			for name := range replica.Inspect.NetworkSettings.Networks {
				if _, ok := keys[name]; !ok && !slices.Contains(predefinedNetworks, name) {
					keys[name] = name
				}
			}
		}
	}
	return keys
}

// projectOrigin recovers the directory and files compose stamps on what it
// creates: without them the docker CLI no longer finds the compose project from its path.
func projectOrigin(services []model.Service) (workingDir string, files []string) {
	for _, svc := range services {
		for _, replica := range svc.Containers {
			if dir := replica.Labels[api.WorkingDirLabel]; dir != "" && workingDir == "" {
				workingDir = dir
			}
			if list := replica.Labels[api.ConfigFilesLabel]; list != "" && files == nil {
				files = strings.Split(list, ",")
			}
			if workingDir != "" && files != nil {
				return workingDir, files
			}
		}
	}
	return workingDir, files
}

// userLabels drops what compose, Docker Desktop and arcave stamp themselves, leaving the
// declaration. nil when nothing is left, so an empty map never renders.
func userLabels(labels map[string]string) types.Labels {
	const (
		// composeLabelPrefix marks what compose stamps itself. Compose re-stamps it on
		// create, so the spec must not declare it as the user's own.
		composeLabelPrefix = "com.docker.compose."
		// desktopLabelPrefix marks what Docker Desktop stamps on every container it creates;
		// it is the daemon's, not the user's, and means nothing outside Desktop.
		desktopLabelPrefix = "desktop.docker.io/"
		// arcaveLabelPrefix marks what arcave stamps on a committed layer; it is the image's
		// statement about its upstream, not the service's.
		arcaveLabelPrefix = "io.arcave."
	)

	kept := types.Labels{}
	for key, value := range labels {
		if !strings.HasPrefix(key, composeLabelPrefix) && !strings.HasPrefix(key, desktopLabelPrefix) &&
			!strings.HasPrefix(key, arcaveLabelPrefix) {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
