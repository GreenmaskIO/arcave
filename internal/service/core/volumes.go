package core

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// skipReasons says why a mount type is not a unit of backup.
var skipReasons = map[model.VolumeType]string{
	model.VolumeTypeTmpfs:   "non-persistent (tmpfs lives in RAM)",
	model.VolumeTypeNPipe:   "a named pipe is a channel, not data",
	model.VolumeTypeImage:   "read-only image content, reproducible from the image",
	model.VolumeTypeCluster: "CSI volume: data lives outside the docker daemon",
	model.VolumeTypeUnknown: "unknown mount type",
}

// resolveBackupVolumes reads the compose project and returns the units a
// savepoint of it covers. Every workflow comes through here: a create that
// resolved its own would capture volumes its rollback leaves alone. The skips
// are logged here so FindBackupVolumes and reachableVolumes stay pure.
func (s *Service) resolveBackupVolumes(ctx context.Context, composeFiles []string) ([]model.Volume, error) {
	res, err := s.composeInspector.Introspect(ctx, composeFiles...)
	if err != nil {
		return nil, fmt.Errorf("introspecting compose files: %w", err)
	}

	volumes, skipped, err := FindBackupVolumes(res, s.defaultBackend)
	if err != nil {
		return nil, fmt.Errorf("deciding which volumes to back up: %w", err)
	}
	volumes, unreachable := reachableVolumes(volumes)

	for _, sk := range append(skipped, unreachable...) {
		zerolog.Ctx(ctx).Warn().
			Str("service", sk.Service).
			Str("target", sk.Target).
			Str("raw_type", sk.RawType).
			Str("reason", sk.Reason).
			Msg("volume will not be backed up")
	}
	return volumes, nil
}

// FindBackupVolumes decides what a savepoint captures — which units, with which
// backend — plus the mounts it leaves out and why. Introspection reports facts;
// the policy lives here. Skips are returned rather than logged, so every adapter
// renders them as it likes.
//
// Today it applies the defaults only: every named volume and every bind or
// anonymous mount, all on backend, deduplicated by ObjectID. The one place
// ComposeProject is stamped.
//
// backend is a parameter because it is project config (snapshot.backend),
// resolved at the DI root. A per-volume choice would plug in here.
func FindBackupVolumes(
	res model.IntrospectionResult, backend model.SnapshotBackendKind,
) ([]model.Volume, []model.SkippedVolume, error) {
	var (
		volumes []model.Volume
		skipped []model.SkippedVolume
	)
	seen := map[string]struct{}{}

	// Named volumes come from the project level: a volume three services mount is
	// one unit, and only this level knows every mount of it.
	for _, named := range res.Project.Volumes {
		if _, ok := seen[named.ObjectID()]; ok {
			continue
		}
		seen[named.ObjectID()] = struct{}{}
		volumes = append(volumes, model.Volume{
			ObjectID:       named.ObjectID(),
			ComposeProject: res.Project.Name,
			Type:           model.VolumeTypeNamed,
			Backend:        backend,
			Named: &model.VolumePropertiesNamed{
				VolumeName: named.Name,
				UsedBy:     named.UsedBy,
			},
		})
	}

	// Bind and anonymous mounts belong to the service that declares them.
	for _, svc := range res.Project.Services {
		for _, sv := range svc.Volumes {
			switch sv.Type {
			case model.VolumeTypeNamed:
				// Only a reference to the unit already taken above.
				continue
			case model.VolumeTypeBind, model.VolumeTypeAnon:
				// A unit of its own.
			default:
				skipped = append(skipped, model.SkippedVolume{
					Service: svc.Name,
					Target:  sv.Target,
					RawType: sv.RawType,
					Reason:  skipReason(sv.Type),
				})
				continue
			}

			objectID := sv.ObjectID(svc.Name)
			if _, ok := seen[objectID]; ok {
				continue
			}
			seen[objectID] = struct{}{}
			volumes = append(volumes, model.Volume{
				ObjectID:       objectID,
				ComposeProject: res.Project.Name,
				Type:           sv.Type,
				Backend:        backend,
				Bind:           bindProperties(svc.Name, sv),
				Anon:           anonProperties(svc.Name, sv),
			})
		}
	}

	return volumes, skipped, nil
}

// skipReason falls back to the unknown-type reason, so a VolumeType missing from
// the table is still reported rather than dropped.
func skipReason(volumeType model.VolumeType) string {
	if reason, ok := skipReasons[volumeType]; ok {
		return reason
	}
	return skipReasons[model.VolumeTypeUnknown]
}

// bindProperties and anonProperties each fill their arm or return nil, so
// exactly one arm of the Volume literal above ends up non-nil.
func bindProperties(service string, sv model.ServiceVolume) *model.VolumePropertiesBind {
	if sv.Type != model.VolumeTypeBind {
		return nil
	}
	return &model.VolumePropertiesBind{
		Service:  service,
		Target:   sv.Target,
		Source:   sv.Source,
		ReadOnly: sv.ReadOnly,
	}
}

func anonProperties(service string, sv model.ServiceVolume) *model.VolumePropertiesAnon {
	if sv.Type != model.VolumeTypeAnon {
		return nil
	}
	var subpath string
	if sv.Volume != nil {
		subpath = sv.Volume.Subpath
	}
	return &model.VolumePropertiesAnon{
		Service:  service,
		Target:   sv.Target,
		ReadOnly: sv.ReadOnly,
		Subpath:  subpath,
	}
}

// reachableVolumes drops the units today's backends cannot get at, returning
// them as skips. Kept out of FindBackupVolumes: reachability is the backends'
// property, not the domain's. One rule covers both today, and splits the day one
// of them reaches what the other cannot.
func reachableVolumes(volumes []model.Volume) ([]model.Volume, []model.SkippedVolume) {
	var (
		kept    []model.Volume
		skipped []model.SkippedVolume
	)
	for _, v := range volumes {
		reason := unreachableReason(v)
		if reason == "" {
			kept = append(kept, v)
			continue
		}
		// No service or target to name — that is the diagnosis — so the reason
		// carries the volume's own name instead.
		skipped = append(skipped, model.SkippedVolume{RawType: "volume", Reason: reason})
	}
	return kept, skipped
}

// unreachableReason returns why no backend can reach v's data, or "".
// Only a named volume can be unreachable: a bind or anonymous one is its own
// single mount.
func unreachableReason(v model.Volume) string {
	if v.Type != model.VolumeTypeNamed || v.Named == nil {
		return ""
	}
	if len(v.Named.UsedBy) == 0 {
		return fmt.Sprintf("named volume %s is mounted by no service; there is no container to reach its data through", v.Named.VolumeName)
	}
	for _, ref := range v.Named.UsedBy {
		if ref.Subpath == "" {
			return ""
		}
	}
	return fmt.Sprintf("named volume %s is only ever mounted at a subpath; backing it up would capture part of the volume", v.Named.VolumeName)
}
