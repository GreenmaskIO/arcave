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
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/network"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	// dependencyCondition is the default for a dependency whose label omits one.
	dependencyCondition = "running_or_healthy"
	// composeLabelPrefix marks what compose stamps itself. Compose re-stamps it on
	// create, so the spec must not declare it as the user's own.
	composeLabelPrefix = "com.docker.compose."
	// desktopLabelPrefix marks what Docker Desktop stamps on every container it creates;
	// it is the daemon's, not the user's, and means nothing outside Desktop.
	desktopLabelPrefix = "desktop.docker.io/"
	// pullPolicyMissing keeps a vanished image loud: create must fail rather than
	// quietly build or start something else.
	pullPolicyMissing = "missing"
)

// predefinedNetworks are the daemon's own; a container in one is described by
// network_mode, never by a networks: entry.
var predefinedNetworks = []string{"host", "none", "bridge", "default"}

// Assembler turns an introspection result into a compose project.
type Assembler struct{}

func NewAssembler() *Assembler { return &Assembler{} }

// Assemble builds the compose spec of the introspected compose project.
func (a *Assembler) Assemble(res model.IntrospectionResult) (*types.Project, error) {
	if len(res.Project.Services) == 0 {
		return nil, fmt.Errorf("compose project %q has no services to act on", res.Project.Name)
	}

	keys := networkKeys(res.Project.Networks, res.Project.Services)
	workingDir, files := projectOrigin(res.Project.Services)
	return &types.Project{
		Name:         res.Project.Name,
		WorkingDir:   workingDir,
		ComposeFiles: files,
		Services:     services(res.Project.Services, keys),
		Volumes:      volumes(res.Project.Volumes),
		Networks:     networks(res.Project.Networks, keys, staticAddressNetworks(res.Project.Services)),
	}, nil
}

// services renders one ServiceConfig per introspected service.
func services(introspected []model.Service, netKeys map[string]string) types.Services {
	known := make(map[string]bool, len(introspected))
	for _, svc := range introspected {
		known[svc.Name] = true
	}

	services := make(types.Services, len(introspected))
	for _, svc := range introspected {
		services[svc.Name] = serviceConfig(svc, netKeys, known)
	}
	return services
}

// dependsOn parses compose's `service:condition:restart` dependency label. A dependency the
// compose project no longer holds is dropped, so every one kept exists and is required, as compose's default.
func dependsOn(label string, known map[string]bool) types.DependsOnConfig {
	if label == "" {
		return nil
	}
	depends := types.DependsOnConfig{}
	for _, entry := range strings.Split(label, ",") {
		parts := strings.Split(entry, ":")
		if !known[parts[0]] {
			continue
		}
		dependency := types.ServiceDependency{Condition: dependencyCondition, Restart: true, Required: true}
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

// networks renders the networks: section, the foreign ones keyed by docker name
// and marked external — the introspector sees only what carries the project label.
// networks renders the networks: section. A network's address pools are declared only where
// a container holds a static address in it; elsewhere the daemon picks a free range again.
func networks(introspected []model.Network, keys map[string]string, static map[string]bool) types.Networks {
	owned := make(map[string]bool, len(introspected))
	networks := make(types.Networks, len(keys))
	for _, n := range introspected {
		owned[n.Key] = true
		enableIPv6 := n.Inspect.EnableIPv6
		networks[n.Key] = types.NetworkConfig{
			Name:       n.Name,
			Driver:     n.Inspect.Driver,
			DriverOpts: n.Inspect.Options,
			Ipam:       ipam(n.Inspect.IPAM, static[n.Name]),
			Internal:   n.Inspect.Internal,
			Attachable: n.Inspect.Attachable,
			EnableIPv6: &enableIPv6,
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

// userLabels drops what compose and Docker Desktop stamp themselves, leaving the declaration. nil when
// nothing is left, so an empty map never renders.
func userLabels(labels map[string]string) types.Labels {
	kept := types.Labels{}
	for k, v := range labels {
		if !strings.HasPrefix(k, composeLabelPrefix) && !strings.HasPrefix(k, desktopLabelPrefix) {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
