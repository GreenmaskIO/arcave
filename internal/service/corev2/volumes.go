package corev2

import (
	"github.com/greenmaskio/arcave/internal/model"
)

// defaultBackend captures every unit arcave takes today. Choosing cdc, opting a
// volume out by label or warning on a non-local driver all plug in here.
const defaultBackend = model.SnapshotBackendFullCopy

// skipReasons says why a mount type is not a unit of backup: only named, bind
// and anonymous mounts carry persistent data arcave can reach.
var skipReasons = map[model.VolumeType]string{
	model.VolumeTypeTmpfs:   "non-persistent (tmpfs lives in RAM)",
	model.VolumeTypeNPipe:   "a named pipe is a channel, not data",
	model.VolumeTypeImage:   "read-only image content, reproducible from the image",
	model.VolumeTypeCluster: "CSI volume: data lives outside the docker daemon",
	model.VolumeTypeUnknown: "unknown mount type",
}

// FindBackupVolumes decides what a savepoint captures: which units to back up
// and with which backend, plus the mounts it leaves out and why. Introspection
// reports only facts, so this is the single place that policy lives. Skips are
// returned rather than logged, so every adapter can render them as it likes.
//
// Today it applies the defaults only: every named volume and every bind or
// anonymous mount, all full-copy, deduplicated by ObjectID.
func FindBackupVolumes(res model.IntrospectionResult) ([]model.Volume, []model.SkippedVolume, error) {
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
			ObjectID: named.ObjectID(),
			Type:     model.VolumeTypeNamed,
			Backend:  defaultBackend,
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
				ObjectID: objectID,
				Type:     sv.Type,
				Backend:  defaultBackend,
				Bind:     bindProperties(svc.Name, sv),
				Anon:     anonProperties(svc.Name, sv),
			})
		}
	}

	return volumes, skipped, nil
}

// skipReason falls back to the unknown-type reason, so a VolumeType missing from
// the table is still reported rather than silently dropped.
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
