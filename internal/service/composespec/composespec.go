// Package composespec builds the compose spec of a live stack from
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

// Assemble builds the compose spec of the introspected stack.
func (a *Assembler) Assemble(res model.IntrospectionResult) (*types.Project, error) {
	if len(res.Project.Services) == 0 {
		return nil, fmt.Errorf("stack %q has no services to act on", res.Project.Name)
	}

	keys := networkKeys(res.Project.Networks, res.Project.Services)
	workingDir, files := projectOrigin(res.Project.Services)
	return &types.Project{
		Name:         res.Project.Name,
		WorkingDir:   workingDir,
		ComposeFiles: files,
		Services:     services(res.Project.Services, keys),
		Volumes:      volumes(res.Project.Volumes),
		Networks:     networks(res.Project.Networks, keys),
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

// dependsOn parses compose's `service:condition:restart` dependency label. Required stays false
// and a dependency the stack no longer holds is dropped: neither may fail the graph.
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
		dependency := types.ServiceDependency{Condition: dependencyCondition, Restart: true}
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
func networks(introspected []model.Network, keys map[string]string) types.Networks {
	owned := make(map[string]bool, len(introspected))
	networks := make(types.Networks, len(keys))
	for _, n := range introspected {
		owned[n.Key] = true
		enableIPv6 := n.Inspect.EnableIPv6
		networks[n.Key] = types.NetworkConfig{
			Name:       n.Name,
			Driver:     n.Inspect.Driver,
			DriverOpts: n.Inspect.Options,
			Ipam:       ipam(n.Inspect.IPAM),
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

// ipam carries the address pool verbatim, auto-assigned subnets included: a
// service holding a static address needs the pool it was allocated from.
func ipam(from network.IPAM) types.IPAMConfig {
	config := types.IPAMConfig{Driver: from.Driver}
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
// creates: without them the docker CLI no longer finds the stack from its path.
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

// userLabels drops what compose stamps itself, leaving the declaration. nil when
// nothing is left, so an empty map never renders.
func userLabels(labels map[string]string) types.Labels {
	kept := types.Labels{}
	for k, v := range labels {
		if !strings.HasPrefix(k, composeLabelPrefix) {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
