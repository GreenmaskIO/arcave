// Package introspect reports a compose stack as the daemon holds it: services, their
// mounts, and the project's volumes and networks. It reads no compose file and no config.
//
// TODO: read HostConfig.Mounts and HostConfig.Tmpfs for the subpath, nocopy, bind
// flags and tmpfs mounts that .Mounts drops.
package introspect

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"

	"github.com/greenmaskio/arcave/internal/model"
)

// anonymousVolumeLabel is the daemon's mark on a volume it created for a container;
// anonVolumeName is the fallback for daemons that predate it.
const anonymousVolumeLabel = "com.docker.volume.anonymous"

// anonVolumeName matches the name the daemon generates for an anonymous volume.
var anonVolumeName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// dockerAPI is the slice of the Docker SDK this package needs.
type dockerAPI interface {
	ContainerList(ctx context.Context, opts container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error)
	VolumeList(ctx context.Context, opts volume.ListOptions) (volume.ListResponse, error)
	VolumeInspect(ctx context.Context, name string) (volume.Volume, error)
	NetworkList(ctx context.Context, opts network.ListOptions) ([]network.Summary, error)
}

// Inspector reports the one stack it is constructed with.
type Inspector struct {
	api   dockerAPI
	stack model.StackName
}

func NewInspector(api dockerAPI, stack model.StackName) *Inspector {
	return &Inspector{api: api, stack: stack}
}

// Introspect reports the stack, stopped containers included. It cannot see a mount's subpath, an
// external volume named like a generated one, an unmounted foreign volume, or a torn-down stack.
func (i *Inspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	summaries, err := i.api.ContainerList(ctx, container.ListOptions{All: true, Filters: i.projectFilter()})
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("list containers of compose project %q: %w", i.stack, err)
	}
	if len(summaries) == 0 {
		return model.IntrospectionResult{}, fmt.Errorf(
			"no containers found for compose project %q: bring the stack up once, or name another project with -p", i.stack)
	}

	owned, err := i.ownedVolumes(ctx)
	if err != nil {
		return model.IntrospectionResult{}, err
	}

	services, err := i.services(ctx, keepProjectContainers(summaries), owned)
	if err != nil {
		return model.IntrospectionResult{}, err
	}

	networks, err := i.networks(ctx)
	if err != nil {
		return model.IntrospectionResult{}, err
	}

	return model.IntrospectionResult{Project: model.Project{
		Name:     i.stack.String(),
		Services: services,
		Volumes:  namedVolumes(services, owned),
		Networks: networks,
	}}, nil
}

// keepProjectContainers drops the one-off containers `docker compose run` leaves behind.
// Filtered here, not in the daemon query: a compose too old to set the label would drop the stack.
func keepProjectContainers(summaries []container.Summary) []container.Summary {
	kept := make([]container.Summary, 0, len(summaries))
	for _, s := range summaries {
		if s.Labels[api.OneoffLabel] != "True" {
			kept = append(kept, s)
		}
	}
	return kept
}

// projectFilter selects what compose stamped with this stack's name.
func (i *Inspector) projectFilter() filters.Args {
	return filters.NewArgs(filters.Arg("label", api.ProjectLabel+"="+i.stack.String()))
}

// ownedVolumes are the project's own named volumes, keyed by docker name.
func (i *Inspector) ownedVolumes(ctx context.Context) (map[string]volume.Volume, error) {
	listed, err := i.api.VolumeList(ctx, volume.ListOptions{Filters: i.projectFilter()})
	if err != nil {
		return nil, fmt.Errorf("list volumes of compose project %q: %w", i.stack, err)
	}
	owned := make(map[string]volume.Volume, len(listed.Volumes))
	for _, v := range listed.Volumes {
		if v != nil {
			owned[v.Name] = *v
		}
	}
	return owned, nil
}

// services groups the containers into services, sorted by name; one carrying no service label
// is skipped. The mounts come from the first replica: replicas mount the same declarations.
func (i *Inspector) services(
	ctx context.Context, summaries []container.Summary, owned map[string]volume.Volume,
) ([]model.Service, error) {
	replicas := map[string][]container.Summary{}
	for _, s := range summaries {
		if name := s.Labels[api.ServiceLabel]; name != "" {
			replicas[name] = append(replicas[name], s)
		}
	}

	names := make([]string, 0, len(replicas))
	for name := range replicas {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make([]model.Service, 0, len(names))
	for _, name := range names {
		group := replicas[name]
		slices.SortStableFunc(group, func(a, b container.Summary) int {
			return replicaNumber(a.Labels) - replicaNumber(b.Labels)
		})
		containers, err := i.containers(ctx, group)
		if err != nil {
			return nil, err
		}
		volumes, err := i.serviceVolumes(ctx, group[0].Mounts, owned)
		if err != nil {
			return nil, err
		}
		services = append(services, model.Service{
			Name:       name,
			Image:      group[0].Image,
			Labels:     group[0].Labels,
			Volumes:    volumes,
			Containers: containers,
		})
	}
	return services, nil
}

// containers inspects each replica and carries the raw response verbatim.
func (i *Inspector) containers(ctx context.Context, group []container.Summary) ([]model.Container, error) {
	containers := make([]model.Container, 0, len(group))
	for _, s := range group {
		inspect, err := i.api.ContainerInspect(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("inspect container %s: %w", s.ID, err)
		}
		containers = append(containers, model.Container{
			ID:      s.ID,
			Name:    containerName(s.Names),
			Number:  replicaNumber(s.Labels),
			Labels:  s.Labels,
			Inspect: inspect,
		})
	}
	return containers, nil
}

// replicaNumber reads com.docker.compose.container-number; a container without it sorts first.
func replicaNumber(labels map[string]string) int {
	n, err := strconv.Atoi(labels[api.ContainerNumberLabel])
	if err != nil {
		return 0
	}
	return n
}

// containerName takes the daemon's first name and strips the leading slash.
func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// serviceVolumes classifies one container's mounts, sorted by target. The rule must keep
// mirroring client/docker's matchMount, or a capture and its restore address different data.
func (i *Inspector) serviceVolumes(
	ctx context.Context, mounts []container.MountPoint, owned map[string]volume.Volume,
) ([]model.ServiceVolume, error) {
	volumes := make([]model.ServiceVolume, 0, len(mounts))
	for _, m := range mounts {
		v, err := i.serviceVolume(ctx, m, owned)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, v)
	}
	slices.SortStableFunc(volumes, func(a, b model.ServiceVolume) int {
		return strings.Compare(a.Target, b.Target)
	})
	return volumes, nil
}

