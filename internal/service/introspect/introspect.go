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

// Package introspect reports a compose project as the daemon holds it, in one pass: services,
// their mounts, the project's volumes and networks, the images its containers run on, and
// `docker info`. Answers are kept verbatim. It reads no compose file and no config.
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

// Inspector reports the one compose project it is constructed with.
type Inspector struct {
	api            dockerAPI
	composeProject model.DockerComposeProjectName
}

func NewInspector(api dockerAPI, composeProject model.DockerComposeProjectName) *Inspector {
	return &Inspector{api: api, composeProject: composeProject}
}

// Introspect reports the compose project, stopped containers included. It cannot see an external volume
// named like a generated one, an unmounted foreign volume, or a torn-down compose project.
func (i *Inspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	replicas, err := i.replicas(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read containers of compose project %q: %w", i.composeProject, err)
	}

	// volumesIndex holds every volume the project names or uses: its own named volumes first,
	// then each external or anonymous one a service or any of its replicas mounts.
	volumesIndex, err := i.ownedVolumes(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read volumes of compose project %q: %w", i.composeProject, err)
	}

	services, err := i.services(ctx, replicas, volumesIndex)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read services of compose project %q: %w", i.composeProject, err)
	}

	if err := i.mountedVolumes(ctx, services, volumesIndex); err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read volumes mounted in compose project %q: %w", i.composeProject, err)
	}

	networks, err := i.networks(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read networks of compose project %q: %w", i.composeProject, err)
	}

	images, err := i.images(ctx, services)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read images of compose project %q: %w", i.composeProject, err)
	}

	info, err := i.api.Info(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read docker daemon info: %w", err)
	}

	return model.IntrospectionResult{
		Project: model.Project{
			Name:     i.composeProject.String(),
			Services: services,
			Volumes:  i.namedVolumes(services, volumesIndex),
			Networks: networks,
		},
		Daemon:  info,
		Images:  images,
		Volumes: volumesIndex,
	}, nil
}

// replicas lists the compose project's containers, stopped ones included, without the one-off
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
		return nil, errors.New("no containers found: bring the compose project up once, or name another project with -p")
	}
	return replicas, nil
}

// projectFilter selects what compose stamped with this compose project's name.
func (i *Inspector) projectFilter() filters.Args {
	return filters.NewArgs(filters.Arg("label", api.ProjectLabel+"="+i.composeProject.String()))
}

// keepProjectContainers drops the one-off containers.
// Filtered here, not in the daemon query: a compose too old to set the label would drop the compose project.
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

// services reports the compose project's services, sorted by name.
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
	mounts, err := i.serviceVolumes(ctx, replicas[0].Mounts, containers[0].Inspect, volumes)
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
	ctx context.Context, mounts []container.MountPoint, inspect container.InspectResponse,
	index map[string]volume.Volume,
) ([]model.ServiceVolume, error) {
	declared := declaredMounts(inspect)
	volumes := make([]model.ServiceVolume, 0, len(mounts))
	for _, m := range mounts {
		v, err := i.serviceVolume(ctx, m, index)
		if err != nil {
			return nil, err
		}
		applyMountOptions(&v, m, declared[m.Destination])
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

// declaredMount is one mount as requested, through the mount API (api) or the bind API (flags).
// source is the path compose gave; Docker Desktop reports its VM's own path in .Mounts.
type declaredMount struct {
	api    *mount.Mount
	source string
	flags  []string
}

// declaredMounts indexes the container's mount requests by target; .Mounts drops their options.
func declaredMounts(inspect container.InspectResponse) map[string]declaredMount {
	declared := map[string]declaredMount{}
	if inspect.ContainerJSONBase == nil || inspect.HostConfig == nil {
		return declared
	}
	for _, bind := range inspect.HostConfig.Binds {
		if source, target, flags, ok := parseBind(bind); ok {
			declared[target] = declaredMount{source: source, flags: flags}
		}
	}
	for idx := range inspect.HostConfig.Mounts {
		m := &inspect.HostConfig.Mounts[idx]
		declared[m.Target] = declaredMount{api: m, source: m.Source}
	}
	return declared
}

// parseBind splits a bind API entry, `source:target[:flags]`.
func parseBind(bind string) (source, target string, flags []string, ok bool) {
	parts := strings.Split(bind, ":")
	switch {
	case len(parts) >= 3 && strings.HasPrefix(parts[len(parts)-2], "/"):
		return strings.Join(parts[:len(parts)-2], ":"), parts[len(parts)-2],
			strings.Split(parts[len(parts)-1], ","), true
	case len(parts) >= 2:
		return strings.Join(parts[:len(parts)-1], ":"), parts[len(parts)-1], nil, true
	default:
		return "", "", nil, false
	}
}

// applyMountOptions fills sv's per-type options and source from the declared mount.
func applyMountOptions(sv *model.ServiceVolume, m container.MountPoint, declared declaredMount) {
	switch sv.Type {
	case model.VolumeTypeNamed, model.VolumeTypeAnon:
		sv.Volume = volumeOptions(declared)
	case model.VolumeTypeBind:
		if declared.source != "" {
			sv.Source = declared.source
		}
		sv.Bind = bindOptions(m, declared)
	case model.VolumeTypeTmpfs:
		if declared.api != nil && declared.api.TmpfsOptions != nil {
			sv.Tmpfs = &model.TmpfsMount{
				SizeBytes: declared.api.TmpfsOptions.SizeBytes,
				Mode:      uint32(declared.api.TmpfsOptions.Mode),
			}
		}
	case model.VolumeTypeImage:
		if declared.api != nil {
			sv.Source = declared.api.Source
			if declared.api.ImageOptions != nil {
				sv.Image = &model.ImageMount{Subpath: declared.api.ImageOptions.Subpath}
			}
		}
	}
}

// volumeOptions reads nocopy, subpath and the per-mount labels; nil when none was asked for.
func volumeOptions(declared declaredMount) *model.VolumeMount {
	if slices.Contains(declared.flags, "nocopy") {
		return &model.VolumeMount{NoCopy: true}
	}
	if declared.api == nil || declared.api.VolumeOptions == nil {
		return nil
	}
	o := declared.api.VolumeOptions
	if !o.NoCopy && o.Subpath == "" && len(o.Labels) == 0 {
		return nil
	}
	return &model.VolumeMount{NoCopy: o.NoCopy, Subpath: o.Subpath, Labels: o.Labels}
}

// bindOptions reads a bind's options. The bind API creates a missing host path and the mount
// API does so only when asked, so a bind of unknown origin is taken as the bind API's.
func bindOptions(m container.MountPoint, declared declaredMount) *model.BindMount {
	bind := &model.BindMount{Propagation: string(m.Propagation), CreateHostPath: true}
	for _, flag := range declared.flags {
		if flag == "z" || flag == "Z" {
			bind.SELinux = flag
		}
	}
	if declared.api == nil {
		return bind
	}
	o := declared.api.BindOptions
	if o == nil {
		bind.CreateHostPath = false
		return bind
	}
	bind.CreateHostPath = o.CreateMountpoint
	switch {
	case o.NonRecursive:
		bind.Recursive = "disabled"
	case o.ReadOnlyNonRecursive:
		bind.Recursive = "writable"
	case o.ReadOnlyForceRecursive:
		bind.Recursive = "readonly"
	}
	return bind
}

// owns tells the project's own volume by the label compose stamped on it — the one
// ownedVolumes lists by.
func (i *Inspector) owns(v volume.Volume) bool {
	return v.Labels[api.ProjectLabel] == i.composeProject.String()
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
