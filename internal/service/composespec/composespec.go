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
	res     model.IntrospectionResult
	netKeys map[string]string
	known   map[string]bool
	// healthchecked are the services whose first replica runs a healthcheck.
	healthchecked map[string]bool
	// replicas maps a replica's container id to its service: compose writes a service
	// reference as the id of the replica it resolved it to.
	replicas map[string]string
	// containers maps a replica's container name to its service: a link names its target by name.
	containers map[string]string
	// foreign maps a container outside the project, by id and by name, to its name: compose
	// writes a container: namespace as the id it resolved the name to.
	foreign map[string]string
}

func newAssembly(res model.IntrospectionResult) assembly {
	s := assembly{
		res:           res,
		netKeys:       networkKeys(res.Project.Networks, res.Project.Services),
		known:         make(map[string]bool, len(res.Project.Services)),
		healthchecked: map[string]bool{},
		replicas:      map[string]string{},
		containers:    map[string]string{},
		foreign:       map[string]string{},
	}
	for id, inspect := range res.Referenced {
		if name := strings.TrimPrefix(inspect.Name, "/"); name != "" {
			s.foreign[id] = name
			s.foreign[name] = name
		}
	}
	for _, svc := range res.Project.Services {
		s.known[svc.Name] = true
		if len(svc.Containers) > 0 && hasHealthCheck(svc.Containers[0].Inspect.Config) {
			s.healthchecked[svc.Name] = true
		}
		for _, c := range svc.Containers {
			if c.ID != "" {
				s.replicas[c.ID] = svc.Name
			}
			if c.Name != "" {
				s.containers[strings.TrimPrefix(c.Name, "/")] = svc.Name
			}
		}
	}
	return s
}

// Assemble builds the compose spec of the introspected compose project.
func (a *Assembler) Assemble(ctx context.Context, res model.IntrospectionResult) (*types.Project, error) {
	if len(res.Project.Services) == 0 {
		return nil, fmt.Errorf("compose project %q has no services to act on", res.Project.Name)
	}

	s := newAssembly(res)
	s.warnOnMissingContainers(ctx)
	workingDir, files := projectOrigin(res.Project.Services)
	return &types.Project{
		Name:         res.Project.Name,
		WorkingDir:   workingDir,
		ComposeFiles: files,
		Services:     s.services(ctx),
		Volumes:      volumes(res.Project.Volumes),
		Networks:     networks(res.Project.Networks, s.netKeys, staticAddressNetworks(res.Project.Services)),
	}, nil
}

// services renders one ServiceConfig per introspected service.
func (s assembly) services(ctx context.Context) types.Services {
	services := make(types.Services, len(s.res.Project.Services))
	for _, svc := range s.res.Project.Services {
		services[svc.Name] = s.serviceConfig(svc)
		s.warnOnDivergentReplicas(ctx, svc, services[svc.Name])
	}
	return services
}

// warnOnMissingContainers reports, once each, a container: namespace naming a container the
// daemon no longer has: the spec keeps the id, and a rollback fails to create on it.
func (s assembly) warnOnMissingContainers(ctx context.Context) {
	var warned []string
	for _, svc := range s.res.Project.Services {
		for _, c := range svc.Containers {
			if c.Inspect.ContainerJSONBase == nil || c.Inspect.HostConfig == nil {
				continue
			}
			h := c.Inspect.HostConfig
			for _, mode := range []string{string(h.NetworkMode), string(h.IpcMode), string(h.PidMode)} {
				ref, ok := strings.CutPrefix(mode, types.ContainerPrefix)
				if !ok || s.replicas[ref] != "" || s.foreign[ref] != "" || slices.Contains(warned, ref) {
					continue
				}
				warned = append(warned, ref)
				zerolog.Ctx(ctx).Warn().Str("service", svc.Name).Str("container", ref).
					Msg("a namespace refers to a container the daemon no longer has; a rollback cannot create the service until it is back")
			}
		}
	}
}

