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

	"github.com/docker/docker/api/types/mount"
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
	ctx context.Context, res model.IntrospectionResult, capturePolicy model.CapturePolicy,
) ([]model.Volume, model.NotCovered, error) {
	volumes, notCovered, err := FindBackupVolumes(res, capturePolicy)
	if err != nil {
		return nil, model.NotCovered{}, fmt.Errorf("deciding which volumes to back up: %w", err)
	}

	log := zerolog.Ctx(ctx)
	for _, skippedMount := range notCovered.Mounts {
		event := log.Warn()
		if skippedMount.Volume != "" {
			event = event.Str("volume", skippedMount.Volume)
		}
		if skippedMount.Service != "" {
			event = event.Str("service", skippedMount.Service).
				Str("target", skippedMount.Target).
				Str("raw_type", skippedMount.RawType)
		}
		msg := "volume will not be backed up"
		// A mount of compose type volume is an anonymous one: a named volume is left out whole.
		if skippedMount.Reason == model.SkipReasonExcluded &&
			skippedMount.Volume == "" && skippedMount.RawType == string(mount.TypeVolume) {
			msg = "volume will not be backed up; a rollback to this savepoint recreates it empty"
		}
		event.Str("reason", skippedMount.Detail).Msg(msg)
	}
	for _, serviceName := range notCovered.ServicesWithoutVolumes {
		log.Warn().Str("service", serviceName).Msg("service has no volume to back up")
	}
	return volumes, notCovered, nil
}

// FindBackupVolumes decides, as capturePolicy says, which units a savepoint captures with
// which backend, and what it leaves out; two units rendering one ObjectID are an error.
func FindBackupVolumes(
	res model.IntrospectionResult, capturePolicy model.CapturePolicy,
) ([]model.Volume, model.NotCovered, error) {
	var (
		volumes []model.Volume
		skipped []model.SkippedVolume
	)
	registered := map[string]volumeIdentity{}
	covered := map[string]bool{}
	excludedNamed := map[string]bool{}
	// excludedFrom are the services a label left a volume of out: not "without volumes".
	excludedFrom := map[string]bool{}

	// Named volumes come from the project level: a volume three services mount is
	// one unit, and only this level knows every mount of it.
	for _, named := range res.Project.Volumes {
		decision := capturePolicy.DecideNamedVolume(named.Name)
		if !decision.Capture {
			excludedNamed[named.Name] = true
			skipped = append(skipped, model.SkippedVolume{
				Volume:  named.Name,
				RawType: string(mount.TypeVolume),
				Reason:  model.SkipReasonExcluded,
				Detail:  decision.Source,
			})
			continue
		}
		for _, ref := range named.UsedBy {
			covered[ref.Service] = true
		}
		namedIdentity := volumeIdentity{dockerName: named.Name}
		objectIDIsRegistered, err := isObjectIDRegistered(registered, named.ObjectID(), namedIdentity)
		if err != nil {
			return nil, model.NotCovered{}, err
		}
		if objectIDIsRegistered {
			continue
		}
		registered[named.ObjectID()] = namedIdentity
		volumes = append(volumes, model.Volume{
			ObjectID:       named.ObjectID(),
			ComposeProject: res.Project.Name,
			Type:           model.VolumeTypeNamed,
			Backend:        decision.Backend,
			Named: &model.VolumePropertiesNamed{
				VolumeName: named.Name,
				UsedBy:     named.UsedBy,
			},
		})
	}

	// Bind and anonymous mounts belong to the service that declares them; one inherited
	// through volumes_from belongs to its source.
	inProject := projectContainers(res.Project.Services)
	for _, service := range res.Project.Services {
		for _, serviceVolume := range service.Volumes {
			if serviceVolume.Type == model.VolumeTypeNamed {
				// Only a reference to the unit decided above.
				if excludedNamed[serviceVolume.VolumeName] {
					excludedFrom[service.Name] = true
				} else {
					covered[service.Name] = true
				}
				continue
			}
			if serviceVolume.InheritedFrom != "" {
				if inProject[serviceVolume.InheritedFrom] {
					// Captured through its source.
					covered[service.Name] = true
				} else {
					skipped = append(skipped, newSkippedVolume(service.Name, serviceVolume, inheritedSkipReason))
				}
				continue
			}
			switch serviceVolume.Type {
			case model.VolumeTypeBind, model.VolumeTypeAnon:
				// A unit of its own.
			default:
				skipped = append(skipped, newSkippedVolume(service.Name, serviceVolume, reasonFor(serviceVolume.Type)))
				continue
			}

			decision := capturePolicy.DecideMount(model.MountRef{
				Service: service.Name,
				Replica: serviceVolume.Replica,
				Target:  serviceVolume.Target,
			})
			if !decision.Capture {
				excludedFrom[service.Name] = true
				skipped = append(skipped, newSkippedVolume(
					service.Name,
					serviceVolume,
					skipReason{model.SkipReasonExcluded, decision.Source},
				))
				continue
			}
			covered[service.Name] = true

			objectID := serviceVolume.ObjectID(service.Name)
			mountIdentity := volumeIdentity{
				service: service.Name,
				replica: serviceVolume.Replica,
				target:  serviceVolume.Target,
			}
			isRegistered, err := isObjectIDRegistered(registered, objectID, mountIdentity)
			if err != nil {
				return nil, model.NotCovered{}, err
			}
			if isRegistered {
				continue
			}
			registered[objectID] = mountIdentity
			volumes = append(volumes, model.Volume{
				ObjectID:       objectID,
				ComposeProject: res.Project.Name,
				Type:           serviceVolume.Type,
				Backend:        decision.Backend,
				Bind:           bindProperties(service.Name, serviceVolume),
				Anon:           anonProperties(service.Name, serviceVolume),
			})
		}
	}

	var uncovered []string
	for _, service := range res.Project.Services {
		if !covered[service.Name] && !excludedFrom[service.Name] {
			uncovered = append(uncovered, service.Name)
		}
	}
	return volumes, model.NotCovered{Mounts: skipped, ServicesWithoutVolumes: uncovered}, nil
}