// serviceVolume classifies one mount; a volume mount is the project's own, anonymous, or external.
func (i *Inspector) serviceVolume(
	ctx context.Context, m container.MountPoint, owned map[string]volume.Volume,
) (model.ServiceVolume, error) {
	sv := model.ServiceVolume{
		RawType:  string(m.Type),
		Target:   m.Destination,
		ReadOnly: !m.RW,
	}

	switch m.Type {
	case mount.TypeVolume:
		if owner, ok := owned[m.Name]; ok {
			sv.Type = model.VolumeTypeNamed
			sv.Source = owner.Labels[api.VolumeLabel]
			sv.VolumeName = m.Name
			break
		}
		anonymous, err := i.isAnonymous(ctx, m.Name)
		if err != nil {
			return model.ServiceVolume{}, err
		}
		if anonymous {
			sv.Type = model.VolumeTypeAnon
			sv.VolumeName = m.Name
			break
		}
		// External: compose never labelled it, so its compose key is unrecoverable here.
		sv.Type = model.VolumeTypeNamed
		sv.Source = m.Name
		sv.VolumeName = m.Name
	case mount.TypeBind:
		sv.Type = model.VolumeTypeBind
		sv.Source = m.Source
		sv.Bind = &model.BindMount{Propagation: string(m.Propagation)}
	case mount.TypeTmpfs:
		sv.Type = model.VolumeTypeTmpfs
	case mount.TypeNamedPipe:
		sv.Type = model.VolumeTypeNPipe
	case mount.TypeCluster:
		sv.Type = model.VolumeTypeCluster
	case mount.TypeImage:
		sv.Type = model.VolumeTypeImage
	default:
		sv.Type = model.VolumeTypeUnknown
	}
	return sv, nil
}

// isAnonymous tells an anonymous volume from an external one by the daemon's label,
// falling back to the generated name shape.
func (i *Inspector) isAnonymous(ctx context.Context, name string) (bool, error) {
	v, err := i.api.VolumeInspect(ctx, name)
	if err != nil {
		return false, fmt.Errorf("inspect volume %q: %w", name, err)
	}
	if _, ok := v.Labels[anonymousVolumeLabel]; ok {
		return true, nil
	}
	return anonVolumeName.MatchString(name), nil
}

// namedVolumes lists the volumes compose owns, an unmounted one included, plus the external
// ones only a mount reveals, each with its users filled in.
func namedVolumes(services []model.Service, owned map[string]volume.Volume) []model.NamedVolume {
	users := volumeUsers(services)

	volumes := make([]model.NamedVolume, 0, len(owned))
	for name, v := range owned {
		volumes = append(volumes, model.NamedVolume{
			Key:        v.Labels[api.VolumeLabel],
			Name:       name,
			UsedBy:     users[name],
			Labels:     v.Labels,
			Driver:     v.Driver,
			DriverOpts: v.Options,
		})
	}
	for name := range users {
		if _, ok := owned[name]; !ok {
			volumes = append(volumes, model.NamedVolume{
				Key: name, Name: name, External: true, UsedBy: users[name],
			})
		}
	}

	slices.SortFunc(volumes, func(a, b model.NamedVolume) int {
		return strings.Compare(a.Key, b.Key)
	})
	return volumes
}

// volumeUsers inverts the service->volume relation, keyed by docker name and sorted by
// (service, target); an identical mount declared twice is one reference.
func volumeUsers(services []model.Service) map[string][]model.ServiceReference {
	users := map[string][]model.ServiceReference{}
	for _, svc := range services {
		for _, v := range svc.Volumes {
			if v.Type != model.VolumeTypeNamed {
				continue
			}
			ref := model.ServiceReference{Service: svc.Name, Target: v.Target, ReadOnly: v.ReadOnly}
			if !slices.Contains(users[v.VolumeName], ref) {
				users[v.VolumeName] = append(users[v.VolumeName], ref)
			}
		}
	}
	for _, refs := range users {
		slices.SortStableFunc(refs, func(a, b model.ServiceReference) int {
			return strings.Compare(a.Service+"\x00"+a.Target, b.Service+"\x00"+b.Target)
		})
	}
	return users
}

// networks reports the project's networks, sorted by key.
func (i *Inspector) networks(ctx context.Context) ([]model.Network, error) {
	summaries, err := i.api.NetworkList(ctx, network.ListOptions{Filters: i.projectFilter()})
	if err != nil {
		return nil, fmt.Errorf("list networks of compose project %q: %w", i.stack, err)
	}

	networks := make([]model.Network, 0, len(summaries))
	for _, n := range summaries {
		networks = append(networks, model.Network{
			Key:     n.Labels[api.NetworkLabel],
			Name:    n.Name,
			Inspect: n,
		})
	}
	slices.SortFunc(networks, func(a, b model.Network) int {
		return strings.Compare(a.Key, b.Key)
	})
	return networks, nil
}
