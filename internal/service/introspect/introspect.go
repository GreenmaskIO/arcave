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

// Package introspect reports a compose stack as the daemon holds it, in one pass: services,
// their mounts, the project's volumes and networks, the images its containers run on, and
// `docker info`. Answers are kept verbatim. It reads no compose file and no config.
//
// TODO: read HostConfig.Mounts and HostConfig.Tmpfs for the subpath, nocopy, bind
// flags and tmpfs mounts that .Mounts drops.
package introspect

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

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
	ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	Info(ctx context.Context) (system.Info, error)
}

// Inspector reports the one stack it is constructed with.
type Inspector struct {
	api   dockerAPI
	stack model.DockerComposeProjectName
}

func NewInspector(api dockerAPI, stack model.DockerComposeProjectName) *Inspector {
	return &Inspector{api: api, stack: stack}
}

// Introspect reports the stack, stopped containers included. It cannot see a mount's subpath, an
// external volume named like a generated one, an unmounted foreign volume, or a torn-down stack.
func (i *Inspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	replicas, err := i.replicas(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read containers of compose project %q: %w", i.stack, err)
	}

	// volumesIndex holds every volume the project names or uses: its own named volumes first,
	// then each external or anonymous one a service or any of its replicas mounts.
	volumesIndex, err := i.ownedVolumes(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read volumes of compose project %q: %w", i.stack, err)
	}

	services, err := i.services(ctx, replicas, volumesIndex)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read services of compose project %q: %w", i.stack, err)
	}

	if err := i.mountedVolumes(ctx, services, volumesIndex); err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read volumes mounted in compose project %q: %w", i.stack, err)
	}

	networks, err := i.networks(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read networks of compose project %q: %w", i.stack, err)
	}

	images, err := i.images(ctx, services)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read images of compose project %q: %w", i.stack, err)
	}

	info, err := i.api.Info(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read docker daemon info: %w", err)
	}

	return model.IntrospectionResult{
		Project: model.Project{
			Name:     i.stack.String(),
			Services: services,
			Volumes:  i.namedVolumes(services, volumesIndex),
			Networks: networks,
		},
		Daemon:  info,
		Images:  images,
		Volumes: volumesIndex,
	}, nil
}

// replicas lists the stack's containers, stopped ones included, without the one-off
// containers `docker compose run` leaves behind.
func (i *Inspector) replicas(ctx context.Context) ([]container.Summary, error) {
	summaries, err := i.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: i.projectFilter(),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	replicas := keepProjectContainers(summaries)
	if len(replicas) == 0 {
		return nil, errors.New("no containers found: bring the stack up once, or name another project with -p")
	}
	return replicas, nil
}

// projectFilter selects what compose stamped with this stack's name.
func (i *Inspector) projectFilter() filters.Args {
	return filters.NewArgs(filters.Arg("label", api.ProjectLabel+"="+i.stack.String()))
}

// keepProjectContainers drops the one-off containers.
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

// ownedVolumes are the project's own named volumes, keyed by docker name: the volume index
// the rest of the pass adds to.
func (i *Inspector) ownedVolumes(ctx context.Context) (map[string]volume.Volume, error) {
	listed, err := i.api.VolumeList(ctx, volume.ListOptions{
		Filters: i.projectFilter(),
	})
	if err != nil {
		return nil, err
	}
	volumes := make(map[string]volume.Volume, len(listed.Volumes))
	for _, v := range listed.Volumes {
		if v != nil {
			volumes[v.Name] = *v
		}
	}
	return volumes, nil
}

// services reports the stack's services, sorted by name.
func (i *Inspector) services(
	ctx context.Context, containers []container.Summary, volumes map[string]volume.Volume,
) ([]model.Service, error) {
	byService := groupByService(containers)
	services := make([]model.Service, 0, len(byService))
	for _, name := range slices.Sorted(maps.Keys(byService)) {
		svc, err := i.service(ctx, name, byService[name], volumes)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		services = append(services, svc)
	}
	return services, nil
}

