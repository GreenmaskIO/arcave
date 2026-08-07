package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// errObjectIDCollision refuses a project whose volumes render one ObjectID from
// two different identities. An ObjectID is one path segment, so a target is
// flattened into it and {app, /var/lib} meets {app-var, /lib} there; the two
// would share a repository and a capture directory, each overwriting the other.
var errObjectIDCollision = errors.New("two volumes address the same object id")

// skipReasons says why a mount type is not a unit of backup.
var skipReasons = map[model.VolumeType]string{
	model.VolumeTypeTmpfs:   "non-persistent (tmpfs lives in RAM)",
	model.VolumeTypeNPipe:   "a named pipe is a channel, not data",
	model.VolumeTypeImage:   "read-only image content, reproducible from the image",
	model.VolumeTypeCluster: "CSI volume: data lives outside the docker arcaved",
	model.VolumeTypeUnknown: "unknown mount type",
}

// resolveBackupVolumes reads the compose project and returns the units a
// savepoint of it covers. Every workflow comes through here: a create that
// resolved its own would capture volumes its rollback leaves alone. The skips
// are logged here so FindBackupVolumes stays pure.
//
// Nothing is dropped for being unreachable any more: a backend addresses a
// volume directly, so a volume no container mounts — or mounts only at a
// subpath — is captured like any other, and a volume the arcaved does not have
// fails its own operation rather than disappearing from the set.
func (s *Service) resolveBackupVolumes(ctx context.Context, composeFiles []string) ([]model.Volume, error) {
	res, err := s.composeInspector.Introspect(ctx, composeFiles...)
	if err != nil {
		return nil, fmt.Errorf("introspecting compose files: %w", err)
	}

	volumes, skipped, err := FindBackupVolumes(res, s.defaultBackend)
	if err != nil {
		return nil, fmt.Errorf("deciding which volumes to back up: %w", err)
	}

	for _, sk := range skipped {
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
// It is also where an ObjectID collision is caught: this is the only level that
// sees every volume of the project, so two identities rendering one id are
// named here as an error rather than quietly merged into one unit.
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
	seen := map[string]volumeIdentity{}

	// Named volumes come from the project level: a volume three services mount is
	// one unit, and only this level knows every mount of it.
	for _, named := range res.Project.Volumes {
		duplicate, err := claimObjectID(seen, named.ObjectID(), volumeIdentity{dockerName: named.Name})
		if err != nil {
			return nil, nil, err
		}
		if duplicate {
			continue
		}
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
			duplicate, err := claimObjectID(seen, objectID, volumeIdentity{service: svc.Name, target: sv.Target})
			if err != nil {
				return nil, nil, err
			}
			if duplicate {
				continue
			}
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

// volumeIdentity is the facts an ObjectID is rendered from — a docker name for a
// named volume, a service and a target for a bind or anonymous one. Comparable,
// so two claims of one id are told apart by the identity behind them.
type volumeIdentity struct {
	dockerName string
	service    string
	target     string
}

func (id volumeIdentity) String() string {
	if id.dockerName != "" {
		return fmt.Sprintf("named volume %q", id.dockerName)
	}
	return fmt.Sprintf("service %q at %q", id.service, id.target)
}

// claimObjectID records that identity addresses objectID, reporting a repeat of
// the same identity as a duplicate to skip and a different one as an error
// naming both: they would share one unit of backup and overwrite each other.
func claimObjectID(seen map[string]volumeIdentity, objectID string, identity volumeIdentity) (bool, error) {
	prior, taken := seen[objectID]
	switch {
	case taken && prior == identity:
		return true, nil
	case taken:
		return false, fmt.Errorf("%w: %s and %s both address %q; rename one of them",
			errObjectIDCollision, prior, identity, objectID)
	}
	seen[objectID] = identity
	return false, nil
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
