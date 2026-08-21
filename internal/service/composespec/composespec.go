// Package composespec builds the compose spec of a live stack from
// model.IntrospectionResult. It reads no file.
//
// TODO: the spec only stops and starts an existing stack. Creating one needs
// Container.Inspect's Environment, Command, Entrypoint, WorkingDir, Ports, Restart,
// HealthCheck, Networks, mounts and CustomLabels — what a rollback doing `down` needs.
package composespec

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"

	"github.com/greenmaskio/arcave/internal/model"
)

// dependencyCondition is the default for a dependency whose label omits one.
const dependencyCondition = "running_or_healthy"

// Assembler turns an introspection result into a compose project.
type Assembler struct{}

func NewAssembler() *Assembler { return &Assembler{} }

// Assemble builds the compose spec of the introspected stack.
func (a *Assembler) Assemble(res model.IntrospectionResult) (*types.Project, error) {
	if len(res.Project.Services) == 0 {
		return nil, fmt.Errorf("stack %q has no services to act on", res.Project.Name)
	}
	return &types.Project{
		Name:     res.Project.Name,
		Services: services(res.Project.Services),
		Volumes:  volumes(res.Project.Volumes),
		Networks: networks(res.Project.Networks),
	}, nil
}

// services renders one ServiceConfig per introspected service. Scale is the
// replica count: compose skips a service scaled to zero.
func services(introspected []model.Service) types.Services {
	services := make(types.Services, len(introspected))
	for _, svc := range introspected {
		scale := len(svc.Containers)
		services[svc.Name] = types.ServiceConfig{
			Name:      svc.Name,
			Image:     svc.Image,
			Labels:    svc.Labels,
			Scale:     &scale,
			DependsOn: dependsOn(svc.Labels[api.DependenciesLabel]),
		}
	}
	return services
}

// dependsOn parses compose's `service:condition:restart` dependency label. Required stays
// false, unlike compose's own reading: a dependency with no containers must not fail the graph.
func dependsOn(label string) types.DependsOnConfig {
	if label == "" {
		return nil
	}
	depends := types.DependsOnConfig{}
	for _, entry := range strings.Split(label, ",") {
		parts := strings.Split(entry, ":")
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
	return depends
}

func volumes(introspected []model.NamedVolume) types.Volumes {
	volumes := make(types.Volumes, len(introspected))
	for _, v := range introspected {
		volumes[v.Key] = types.VolumeConfig{Name: v.Name, Driver: v.Driver, Labels: v.Labels}
	}
	return volumes
}

func networks(introspected []model.Network) types.Networks {
	networks := make(types.Networks, len(introspected))
	for _, n := range introspected {
		networks[n.Key] = types.NetworkConfig{
			Name:   n.Name,
			Driver: n.Inspect.Driver,
			Labels: n.Inspect.Labels,
		}
	}
	return networks
}
