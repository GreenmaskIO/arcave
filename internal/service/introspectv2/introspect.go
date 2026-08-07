package introspect

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

type composeProjectLoader interface {
	Load(ctx context.Context, files ...string) (*types.Project, error)
}

// Inspector answers "what does this compose project declare?" from the files
// alone — no arcaved, no side effects. It reports volumes at two levels and
// decides nothing: every mount type compose knows is reported verbatim, and what
// a savepoint captures is the consumer's call.
//
// Its one field is the loader seam; a test drives Introspect off an in-memory
// *types.Project.
type Inspector struct {
	loader composeProjectLoader
}

func NewInspector(loader composeProjectLoader) *Inspector {
	return &Inspector{loader: loader}
}

func (c *Inspector) Introspect(ctx context.Context, files ...string) (model.IntrospectionResult, error) {
	project, err := c.loader.Load(ctx, files...)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("loading docker compose file(s): %w", err)
	}
	return introspect(project)
}

// introspect projects a compose project in two passes: classify every service's
// mounts, then aggregate the named volumes from that output — not from the
// compose project again, so the two levels cannot disagree. Everything it
// returns is sorted.
func introspect(project *types.Project) (model.IntrospectionResult, error) {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make([]model.Service, 0, len(names))
	for _, name := range names {
		service, err := introspectService(project, name, project.Services[name])
		if err != nil {
			return model.IntrospectionResult{}, err
		}
		services = append(services, service)
	}

	return model.IntrospectionResult{
		Project: model.Project{
			Name:     project.Name,
			Services: services,
			Volumes:  namedVolumes(project, services),
		},
	}, nil
}

// introspectService projects one service. The name comes from the map key, not
// ServiceConfig.Name: the loader fills that field, a hand-built project need not.
func introspectService(project *types.Project, name string, svc types.ServiceConfig) (model.Service, error) {
	volumes := make([]model.ServiceVolume, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		volume, err := introspectVolume(project, name, v)
		if err != nil {
			return model.Service{}, err
		}
		volumes = append(volumes, volume)
	}
	return model.Service{
		Name:    name,
		Image:   svc.Image,
		Labels:  svc.Labels,
		Volumes: volumes,
	}, nil
}

// introspectVolume classifies one mount and copies its facts. Compose's "volume"
// splits into named and anonymous — the source tells them apart — and every other
// type maps one to one; an unrecognised one is VolumeTypeUnknown, diagnosed by
// RawType. Nothing is dropped: what a savepoint captures is decided elsewhere.
//
// Each branch builds the whole value in one literal, repeating the shared fields
// on purpose: a base value the branches overwrite reads as a diff against
// something nowhere written down, and hides which fields a type actually has.
//
// A named mount whose volume the project does not declare, or declares without a
// resolved docker name, is an error rather than an entry with an empty
// VolumeName: that name is the volume's identity, and empty ones would dedup
// into a single backup. compose-go rejects the first case at load, so this only
// fires for a project built by hand.
func introspectVolume(project *types.Project, service string, v types.ServiceVolumeConfig) (model.ServiceVolume, error) {
	switch v.Type {
	case types.VolumeTypeVolume:
		if v.Source == "" {
			return model.ServiceVolume{
				Type:     model.VolumeTypeAnon,
				RawType:  v.Type,
				Source:   "",
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
				Volume:   copyVolumeMount(v.Volume),
			}, nil
		}
		cfg, ok := project.Volumes[v.Source]
		if !ok {
			return model.ServiceVolume{}, fmt.Errorf("service %q mounts undeclared volume %q", service, v.Source)
		}
		if cfg.Name == "" {
			return model.ServiceVolume{}, fmt.Errorf("service %q mounts volume %q with no resolved docker name", service, v.Source)
		}
		return model.ServiceVolume{
			Type:       model.VolumeTypeNamed,
			RawType:    v.Type,
			Source:     v.Source,
			Target:     v.Target,
			VolumeName: cfg.Name,
			ReadOnly:   v.ReadOnly,
			Volume:     copyVolumeMount(v.Volume),
		}, nil

	case types.VolumeTypeBind:
		return model.ServiceVolume{
			Type:     model.VolumeTypeBind,
			RawType:  v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
			Bind:     copyBindMount(v.Bind),
		}, nil

	case types.VolumeTypeTmpfs:
		return model.ServiceVolume{
			Type:     model.VolumeTypeTmpfs,
			RawType:  v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
			Tmpfs:    copyTmpfsMount(v.Tmpfs),
		}, nil

	case types.VolumeTypeNamedPipe:
		return model.ServiceVolume{
			Type:     model.VolumeTypeNPipe,
			RawType:  v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		}, nil

	case types.VolumeTypeCluster:
		return model.ServiceVolume{
			Type:     model.VolumeTypeCluster,
			RawType:  v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		}, nil

	case types.VolumeTypeImage:
		return model.ServiceVolume{
			Type:     model.VolumeTypeImage,
			RawType:  v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
			Image:    copyImageMount(v.Image),
		}, nil

	default:
		return model.ServiceVolume{
			Type:     model.VolumeTypeUnknown,
			RawType:  v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		}, nil
	}
}

