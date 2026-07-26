package introspect

import (
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
// alone — no daemon, no side effects. It reports volumes at two levels and
// decides nothing: what a savepoint captures is the consumer's call.
//
// Its one field is the loader seam; a test drives Inspect off an in-memory
// *types.Project.
type Inspector struct {
	loader composeProjectLoader
}

func NewInspector(loader composeProjectLoader) *Inspector {
	return &Inspector{loader: loader}
}

func (c *Inspector) Inspect(ctx context.Context, files ...string) (model.InspectionResult, error) {
	project, err := c.loader.Load(ctx, files...)
	if err != nil {
		return model.InspectionResult{}, fmt.Errorf("loading docker compose file(s): %w", err)
	}
	return introspect(project), nil
}

// introspect projects a compose project in two passes: classify every service's
// mounts, then aggregate the named volumes from that output — not from the
// compose project again, so the two levels cannot disagree. Everything it
// returns is sorted.
func introspect(project *types.Project) model.InspectionResult {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make([]model.Service, 0, len(names))
	for _, name := range names {
		services = append(services, introspectService(project, name, project.Services[name]))
	}

	return model.InspectionResult{
		Project: model.Project{
			Name:     project.Name,
			Services: services,
			Volumes:  namedVolumes(project, services),
		},
	}
}

// introspectService projects one service. The name comes from the map key, which
// is set whoever built the project — the loader fills ServiceConfig.Name, a
// hand-built project in a test need not.
func introspectService(project *types.Project, name string, svc types.ServiceConfig) model.Service {
	volumes := make([]model.ServiceVolume, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		volumes = append(volumes, introspectVolume(project, v))
	}
	return model.Service{
		Name:    name,
		Image:   svc.Image,
		Labels:  svc.Labels,
		Volumes: volumes,
	}
}

// introspectVolume classifies one mount. Compose says "volume" for both named and
// anonymous — the source tells them apart. A named mount leaves the flags zero:
// its unit of backup is the project's NamedVolume, and VolumeName points at it.
func introspectVolume(project *types.Project, v types.ServiceVolumeConfig) model.ServiceVolume {
	switch v.Type {
	case types.VolumeTypeVolume:
		if v.Source == "" {
			return model.ServiceVolume{
				Type:         model.VolumeTypeAnon,
				RawType:      v.Type,
				Source:       "",
				Target:       v.Target,
				VolumeName:   "",
				NeedToBackup: true,
				Backend:      model.SnapshotBackendFullCopy,
			}
		}
		return model.ServiceVolume{
			Type:         model.VolumeTypeNamed,
			RawType:      v.Type,
			Source:       v.Source,
			Target:       v.Target,
			VolumeName:   project.Volumes[v.Source].Name,
			NeedToBackup: false,
			Backend:      "",
		}

	case types.VolumeTypeBind:
		return model.ServiceVolume{
			Type:         model.VolumeTypeBind,
			RawType:      v.Type,
			Source:       v.Source,
			Target:       v.Target,
			VolumeName:   "",
			NeedToBackup: true,
			Backend:      model.SnapshotBackendFullCopy,
		}

	default:
		return model.ServiceVolume{
			Type:         model.VolumeTypeUnsupported,
			RawType:      v.Type,
			Source:       "",
			Target:       v.Target,
			VolumeName:   "",
			NeedToBackup: false,
			Backend:      model.SnapshotBackendUnknown,
		}
	}
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

// volumeUsers inverts the service->volume relation: which services mount each
// compose volume key. Services arrive sorted, so each list comes out sorted.
func volumeUsers(services []model.Service) map[string][]string {
	users := make(map[string][]string)
	for _, svc := range services {
		for _, v := range svc.Volumes {
			// A service may mount the same volume twice; it is still one user.
			if v.Type != model.VolumeTypeNamed || slices.Contains(users[v.Source], svc.Name) {
				continue
			}
			users[v.Source] = append(users[v.Source], svc.Name)
		}
	}
	return users
}

func newNamedVolume(key string, cfg types.VolumeConfig, usedBy []string) model.NamedVolume {
	return model.NamedVolume{
		Key:          key,
		Name:         cfg.Name,
		External:     bool(cfg.External),
		UsedBy:       usedBy,
		NeedToBackup: true,
		Backend:      model.SnapshotBackendFullCopy,
	}
}