// groupByService groups the containers by service, each group sorted by replica number; one
// carrying no service label is skipped.
func groupByService(summaries []container.Summary) map[string][]container.Summary {
	byService := map[string][]container.Summary{}
	for _, s := range summaries {
		if name := s.Labels[api.ServiceLabel]; name != "" {
			byService[name] = append(byService[name], s)
		}
	}
	for _, group := range byService {
		slices.SortStableFunc(group, func(a, b container.Summary) int {
			return replicaNumber(a.Labels) - replicaNumber(b.Labels)
		})
	}
	return byService
}

// service reports one service from its replicas. The mounts come from the first replica:
// replicas mount the same declarations. A volume a mount names is read through the index.
func (i *Inspector) service(
	ctx context.Context, name string, replicas []container.Summary, volumes map[string]volume.Volume,
) (model.Service, error) {
	containers, err := i.containers(ctx, replicas)
	if err != nil {
		return model.Service{}, err
	}
	mounts, err := i.serviceVolumes(ctx, replicas[0].Mounts, volumes)
	if err != nil {
		return model.Service{}, err
	}
	return model.Service{
		Name:       name,
		Image:      replicas[0].Image,
		Labels:     replicas[0].Labels,
		Volumes:    mounts,
		Containers: containers,
	}, nil
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
	ctx context.Context, mounts []container.MountPoint, index map[string]volume.Volume,
) ([]model.ServiceVolume, error) {
	volumes := make([]model.ServiceVolume, 0, len(mounts))
	for _, m := range mounts {
		v, err := i.serviceVolume(ctx, m, index)
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
	ctx context.Context, m container.MountPoint, index map[string]volume.Volume,
) (model.ServiceVolume, error) {
	sv := model.ServiceVolume{
		RawType:  string(m.Type),
		Target:   m.Destination,
		ReadOnly: !m.RW,
	}

	switch m.Type {
	case mount.TypeVolume:
		v, err := i.volume(ctx, m.Name, index)
		if err != nil {
			return model.ServiceVolume{}, err
		}
		sv.VolumeName = m.Name
		switch {
		case i.owns(v):
			sv.Type = model.VolumeTypeNamed
			sv.Source = v.Labels[api.VolumeLabel]
		case isAnonymous(v):
			sv.Type = model.VolumeTypeAnon
		default:
			// External: compose never labelled it, so its compose key is unrecoverable here.
			sv.Type = model.VolumeTypeNamed
			sv.Source = m.Name
		}
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

// volume answers from the index, inspecting and recording a volume it does not hold yet.
func (i *Inspector) volume(ctx context.Context, name string, index map[string]volume.Volume) (volume.Volume, error) {
	if v, ok := index[name]; ok {
		return v, nil
	}
	v, err := i.api.VolumeInspect(ctx, name)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("inspect volume %q: %w", name, err)
	}
	index[name] = v
	return v, nil
}

// owns tells the project's own volume by the label compose stamped on it — the one
// ownedVolumes lists by.
func (i *Inspector) owns(v volume.Volume) bool {
	return v.Labels[api.ProjectLabel] == i.stack.String()
}

// isAnonymous tells an anonymous volume from an external one by the daemon's label,
// falling back to the generated name shape.
func isAnonymous(v volume.Volume) bool {
	if _, ok := v.Labels[anonymousVolumeLabel]; ok {
		return true
	}
	return anonVolumeName.MatchString(v.Name)
}

// mountedVolumes reads into the index every volume any replica mounts, not only the first.
func (i *Inspector) mountedVolumes(ctx context.Context, services []model.Service, index map[string]volume.Volume) error {
	for _, name := range mountedVolumeNames(services) {
		if _, err := i.volume(ctx, name, index); err != nil {
			return fmt.Errorf("mounted volume %q: %w", name, err)
		}
	}
	return nil
}

// mountedVolumeNames are the docker names of the volumes the replicas mount, in discovery order.
func mountedVolumeNames(services []model.Service) []string {
	var names []string
	for _, svc := range services {
		for _, c := range svc.Containers {
			for _, m := range c.Inspect.Mounts {
				if m.Type == mount.TypeVolume && m.Name != "" && !slices.Contains(names, m.Name) {
					names = append(names, m.Name)
				}
			}
		}
	}
	return names
}

// networks reports the project's networks, sorted by key.
func (i *Inspector) networks(ctx context.Context) ([]model.Network, error) {
	summaries, err := i.api.NetworkList(ctx, network.ListOptions{Filters: i.projectFilter()})
	if err != nil {
		return nil, err
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

// images inspects each distinct image the containers run on, then the base images those name
// by model.BaseImageIDLabel — the true base of a container a rollback rebuilt on an arcave tag.
func (i *Inspector) images(
	ctx context.Context, services []model.Service,
) (map[model.ImageDigestID]image.InspectResponse, error) {
	ids, err := containerImageIDs(services)
	if err != nil {
		return nil, err
	}

	images := make(map[model.ImageDigestID]image.InspectResponse, len(ids))
	for _, id := range ids {
		inspect, err := i.api.ImageInspect(ctx, id.String())
		if err != nil {
			return nil, fmt.Errorf("inspect image %s: %w", id, err)
		}
		images[id] = inspect
	}

	// Look for a base label on container images only, not on the base images found here.
	// The label always points to the original upstream image, so one step is enough.
	for _, id := range ids {
		if err := i.baseImage(ctx, id, images); err != nil {
			return nil, err
		}
	}
	return images, nil
}

// containerImageIDs are the distinct ids of the images the containers run on, in discovery order.
func containerImageIDs(services []model.Service) ([]model.ImageDigestID, error) {
	var ids []model.ImageDigestID
	for _, svc := range services {
		for _, c := range svc.Containers {
			id, err := model.ParseImageDigestID(containerImage(c.Inspect))
			if err != nil {
				return nil, fmt.Errorf("image of container %s: %w", c.ID, err)
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// containerImage is the id of the image a container runs on, empty if the daemon gave none.
func containerImage(inspect container.InspectResponse) string {
	if inspect.ContainerJSONBase == nil {
		return ""
	}
	return inspect.Image
}

// baseImage adds to the index the base image named by the label on image id. A base image is
// the upstream image the user's compose file named (e.g. postgres:16), as opposed to a layer
// arcave committed on top of it. If the daemon no longer has that image (it was removed), it is
// just not added to the index; that is not an error.
func (i *Inspector) baseImage(
	ctx context.Context, id model.ImageDigestID, images map[model.ImageDigestID]image.InspectResponse,
) error {
	base, ok := baseImageID(images[id])
	if !ok {
		return nil
	}
	if _, ok := images[base]; ok {
		return nil
	}
	inspect, err := i.api.ImageInspect(ctx, base.String())
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect base image %s of image %s: %w", base, id, err)
	}
	images[base] = inspect
	return nil
}

// baseImageID is the id an image's model.BaseImageIDLabel names; false when it carries none
// or the value is no id.
func baseImageID(inspect image.InspectResponse) (model.ImageDigestID, bool) {
	if inspect.Config == nil {
		return "", false
	}
	id, err := model.ParseImageDigestID(inspect.Config.Labels[model.BaseImageIDLabel])
	if err != nil {
		return "", false
	}
	return id, true
}

// namedVolumes lists the volumes compose owns, an unmounted one included, plus the external
// ones only a mount reveals, each with its users filled in.
func (i *Inspector) namedVolumes(services []model.Service, index map[string]volume.Volume) []model.NamedVolume {
	users := volumeUsers(services)

	volumes := make([]model.NamedVolume, 0, len(index))
	for name, v := range index {
		if !i.owns(v) {
			continue
		}
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
		if !i.owns(index[name]) {
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