// The four copy helpers mirror one compose options struct each. nil stays nil,
// so "no options declared" stays distinguishable from "declared, all zero".

func copyVolumeMount(volume *types.ServiceVolumeVolume) *model.VolumeMount {
	if volume == nil {
		return nil
	}
	return &model.VolumeMount{
		NoCopy:  volume.NoCopy,
		Subpath: volume.Subpath,
		Labels:  volume.Labels,
	}
}

func copyBindMount(bind *types.ServiceVolumeBind) *model.BindMount {
	if bind == nil {
		return nil
	}
	return &model.BindMount{
		SELinux:        bind.SELinux,
		Propagation:    bind.Propagation,
		CreateHostPath: bind.CreateHostPath,
		Recursive:      bind.Recursive,
	}
}

func copyTmpfsMount(tmpfs *types.ServiceVolumeTmpfs) *model.TmpfsMount {
	if tmpfs == nil {
		return nil
	}
	return &model.TmpfsMount{
		SizeBytes: int64(tmpfs.Size),
		Mode:      tmpfs.Mode,
	}
}

func copyImageMount(image *types.ServiceVolumeImage) *model.ImageMount {
	if image == nil {
		return nil
	}
	return &model.ImageMount{Subpath: image.SubPath}
}

// namedVolumes lists the volumes: section — a volume mounted by nobody still
// exists, docker creates it — with the users of each filled in.
func namedVolumes(project *types.Project, services []model.Service) []model.NamedVolume {
	users := volumeUsers(services)

	volumes := make([]model.NamedVolume, 0, len(project.Volumes))
	for key, cfg := range project.Volumes {
		volumes = append(volumes, newNamedVolume(key, cfg, users[key]))
	}
	slices.SortFunc(volumes, func(a, b model.NamedVolume) int {
		return strings.Compare(a.Key, b.Key)
	})
	return volumes
}

// volumeUsers inverts the service->volume relation: which mounts each compose
// volume key has, and how the data is reachable through each. Two mounts
// differing in target, read_only or subpath are two references — the restore
// planner picks between them on exactly those — but an identical one declared
// twice is one. Each list comes out sorted by (service, target).
func volumeUsers(services []model.Service) map[string][]model.ServiceReference {
	users := make(map[string][]model.ServiceReference)
	for _, svc := range services {
		for _, v := range svc.Volumes {
			if v.Type != model.VolumeTypeNamed {
				continue
			}
			var subpath string
			if v.Volume != nil {
				subpath = v.Volume.Subpath
			}
			reference := model.ServiceReference{
				Service:  svc.Name,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
				Subpath:  subpath,
			}
			if slices.Contains(users[v.Source], reference) {
				continue
			}
			users[v.Source] = append(users[v.Source], reference)
		}
	}
	// Stable: (service, target) is not a total order — two mounts differing only
	// in read_only or subpath tie on it — and declaration order is the tiebreak
	// that needs no second sort key invented for it.
	for _, references := range users {
		slices.SortStableFunc(references, func(a, b model.ServiceReference) int {
			return cmp.Or(strings.Compare(a.Service, b.Service), strings.Compare(a.Target, b.Target))
		})
	}
	return users
}

// newNamedVolume copies the volumes: entry verbatim — labels, driver and driver
// options included — interpreting none of it.
func newNamedVolume(key string, cfg types.VolumeConfig, usedBy []model.ServiceReference) model.NamedVolume {
	return model.NamedVolume{
		Key:        key,
		Name:       cfg.Name,
		External:   bool(cfg.External),
		UsedBy:     usedBy,
		Labels:     cfg.Labels,
		Driver:     cfg.Driver,
		DriverOpts: cfg.DriverOpts,
	}
}
