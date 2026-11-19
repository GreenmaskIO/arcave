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

package captureplan

import (
	"context"
	"errors"
	"fmt"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"

	"github.com/moby/moby/api/types/mount"
	"github.com/rs/zerolog"
)

// errObjectIDCollision refuses a project whose volumes render one ObjectID from
// two different identities. An ObjectID is one path segment, so a target is
// flattened into it and {app, /var/lib} meets {app-var, /lib} there; the two
// would share a repository and a capture directory, each overwriting the other.
var errObjectIDCollision = errors.New("two volumes address the same object id")

// skipReason is why a mount is not a unit of backup, as a code and as prose.
type skipReason struct {
	code   composemodel.SkipReason
	detail string
}

// skipReasons says why a mount type is not a unit of backup.
var skipReasons = map[composemodel.VolumeType]skipReason{
	composemodel.VolumeTypeTmpfs:   {composemodel.SkipReasonTmpfs, "non-persistent (tmpfs lives in RAM)"},
	composemodel.VolumeTypeNPipe:   {composemodel.SkipReasonNPipe, "a named pipe is a channel, not data"},
	composemodel.VolumeTypeImage:   {composemodel.SkipReasonImage, "read-only image content, reproducible from the image"},
	composemodel.VolumeTypeCluster: {composemodel.SkipReasonCluster, "CSI volume: data lives outside the docker daemon"},
	composemodel.VolumeTypeUnknown: {composemodel.SkipReasonUnknownType, "unknown mount type"},
}

// inheritedSkipReason skips a mount inherited from a container the project does not stop or capture.
var inheritedSkipReason = skipReason{composemodel.SkipReasonInheritedFromOutside, "inherited from a container outside the project"}

// resolveBackupVolumes returns the units a savepoint of the introspected compose project
// covers, and the mounts and services it leaves out. Every workflow comes through
// here, so create and rollback cannot differ.
func resolveBackupVolumes(
	ctx context.Context, introspection composemodel.IntrospectionResult, capturePolicy composemodel.CapturePolicy,
) ([]composemodel.Volume, composemodel.NotCovered, error) {
	volumes, notCovered, err := findBackupVolumes(introspection, capturePolicy)
	if err != nil {
		return nil, composemodel.NotCovered{}, fmt.Errorf("deciding which volumes to back up: %w", err)
	}

	// Info, not Warn: what is left out is in the record's not_covered, and the result says so.
	log := zerolog.Ctx(ctx)
	for _, skippedMount := range notCovered.Mounts {
		event := log.Info()
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
		if skippedMount.Reason == composemodel.SkipReasonExcluded &&
			skippedMount.Volume == "" && skippedMount.RawType == string(mount.TypeVolume) {
			msg = "volume will not be backed up; a rollback to this savepoint recreates it empty"
		}
		event.Str("reason", skippedMount.Detail).Msg(msg)
	}
	for _, serviceName := range notCovered.ServicesWithoutVolumes {
		log.Info().Str("service", serviceName).Msg("service has no volume to back up")
	}
	return volumes, notCovered, nil
}

