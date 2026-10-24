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
	"cmp"
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/format"
	"github.com/compose-spec/compose-go/v2/types"
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
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// anonymousVolumeLabel is the daemon's mark on a volume it created for a container;
// anonVolumeName is the fallback for daemons that predate it.
const anonymousVolumeLabel = "com.docker.volume.anonymous"

// desktopBindLabel opens the labels Docker Desktop stamps for each bind it rewrites to its
// VM's own path: desktop.docker.io/binds/<n>/Source and …/Target.
const desktopBindLabel = "desktop.docker.io/binds/"

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
func (s *Inspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	replicas, err := s.replicas(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read containers of compose project %q: %w", s.composeProject, err)
	}

	// volumeIndex holds every volume the project names or uses: its own named volumes first,
	// then each external or anonymous one a service or any of its replicas mounts.
	volumeIndex, err := s.readOwnedVolumes(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read volumes of compose project %q: %w", s.composeProject, err)
	}

	services, err := s.readServices(ctx, replicas, volumeIndex)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read services of compose project %q: %w", s.composeProject, err)
	}

	if err := s.mountedVolumes(ctx, services, volumeIndex); err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read volumes mounted in compose project %q: %w", s.composeProject, err)
	}

	networks, err := s.readNetworks(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read networks of compose project %q: %w", s.composeProject, err)
	}

	images, err := s.readImages(ctx, services)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read images of compose project %q: %w", s.composeProject, err)
	}

	referenced, err := s.readReferenced(ctx, services)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read containers compose project %q refers to: %w", s.composeProject, err)
	}
	markInherited(services, referenced)

	info, err := s.api.Info(ctx)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("read docker daemon info: %w", err)
	}

	return model.IntrospectionResult{
		Project: model.Project{
			Name:     s.composeProject.String(),
			Services: services,
			Volumes:  s.newNamedVolumes(services, volumeIndex),
			Networks: networks,
		},
		Daemon:     info,
		Images:     images,
		Volumes:    volumeIndex,
		Referenced: referenced,
	}, nil
}

// replicas lists the compose project's containers, stopped ones included, without the one-off
// containers `docker compose run` leaves behind. None means never started or taken down, not an error.
func (s *Inspector) replicas(ctx context.Context) ([]container.Summary, error) {
	summaries, err := s.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: s.newProjectFilter(),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return keepProjectContainers(summaries), nil
}

// newProjectFilter selects what compose stamped with this compose project's name.
func (s *Inspector) newProjectFilter() filters.Args {
	return filters.NewArgs(filters.Arg("label", api.ProjectLabel+"="+s.composeProject.String()))
}

// keepProjectContainers drops the one-off containers.
// Filtered here, not in the daemon query: a compose too old to set the label would drop the compose project.
func keepProjectContainers(summaries []container.Summary) []container.Summary {
	kept := make([]container.Summary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Labels[api.OneoffLabel] != "True" {
			kept = append(kept, summary)
		}
	}
	return kept
}

// readOwnedVolumes are the project's own named volumes, keyed by docker name: the volume index
// the rest of the pass adds to.
func (s *Inspector) readOwnedVolumes(ctx context.Context) (map[string]volume.Volume, error) {
	listed, err := s.api.VolumeList(ctx, volume.ListOptions{
		Filters: s.newProjectFilter(),
	})
	if err != nil {
		return nil, err
	}
	volumeIndex := make(map[string]volume.Volume, len(listed.Volumes))
	for _, v := range listed.Volumes {
		if v != nil {
			volumeIndex[v.Name] = *v
		}
	}
	return volumeIndex, nil
}