// dependsOn parses compose's `service:condition:restart` dependency label. A dependency the
// compose project no longer holds is dropped, so every one kept exists and is required, as compose's default.
func (s assembly) dependsOn(label string) types.DependsOnConfig {
	if label == "" {
		return nil
	}
	depends := types.DependsOnConfig{}
	for _, entry := range strings.Split(label, ",") {
		parts := strings.Split(entry, ":")
		if !s.known[parts[0]] {
			continue
		}
		dependency := types.ServiceDependency{Condition: s.defaultCondition(parts[0]), Restart: true, Required: true}
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
func (s assembly) defaultCondition(service string) string {
	if s.healthchecked[service] {
		return types.ServiceConditionHealthy
	}
	return types.ServiceConditionStarted
}

// hasHealthCheck reports a healthcheck the container runs; NONE is compose's disable.
func hasHealthCheck(config *container.Config) bool {
	return config != nil && config.Healthcheck != nil && len(config.Healthcheck.Test) > 0 &&
		config.Healthcheck.Test[0] != "NONE"
}

// volumes renders the volumes: section. Name is always explicit: it is the docker
// name the capture was taken from, and compose must not prefix it a second time.
func volumes(introspected []model.NamedVolume) types.Volumes {
	volumes := make(types.Volumes, len(introspected))
	for _, v := range introspected {
		if v.External {
			// compose rejects a declaration of a volume it does not own.
			volumes[v.Key] = types.VolumeConfig{Name: v.Name, External: true}
			continue
		}
		volumes[v.Key] = types.VolumeConfig{
			Name:       v.Name,
			Driver:     v.Driver,
			DriverOpts: v.DriverOpts,
			Labels:     userLabels(v.Labels),
		}
	}
	return volumes
}

// networks renders the networks: section, foreign ones keyed by docker name and external.
// Address pools are declared only where a container holds a static address.
func networks(introspected []model.Network, keys map[string]string, static map[string]bool) types.Networks {
	owned := make(map[string]bool, len(introspected))
	networks := make(types.Networks, len(keys))
	for _, n := range introspected {
		owned[n.Key] = true
		networks[n.Key] = types.NetworkConfig{
			Name:       n.Name,
			Driver:     n.Inspect.Driver,
			DriverOpts: networkOptions(n.Inspect.Options),
			Ipam:       ipam(n.Inspect.IPAM, static[n.Name]),
			Internal:   n.Inspect.Internal,
			Attachable: n.Inspect.Attachable,
			EnableIPv4: declaredIPFamily(n.Inspect.Options, enableIPv4Option, n.Inspect.EnableIPv4),
			EnableIPv6: declaredIPFamily(n.Inspect.Options, enableIPv6Option, n.Inspect.EnableIPv6),
			Labels:     userLabels(n.Inspect.Labels),
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
	for k, v := range options {
		if !slices.Contains(daemonNetworkOptions, k) {
			kept[k] = v
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

// ipam carries the driver, and the pools verbatim only when withPools: a static address needs
// the pool it was allocated from, while pinning an auto-assigned one risks an overlap on create.
func ipam(from network.IPAM, withPools bool) types.IPAMConfig {
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
		for _, c := range svc.Containers {
			if c.Inspect.NetworkSettings == nil {
				continue
			}
			for name, endpoint := range c.Inspect.NetworkSettings.Networks {
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
		for _, c := range svc.Containers {
			if c.Inspect.NetworkSettings == nil {
				continue
			}
			for name := range c.Inspect.NetworkSettings.Networks {
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
		for _, c := range svc.Containers {
			if dir := c.Labels[api.WorkingDirLabel]; dir != "" && workingDir == "" {
				workingDir = dir
			}
			if list := c.Labels[api.ConfigFilesLabel]; list != "" && files == nil {
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
	for k, v := range labels {
		if !strings.HasPrefix(k, composeLabelPrefix) && !strings.HasPrefix(k, desktopLabelPrefix) &&
			!strings.HasPrefix(k, arcaveLabelPrefix) {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
