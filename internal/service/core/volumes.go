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

// skipReason is why a mount is not a unit of backup, as a code and as prose.
type skipReason struct {
	code   model.SkipReason
	detail string
}

// skipReasons says why a mount type is not a unit of backup.
var skipReasons = map[model.VolumeType]skipReason{
	model.VolumeTypeTmpfs:   {model.SkipReasonTmpfs, "non-persistent (tmpfs lives in RAM)"},
	model.VolumeTypeNPipe:   {model.SkipReasonNPipe, "a named pipe is a channel, not data"},
	model.VolumeTypeImage:   {model.SkipReasonImage, "read-only image content, reproducible from the image"},
	model.VolumeTypeCluster: {model.SkipReasonCluster, "CSI volume: data lives outside the docker daemon"},
	model.VolumeTypeUnknown: {model.SkipReasonUnknownType, "unknown mount type"},
}

// inheritedSkipReason skips a mount inherited from a container the project does not stop or capture.
var inheritedSkipReason = skipReason{model.SkipReasonInheritedFromOutside, "inherited from a container outside the project"}

// resolveBackupVolumes returns the units a savepoint of the introspected compose project
// covers, and the mounts and services it leaves out. Every workflow comes through
// here, so create and rollback cannot differ.
func (s *Service) resolveBackupVolumes(
	ctx context.Context, res model.IntrospectionResult,
) ([]model.Volume, model.NotCovered, error) {
	volumes, notCovered, err := FindBackupVolumes(res, s.defaultBackend)
	if err != nil {
		return nil, model.NotCovered{}, fmt.Errorf("deciding which volumes to back up: %w", err)
	}

	log := zerolog.Ctx(ctx)
	for _, sk := range notCovered.Mounts {
		log.Warn().
			Str("service", sk.Service).
			Str("target", sk.Target).
			Str("raw_type", sk.RawType).
			Str("reason", sk.Detail).
			Msg("volume will not be backed up")
	}
	for _, svc := range notCovered.ServicesWithoutVolumes {
		log.Warn().Str("service", svc).Msg("service has no volume to back up; only its writable layer is captured")
	}
	return volumes, notCovered, nil
}

// FindBackupVolumes decides what a savepoint captures — which units, with which
// backend — plus what it leaves out: the mounts it skips and why, and the services
// no unit belongs to. Introspection reports facts; the policy lives here. Both are
// returned rather than logged, so every adapter renders them as it likes.
//
// Today it applies the defaults only: every named volume and every bind or
// anonymous mount not inherited through volumes_from, all on backend,
// deduplicated by ObjectID. The one place ComposeProject is stamped.
//
// It is also where an ObjectID collision is caught: this is the only level that
// sees every volume of the project, so two identities rendering one id are
// named here as an error rather than quietly merged into one unit.
//
// backend is a parameter because it is project config (snapshot.backend),
// resolved at the DI root. A per-volume choice would plug in here.
func FindBackupVolumes(
	res model.IntrospectionResult, backend model.SnapshotBackendKind,
) ([]model.Volume, model.NotCovered, error) {
	var (
		volumes []model.Volume
		skipped []model.SkippedVolume
	)
	seen := map[string]volumeIdentity{}
	covered := map[string]bool{}

	// Named volumes come from the project level: a volume three services mount is
	// one unit, and only this level knows every mount of it.
	for _, named := range res.Project.Volumes {
		for _, ref := range named.UsedBy {
			covered[ref.Service] = true
		}
		duplicate, err := claimObjectID(seen, named.ObjectID(), volumeIdentity{dockerName: named.Name})
		if err != nil {
			return nil, model.NotCovered{}, err
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

	// Bind and anonymous mounts belong to the service that declares them; one inherited
	// through volumes_from belongs to its source.
	inProject := projectContainers(res.Project.Services)
	for _, svc := range res.Project.Services {
		for _, sv := range svc.Volumes {
			if sv.Type == model.VolumeTypeNamed {
				// Only a reference to the unit already taken above.
				covered[svc.Name] = true
				continue
			}
			if sv.InheritedFrom != "" {
				if inProject[sv.InheritedFrom] {
					// Captured through its source.
					covered[svc.Name] = true
				} else {
					skipped = append(skipped, skippedVolume(svc.Name, sv, inheritedSkipReason))
				}
				continue
			}
			switch sv.Type {
			case model.VolumeTypeBind, model.VolumeTypeAnon:
				// A unit of its own.
				covered[svc.Name] = true
			default:
				skipped = append(skipped, skippedVolume(svc.Name, sv, reasonFor(sv.Type)))
				continue
			}

			objectID := sv.ObjectID(svc.Name)
			duplicate, err := claimObjectID(seen, objectID,
				volumeIdentity{service: svc.Name, replica: sv.Replica, target: sv.Target})
			if err != nil {
				return nil, model.NotCovered{}, err
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

	var uncovered []string
	for _, svc := range res.Project.Services {
		if !covered[svc.Name] {
			uncovered = append(uncovered, svc.Name)
		}
	}
	return volumes, model.NotCovered{Mounts: skipped, ServicesWithoutVolumes: uncovered}, nil
}

// skippedVolume reports one mount of service left out for reason.
func skippedVolume(service string, sv model.ServiceVolume, reason skipReason) model.SkippedVolume {
	return model.SkippedVolume{
		Service: service,
		Target:  sv.Target,
		RawType: sv.RawType,
		Reason:  reason.code,
		Detail:  reason.detail,
	}
}

// projectContainers are the ids of the project's replicas.
func projectContainers(services []model.Service) map[string]bool {
	ids := map[string]bool{}
	for _, svc := range services {
		for _, c := range svc.Containers {
			ids[c.ID] = true
		}
	}
	return ids
}

// volumeIdentity is the facts an ObjectID is rendered from: docker name (named),
// service and target (bind), plus replica (anonymous). Comparable.
type volumeIdentity struct {
	dockerName string
	service    string
	replica    int
	target     string
}

func (id volumeIdentity) String() string {
	switch {
	case id.dockerName != "":
		return fmt.Sprintf("named volume %q", id.dockerName)
	case id.replica != 0:
		return fmt.Sprintf("service %q replica %d at %q", id.service, id.replica, id.target)
	default:
		return fmt.Sprintf("service %q at %q", id.service, id.target)
	}
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

// reasonFor falls back to the unknown-type reason, so a VolumeType missing from
// the table is still reported rather than dropped.
func reasonFor(volumeType model.VolumeType) skipReason {
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
		Replica:  sv.Replica,
		Target:   sv.Target,
		ReadOnly: sv.ReadOnly,
		Subpath:  subpath,
	}
}