// newSkippedVolume reports one mount of service left out for reason.
func newSkippedVolume(service string, serviceVolume model.ServiceVolume, reason skipReason) model.SkippedVolume {
	skipped := model.SkippedVolume{
		Service: service,
		Target:  serviceVolume.Target,
		RawType: serviceVolume.RawType,
		Reason:  reason.code,
		Detail:  reason.detail,
	}
	if serviceVolume.Type == model.VolumeTypeAnon {
		skipped.Replica = serviceVolume.Replica
	}
	return skipped
}

// projectContainers are the ids of the project's replicas.
func projectContainers(services []model.Service) map[string]bool {
	ids := map[string]bool{}
	for _, service := range services {
		for _, container := range service.Containers {
			ids[container.ID] = true
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

// isObjectIDRegistered reports whether identity already registered objectID, a unit to
// skip. An objectID another identity registered is an error naming both: they would
// share one unit of backup and overwrite each other.
func isObjectIDRegistered(
	registered map[string]volumeIdentity, objectID string, identity volumeIdentity,
) (bool, error) {
	prior, ok := registered[objectID]
	switch {
	case !ok:
		return false, nil
	case prior == identity:
		return true, nil
	default:
		return false, fmt.Errorf("%w: %s and %s both address %q; rename one of them",
			errObjectIDCollision, prior, identity, objectID)
	}
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
func bindProperties(service string, serviceVolume model.ServiceVolume) *model.VolumePropertiesBind {
	if serviceVolume.Type != model.VolumeTypeBind {
		return nil
	}
	return &model.VolumePropertiesBind{
		Service:  service,
		Target:   serviceVolume.Target,
		Source:   serviceVolume.Source,
		ReadOnly: serviceVolume.ReadOnly,
	}
}

func anonProperties(service string, serviceVolume model.ServiceVolume) *model.VolumePropertiesAnon {
	if serviceVolume.Type != model.VolumeTypeAnon {
		return nil
	}
	var subpath string
	if serviceVolume.Volume != nil {
		subpath = serviceVolume.Volume.Subpath
	}
	return &model.VolumePropertiesAnon{
		Service:  service,
		Replica:  serviceVolume.Replica,
		Target:   serviceVolume.Target,
		ReadOnly: serviceVolume.ReadOnly,
		Subpath:  subpath,
	}
}