// findBackupVolumes decides, as capturePolicy says, which units a savepoint captures with
// which backend, and what it leaves out; two units rendering one ObjectID are an error.
func findBackupVolumes(
	introspection composemodel.IntrospectionResult, capturePolicy composemodel.CapturePolicy,
) ([]composemodel.Volume, composemodel.NotCovered, error) {
	var (
		volumes []composemodel.Volume
		skipped []composemodel.SkippedVolume
	)
	registered := map[string]volumeIdentity{}
	covered := map[string]bool{}
	excludedNamed := map[string]bool{}
	// excludedFrom are the services a label left a volume of out: not "without volumes".
	excludedFrom := map[string]bool{}

	// Named volumes come from the project level: a volume three services mount is
	// one unit, and only this level knows every mount of it.
	for _, named := range introspection.Project.Volumes {
		decision := capturePolicy.DecideNamedVolume(named.Name)
		if !decision.Capture {
			excludedNamed[named.Name] = true
			skipped = append(skipped, composemodel.SkippedVolume{
				Volume:  named.Name,
				RawType: string(mount.TypeVolume),
				Reason:  composemodel.SkipReasonExcluded,
				Detail:  decision.Source,
			})
			continue
		}
		for _, ref := range named.UsedBy {
			covered[ref.Service] = true
		}
		namedIdentity := volumeIdentity{dockerName: named.Name}
		duplicate, err := isObjectIDRegistered(registered, named.ObjectID(), namedIdentity)
		if err != nil {
			return nil, composemodel.NotCovered{}, err
		}
		if duplicate {
			continue
		}
		registered[named.ObjectID()] = namedIdentity
		volumes = append(volumes, composemodel.Volume{
			ObjectID:       named.ObjectID(),
			ComposeProject: introspection.Project.Name,
			Type:           composemodel.VolumeTypeNamed,
			Backend:        decision.Backend,
			Named: &composemodel.VolumePropertiesNamed{
				VolumeName: named.Name,
				UsedBy:     named.UsedBy,
			},
		})
	}

	// Bind and anonymous mounts belong to the service that declares them; one inherited
	// through volumes_from belongs to its source.
	inProject := projectContainers(introspection.Project.Services)
	for _, service := range introspection.Project.Services {
		for _, serviceVolume := range service.Volumes {
			if serviceVolume.Type == composemodel.VolumeTypeNamed {
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
			case composemodel.VolumeTypeBind, composemodel.VolumeTypeAnon:
				// A unit of its own.
			default:
				skipped = append(skipped, newSkippedVolume(service.Name, serviceVolume, reasonFor(serviceVolume.Type)))
				continue
			}

			decision := capturePolicy.DecideMount(composemodel.MountRef{
				Service: service.Name,
				Replica: serviceVolume.Replica,
				Target:  serviceVolume.Target,
			})
			if !decision.Capture {
				excludedFrom[service.Name] = true
				skipped = append(skipped, newSkippedVolume(
					service.Name,
					serviceVolume,
					skipReason{composemodel.SkipReasonExcluded, decision.Source},
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
			duplicate, err := isObjectIDRegistered(registered, objectID, mountIdentity)
			if err != nil {
				return nil, composemodel.NotCovered{}, err
			}
			if duplicate {
				continue
			}
			registered[objectID] = mountIdentity
			volumes = append(volumes, composemodel.Volume{
				ObjectID:       objectID,
				ComposeProject: introspection.Project.Name,
				Type:           serviceVolume.Type,
				Backend:        decision.Backend,
				Bind:           newBindProperties(service.Name, serviceVolume),
				Anon:           newAnonProperties(service.Name, serviceVolume),
			})
		}
	}

	var uncovered []string
	for _, service := range introspection.Project.Services {
		if !covered[service.Name] && !excludedFrom[service.Name] {
			uncovered = append(uncovered, service.Name)
		}
	}
	return volumes, composemodel.NotCovered{Mounts: skipped, ServicesWithoutVolumes: uncovered}, nil
}

// newSkippedVolume reports one mount of service left out for reason.
func newSkippedVolume(service string, serviceVolume composemodel.ServiceVolume, reason skipReason) composemodel.SkippedVolume {
	skipped := composemodel.SkippedVolume{
		Service: service,
		Target:  serviceVolume.Target,
		RawType: serviceVolume.RawType,
		Reason:  reason.code,
		Detail:  reason.detail,
	}
	if serviceVolume.Type == composemodel.VolumeTypeAnon {
		skipped.Replica = serviceVolume.Replica
	}
	return skipped
}

// projectContainers are the ids of the project's replicas.
func projectContainers(services []composemodel.Service) map[string]bool {
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
func reasonFor(volumeType composemodel.VolumeType) skipReason {
	if reason, ok := skipReasons[volumeType]; ok {
		return reason
	}
	return skipReasons[composemodel.VolumeTypeUnknown]
}

// newBindProperties and newAnonProperties each fill their arm or return nil, so
// exactly one arm of the Volume literal above ends up non-nil.
func newBindProperties(service string, serviceVolume composemodel.ServiceVolume) *composemodel.VolumePropertiesBind {
	if serviceVolume.Type != composemodel.VolumeTypeBind {
		return nil
	}
	return &composemodel.VolumePropertiesBind{
		Service:  service,
		Target:   serviceVolume.Target,
		Source:   serviceVolume.Source,
		ReadOnly: serviceVolume.ReadOnly,
	}
}

func newAnonProperties(service string, serviceVolume composemodel.ServiceVolume) *composemodel.VolumePropertiesAnon {
	if serviceVolume.Type != composemodel.VolumeTypeAnon {
		return nil
	}
	var subpath string
	if serviceVolume.Volume != nil {
		subpath = serviceVolume.Volume.Subpath
	}
	return &composemodel.VolumePropertiesAnon{
		Service:  service,
		Replica:  serviceVolume.Replica,
		Target:   serviceVolume.Target,
		ReadOnly: serviceVolume.ReadOnly,
		Subpath:  subpath,
	}
}