// readServices reports the compose project's readServices, sorted by name.
func (s *Inspector) readServices(
	ctx context.Context, containers []container.Summary, volumeIndex map[string]volume.Volume,
) ([]model.Service, error) {
	byService := groupByService(containers)
	services := make([]model.Service, 0, len(byService))
	for _, name := range slices.Sorted(maps.Keys(byService)) {
		svc, err := s.readService(ctx, name, byService[name], volumeIndex)
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
	for _, summary := range summaries {
		if name := summary.Labels[api.ServiceLabel]; name != "" {
			byService[name] = append(byService[name], summary)
		}
	}
	for _, group := range byService {
		slices.SortStableFunc(group, func(a, b container.Summary) int {
			return replicaNumber(a.Labels) - replicaNumber(b.Labels)
		})
	}
	return byService
}

// readService reports one readService from its replicas.
func (s *Inspector) readService(
	ctx context.Context, name string, replicas []container.Summary, volumeIndex map[string]volume.Volume,
) (model.Service, error) {
	containers, err := s.readContainers(ctx, replicas)
	if err != nil {
		return model.Service{}, err
	}
	mounts, err := s.replicaMounts(ctx, name, replicas, containers, volumeIndex)
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

// replicaMounts reports the mounts of a service's replicas, sorted by (target, replica).
//
// Replicas are created from one declaration, so the first replica's mounts stand for all
// of them: a bind or a named volume is one and the same for every replica. An anonymous
// volume is not — docker creates one per container — so every replica's is reported,
// numbered by its replica. A mount the first replica does not declare alike is warned of:
// the spec is the first replica's, so a rollback rebuilds the replica without it.
func (s *Inspector) replicaMounts(
	ctx context.Context, service string, replicas []container.Summary, containers []model.Container,
	volumeIndex map[string]volume.Volume,
) ([]model.ServiceVolume, error) {
	var first, mounts []model.ServiceVolume
	for replicaIndex, replica := range replicas {
		ownMounts, err := s.readServiceVolumes(ctx, replica, containers[replicaIndex].Inspect, volumeIndex)
		if err != nil {
			return nil, err
		}
		if replicaIndex == 0 {
			first, mounts = ownMounts, slices.Clone(ownMounts)
			continue
		}
		for _, mount := range undeclaredMounts(first, ownMounts) {
			zerolog.Ctx(ctx).Warn().Str("service", service).Int("replica", containers[replicaIndex].Number).
				Str("target", mount.Target).Str("type", string(mount.Type)).
				Str("fix", "docker compose up -d --force-recreate "+service).
				Msg("the first replica does not declare this mount; a rollback rebuilds the replica with the first replica's mounts")
		}
		for _, mount := range ownMounts {
			if mount.Type == model.VolumeTypeAnon {
				mounts = append(mounts, mount)
			}
		}
	}
	slices.SortStableFunc(mounts, func(a, b model.ServiceVolume) int {
		return cmp.Or(strings.Compare(a.Target, b.Target), a.Replica-b.Replica)
	})
	return mounts, nil
}

// readContainers inspects each replica and carries the raw response verbatim.
func (s *Inspector) readContainers(ctx context.Context, group []container.Summary) ([]model.Container, error) {
	containers := make([]model.Container, 0, len(group))
	for _, summary := range group {
		inspect, err := s.api.ContainerInspect(ctx, summary.ID)
		if err != nil {
			return nil, fmt.Errorf("inspect container %s: %w", summary.ID, err)
		}
		containers = append(containers, model.Container{
			ID:      summary.ID,
			Name:    containerName(summary.Names),
			Number:  replicaNumber(summary.Labels),
			Labels:  summary.Labels,
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

// containerName is the container's own name among the daemon's: a legacy link target
// is also listed as /<linking container>/<alias>, and that may come first.
func containerName(names []string) string {
	for _, name := range names {
		if own := strings.TrimPrefix(name, "/"); !strings.Contains(own, "/") {
			return own
		}
	}
	return ""
}

// readServiceVolumes classifies one container's mounts, sorted by target. The rule must keep
// mirroring client/docker's matchMount, or a capture and its restore address different data.
func (s *Inspector) readServiceVolumes(
	ctx context.Context, replica container.Summary, inspect container.InspectResponse,
	volumeIndex map[string]volume.Volume,
) ([]model.ServiceVolume, error) {
	declared := newDeclaredMounts(inspect, replica.Labels)
	mounts := make([]model.ServiceVolume, 0, len(replica.Mounts))
	for _, mountPoint := range replica.Mounts {
		mount, err := s.readServiceVolume(ctx, mountPoint, volumeIndex)
		if err != nil {
			return nil, err
		}
		applyMountOptions(&mount, mountPoint, declared[mountPoint.Destination])
		if mount.Type == model.VolumeTypeAnon {
			mount.Replica = replicaNumber(replica.Labels)
		}
		mounts = append(mounts, mount)
	}
	slices.SortStableFunc(mounts, func(a, b model.ServiceVolume) int {
		return strings.Compare(a.Target, b.Target)
	})
	return mounts, nil
}

// undeclaredMounts are the mounts of a later replica the first does not declare with the
// same type at the same target. An anonymous volume at a target the first leaves free is
// not one: it is captured, and the replica's own layer declares it back.
func undeclaredMounts(first, own []model.ServiceVolume) []model.ServiceVolume {
	var undeclared []model.ServiceVolume
	for _, mount := range own {
		at := slices.IndexFunc(first, func(f model.ServiceVolume) bool { return f.Target == mount.Target })
		if at >= 0 && first[at].Type == mount.Type {
			continue // declared alike
		}
		if at < 0 && mount.Type == model.VolumeTypeAnon {
			continue // captured, and the replica's own layer declares it back
		}
		undeclared = append(undeclared, mount)
	}
	return undeclared
}

// readServiceVolume classifies one mount; a volume mount is the project's own, anonymous, or external.
func (s *Inspector) readServiceVolume(
	ctx context.Context, mountPoint container.MountPoint, volumeIndex map[string]volume.Volume,
) (model.ServiceVolume, error) {
	serviceVolume := model.ServiceVolume{
		RawType:  string(mountPoint.Type),
		Target:   mountPoint.Destination,
		ReadOnly: !mountPoint.RW,
	}

	switch mountPoint.Type {
	case mount.TypeVolume:
		dockerVolume, err := s.volume(ctx, mountPoint.Name, volumeIndex)
		if err != nil {
			return model.ServiceVolume{}, err
		}
		serviceVolume.VolumeName = mountPoint.Name
		switch {
		case s.owns(dockerVolume):
			serviceVolume.Type = model.VolumeTypeNamed
			serviceVolume.Source = dockerVolume.Labels[api.VolumeLabel]
		case isAnonymous(dockerVolume):
			serviceVolume.Type = model.VolumeTypeAnon
		default:
			// External: compose never labelled it, so its compose key is unrecoverable here.
			serviceVolume.Type = model.VolumeTypeNamed
			serviceVolume.Source = mountPoint.Name
		}
	case mount.TypeBind:
		serviceVolume.Type = model.VolumeTypeBind
		serviceVolume.Source = mountPoint.Source
	case mount.TypeTmpfs:
		serviceVolume.Type = model.VolumeTypeTmpfs
	case mount.TypeNamedPipe:
		serviceVolume.Type = model.VolumeTypeNPipe
	case mount.TypeCluster:
		serviceVolume.Type = model.VolumeTypeCluster
	case mount.TypeImage:
		serviceVolume.Type = model.VolumeTypeImage
	default:
		serviceVolume.Type = model.VolumeTypeUnknown
	}
	return serviceVolume, nil
}

// volume answers from the index, inspecting and recording a volume it does not hold yet.
func (s *Inspector) volume(ctx context.Context, name string, volumeIndex map[string]volume.Volume) (volume.Volume, error) {
	if known, ok := volumeIndex[name]; ok {
		return known, nil
	}
	inspected, err := s.api.VolumeInspect(ctx, name)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("inspect volume %q: %w", name, err)
	}
	volumeIndex[name] = inspected
	return inspected, nil
}

// declaredMount is one mount as requested, through the mount API (mountRequest) or the bind API
// (bindRequest). source is the path compose gave; Docker Desktop reports its VM's own path in .Mounts.
type declaredMount struct {
	mountRequest *mount.Mount
	bindRequest  *types.ServiceVolumeConfig
	source       string
}

// newDeclaredMounts indexes the container's mount requests by target; .Mounts drops their options.
// labels are the container's as listed: Docker Desktop's own show there, not in the inspect.
func newDeclaredMounts(inspect container.InspectResponse, labels map[string]string) map[string]declaredMount {
	declared := map[string]declaredMount{}
	if inspect.ContainerJSONBase == nil || inspect.HostConfig == nil {
		return declared
	}
	hostSources := parseDesktopBindSources(labels)
	for _, entry := range inspect.HostConfig.Binds {
		// An entry the parser refuses is still reported by .Mounts, only without options.
		if bind, err := format.ParseVolume(entry); err == nil {
			source := bind.Source
			if hostSource, ok := hostSources[bind.Target]; ok {
				source = hostSource
			}
			declared[bind.Target] = declaredMount{bindRequest: &bind, source: source}
		}
	}
	for i := range inspect.HostConfig.Mounts {
		request := &inspect.HostConfig.Mounts[i]
		declared[request.Target] = declaredMount{mountRequest: request, source: request.Source}
	}
	return declared
}

// parseDesktopBindSources maps a bind's target to the host path Docker Desktop recorded before
// writing its VM's own path into the bind.
func parseDesktopBindSources(labels map[string]string) map[string]string {
	sources, targets := map[string]string{}, map[string]string{}
	for key, value := range labels {
		rest, ok := strings.CutPrefix(key, desktopBindLabel)
		if !ok {
			continue
		}
		index, field, _ := strings.Cut(rest, "/")
		switch field {
		case "Source":
			sources[index] = value
		case "Target":
			targets[index] = value
		}
	}
	byTarget := make(map[string]string, len(targets))
	for index, target := range targets {
		if source, ok := sources[index]; ok {
			byTarget[target] = source
		}
	}
	return byTarget
}

// applyMountOptions fills mount's per-type options and source from the declared mount.
func applyMountOptions(mount *model.ServiceVolume, mountPoint container.MountPoint, declared declaredMount) {
	switch mount.Type {
	case model.VolumeTypeNamed, model.VolumeTypeAnon:
		mount.Volume = newVolumeOptions(declared)
	case model.VolumeTypeBind:
		if declared.source != "" {
			mount.Source = declared.source
		}
		mount.Bind = newBindOptions(mountPoint, declared)
	case model.VolumeTypeTmpfs:
		if declared.mountRequest != nil && declared.mountRequest.TmpfsOptions != nil {
			mount.Tmpfs = &model.TmpfsMount{
				SizeBytes: declared.mountRequest.TmpfsOptions.SizeBytes,
				Mode:      uint32(declared.mountRequest.TmpfsOptions.Mode),
			}
		}
	case model.VolumeTypeImage:
		if declared.mountRequest != nil {
			mount.Source = declared.mountRequest.Source
			if declared.mountRequest.ImageOptions != nil {
				mount.Image = &model.ImageMount{Subpath: declared.mountRequest.ImageOptions.Subpath}
			}
		}
	}
}

// newVolumeOptions reads nocopy, subpath and the per-mount labels; nil when the request carried
// none. Empty options are kept: compose sends them for every volume its file declares.
func newVolumeOptions(declared declaredMount) *model.VolumeMount {
	if declared.bindRequest != nil && declared.bindRequest.Volume != nil && declared.bindRequest.Volume.NoCopy {
		return &model.VolumeMount{NoCopy: true}
	}
	if declared.mountRequest == nil || declared.mountRequest.VolumeOptions == nil {
		return nil
	}
	volumeOpts := declared.mountRequest.VolumeOptions
	return &model.VolumeMount{NoCopy: volumeOpts.NoCopy, Subpath: volumeOpts.Subpath, Labels: volumeOpts.Labels}
}

// newBindOptions reads a bind's options. The bind API creates a missing host path and the mount
// API does so only when asked, so a bind of unknown origin is taken as the bind API's.
func newBindOptions(mountPoint container.MountPoint, declared declaredMount) *model.BindMount {
	bind := &model.BindMount{Propagation: string(mountPoint.Propagation), CreateHostPath: true}
	if declared.bindRequest != nil && declared.bindRequest.Bind != nil {
		bind.SELinux = declared.bindRequest.Bind.SELinux
	}
	if declared.mountRequest == nil {
		return bind
	}
	bindOpts := declared.mountRequest.BindOptions
	if bindOpts == nil {
		bind.CreateHostPath = false
		return bind
	}
	bind.CreateHostPath = bindOpts.CreateMountpoint
	switch {
	case bindOpts.NonRecursive:
		bind.Recursive = "disabled"
	case bindOpts.ReadOnlyNonRecursive:
		bind.Recursive = "writable"
	case bindOpts.ReadOnlyForceRecursive:
		bind.Recursive = "readonly"
	}
	return bind
}

// owns tells the project's own volume by the label compose stamped on it — the one
// ownedVolumes lists by.
func (s *Inspector) owns(dockerVolume volume.Volume) bool {
	return dockerVolume.Labels[api.ProjectLabel] == s.composeProject.String()
}

// isAnonymous tells an anonymous volume from an external one by the daemon's label,
// falling back to the generated name shape.
func isAnonymous(dockerVolume volume.Volume) bool {
	if _, ok := dockerVolume.Labels[anonymousVolumeLabel]; ok {
		return true
	}
	return anonVolumeName.MatchString(dockerVolume.Name)
}

// mountedVolumes reads into the index every volume any replica mounts, not only the first.
func (s *Inspector) mountedVolumes(ctx context.Context, services []model.Service, volumeIndex map[string]volume.Volume) error {
	for _, name := range mountedVolumeNames(services) {
		if _, err := s.volume(ctx, name, volumeIndex); err != nil {
			return fmt.Errorf("mounted volume %q: %w", name, err)
		}
	}
	return nil
}

// mountedVolumeNames are the docker names of the volumes the replicas mount, in discovery order.
func mountedVolumeNames(services []model.Service) []string {
	var names []string
	for _, svc := range services {
		for _, replica := range svc.Containers {
			for _, mountPoint := range replica.Inspect.Mounts {
				if mountPoint.Type == mount.TypeVolume && mountPoint.Name != "" && !slices.Contains(names, mountPoint.Name) {
					names = append(names, mountPoint.Name)
				}
			}
		}
	}
	return names
}

// readNetworks reports the project's readNetworks, sorted by key.
func (s *Inspector) readNetworks(ctx context.Context) ([]model.Network, error) {
	summaries, err := s.api.NetworkList(ctx, network.ListOptions{Filters: s.newProjectFilter()})
	if err != nil {
		return nil, err
	}

	networks := make([]model.Network, 0, len(summaries))
	for _, summary := range summaries {
		networks = append(networks, model.Network{
			Key:     summary.Labels[api.NetworkLabel],
			Name:    summary.Name,
			Inspect: summary,
		})
	}
	slices.SortFunc(networks, func(a, b model.Network) int {
		return strings.Compare(a.Key, b.Key)
	})
	return networks, nil
}

// readImages inspects each distinct image the containers run on, then the base readImages those name
// by model.SystemLabelBaseImageID — the true base of a container a rollback rebuilt on an arcave tag.
func (s *Inspector) readImages(
	ctx context.Context, services []model.Service,
) (map[model.ImageDigestID]image.InspectResponse, error) {
	ids, err := containerImageIDs(services)
	if err != nil {
		return nil, err
	}

	images := make(map[model.ImageDigestID]image.InspectResponse, len(ids))
	for _, id := range ids {
		inspect, err := s.api.ImageInspect(ctx, id.String())
		if err != nil {
			return nil, fmt.Errorf("inspect image %s: %w", id, err)
		}
		images[id] = inspect
	}

	// Look for a base label on container images only, not on the base images found here.
	// The label always points to the original upstream image, so one step is enough.
	for _, id := range ids {
		if err := s.baseImage(ctx, id, images); err != nil {
			return nil, err
		}
	}
	return images, nil
}

// containerImageIDs are the distinct ids of the images the containers run on, in discovery order.
func containerImageIDs(services []model.Service) ([]model.ImageDigestID, error) {
	var ids []model.ImageDigestID
	for _, svc := range services {
		for _, replica := range svc.Containers {
			id, err := model.ParseImageDigestID(containerImage(replica.Inspect))
			if err != nil {
				return nil, fmt.Errorf("image of container %s: %w", replica.ID, err)
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
func (s *Inspector) baseImage(
	ctx context.Context, id model.ImageDigestID, images map[model.ImageDigestID]image.InspectResponse,
) error {
	base, ok := baseImageID(images[id])
	if !ok {
		return nil
	}
	if _, ok := images[base]; ok {
		return nil
	}
	inspect, err := s.api.ImageInspect(ctx, base.String())
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect base image %s of image %s: %w", base, id, err)
	}
	images[base] = inspect
	return nil
}

// baseImageID is the id an image's model.SystemLabelBaseImageID names; false when it carries none
// or the value is no id.
func baseImageID(inspect image.InspectResponse) (model.ImageDigestID, bool) {
	if inspect.Config == nil {
		return "", false
	}
	id, err := model.ParseImageDigestID(inspect.Config.Labels[string(model.SystemLabelBaseImageID)])
	if err != nil {
		return "", false
	}
	return id, true
}

// readReferenced inspects each distinct container outside the project a replica refers to, keyed by
// id. One the daemon no longer has is left out; that is not an error.
func (s *Inspector) readReferenced(
	ctx context.Context, services []model.Service,
) (map[string]container.InspectResponse, error) {
	referenced := map[string]container.InspectResponse{}
	for _, ref := range foreignReferences(services) {
		inspect, err := s.api.ContainerInspect(ctx, ref)
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect container %s: %w", ref, err)
		}
		if inspect.ContainerJSONBase != nil {
			referenced[inspect.ID] = inspect
		}
	}
	return referenced, nil
}

// foreignReferences are the distinct containers the replicas name in a container: namespace
// mode or in volumes_from that are no replica of the project, in discovery order.
func foreignReferences(services []model.Service) []string {
	own := map[string]bool{}
	for _, svc := range services {
		for _, replica := range svc.Containers {
			own[replica.ID] = true
			own[replica.Name] = true
		}
	}
	var refs []string
	add := func(ref string) {
		if ref != "" && !own[ref] && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	for _, svc := range services {
		for _, replica := range svc.Containers {
			if replica.Inspect.ContainerJSONBase == nil || replica.Inspect.HostConfig == nil {
				continue
			}
			hostConfig := replica.Inspect.HostConfig
			for _, mode := range []string{string(hostConfig.NetworkMode), string(hostConfig.IpcMode), string(hostConfig.PidMode)} {
				if ref, ok := strings.CutPrefix(mode, types.ContainerPrefix); ok {
					add(ref)
				}
			}
			for _, entry := range hostConfig.VolumesFrom {
				ref, _, _ := strings.Cut(entry, ":")
				add(ref)
			}
		}
	}
	return refs
}

// markInherited sets InheritedFrom on every mount a replica inherited through volumes_from: one its
// source — a replica of the project or a referenced container — mounts alike at the same target.
func markInherited(services []model.Service, referenced map[string]container.InspectResponse) {
	sources := map[string]container.InspectResponse{}
	for _, svc := range services {
		for _, replica := range svc.Containers {
			sources[replica.ID], sources[replica.Name] = replica.Inspect, replica.Inspect
		}
	}
	for id, inspect := range referenced {
		sources[id], sources[strings.TrimPrefix(inspect.Name, "/")] = inspect, inspect
	}
	for i := range services {
		svc := &services[i]
		for j := range svc.Volumes {
			if replica, ok := mountingReplica(*svc, svc.Volumes[j]); ok {
				svc.Volumes[j].InheritedFrom = inheritedFrom(replica.Inspect, svc.Volumes[j].Target, sources)
			}
		}
	}
}

// mountingReplica is the replica a reported mount was read off: its own for an anonymous
// volume, the first otherwise.
func mountingReplica(svc model.Service, mount model.ServiceVolume) (model.Container, bool) {
	if mount.Type != model.VolumeTypeAnon {
		if len(svc.Containers) == 0 {
			return model.Container{}, false
		}
		return svc.Containers[0], true
	}
	at := slices.IndexFunc(svc.Containers, func(c model.Container) bool { return c.Number == mount.Replica })
	if at < 0 {
		return model.Container{}, false
	}
	return svc.Containers[at], true
}

// inheritedFrom is the id of the volumes_from source mounting the same data at target as the
// container does; empty when none does.
func inheritedFrom(inspect container.InspectResponse, target string, sources map[string]container.InspectResponse) string {
	if inspect.ContainerJSONBase == nil || inspect.HostConfig == nil {
		return ""
	}
	at := slices.IndexFunc(inspect.Mounts, func(m container.MountPoint) bool { return m.Destination == target })
	if at < 0 {
		return ""
	}
	ownMount := inspect.Mounts[at]
	for _, entry := range inspect.HostConfig.VolumesFrom {
		ref, _, _ := strings.Cut(entry, ":")
		source, ok := sources[ref]
		if ok && source.ContainerJSONBase != nil && slices.ContainsFunc(source.Mounts, func(m container.MountPoint) bool {
			return m.Type == ownMount.Type && m.Name == ownMount.Name && m.Source == ownMount.Source && m.Destination == ownMount.Destination
		}) {
			return source.ID
		}
	}
	return ""
}

// newNamedVolumes lists the volumes compose owns, an unmounted one included, plus the external
// ones only a mount reveals, each with its users filled in.
func (s *Inspector) newNamedVolumes(services []model.Service, volumeIndex map[string]volume.Volume) []model.NamedVolume {
	users := newVolumeUsers(services)

	volumes := make([]model.NamedVolume, 0, len(volumeIndex))
	for name, dockerVolume := range volumeIndex {
		if !s.owns(dockerVolume) {
			continue
		}
		volumes = append(volumes, model.NamedVolume{
			Key:        dockerVolume.Labels[api.VolumeLabel],
			Name:       name,
			UsedBy:     users[name],
			Labels:     dockerVolume.Labels,
			Driver:     dockerVolume.Driver,
			DriverOpts: dockerVolume.Options,
		})
	}
	for name := range users {
		if !s.owns(volumeIndex[name]) {
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

// newVolumeUsers inverts the service->volume relation, keyed by docker name and sorted by
// (service, target); an identical mount declared twice is one reference.
func newVolumeUsers(services []model.Service) map[string][]model.ServiceReference {
	users := map[string][]model.ServiceReference{}
	for _, svc := range services {
		for _, mount := range svc.Volumes {
			if mount.Type != model.VolumeTypeNamed {
				continue
			}
			ref := model.ServiceReference{Service: svc.Name, Target: mount.Target, ReadOnly: mount.ReadOnly}
			if !slices.Contains(users[mount.VolumeName], ref) {
				users[mount.VolumeName] = append(users[mount.VolumeName], ref)
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
