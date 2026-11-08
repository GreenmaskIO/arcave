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
	"maps"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// newSavepointDeployment is the build of project from savepoint this process is about to run;
// revertedFrom is the savepoint a failed rollback was restoring, nil for a rollback.
func (s *Service) newSavepointDeployment(
	savepoint model.Savepoint, revertedFrom *model.Savepoint, project *types.Project,
) model.SavepointDeployment {
	origin, reverted := model.DeploymentOriginRollback, model.SavepointRef{}
	if revertedFrom != nil {
		origin, reverted = model.DeploymentOriginRevert, model.SavepointRef{
			ID:   revertedFrom.ID,
			Name: revertedFrom.Name,
		}
	}
	return model.SavepointDeployment{
		ID:            model.NewDeploymentID(),
		At:            s.now().UTC(),
		Origin:        origin,
		ArcaveProject: s.arcaveProject,
		Session: model.SessionRef{
			ID:   s.session,
			Name: s.sessionName,
		},
		Savepoint: model.DeployedSavepoint{
			ID:                savepoint.ID,
			Name:              savepoint.Name,
			Description:       savepoint.Description,
			CreatedAt:         savepoint.CreatedAt,
			SafetySnapshotFor: newSafetySnapshotFor(savepoint),
		},
		RevertedFrom:  reverted,
		Manifest:      newDeploymentManifest(savepoint, project),
		ArcaveVersion: s.runnerInfo.ArcaveVersion,
		Host:          s.runnerInfo.Host,
	}
}

// newSafetySnapshotFor is the savepoint the rollback a safety snapshot undoes restored; empty for a
// savepoint a user took.
func newSafetySnapshotFor(savepoint model.Savepoint) model.SavepointID {
	if !savepoint.IsSafetySnapshot() {
		return ""
	}
	return model.SavepointID(savepoint.Labels[model.SavepointLabelSafetySnapshotFor])
}

// newDeploymentManifest is what building project from savepoint runs: each service's recorded
// replicas, the scale for a service the savepoint recorded none of, and what it left out.
func newDeploymentManifest(savepoint model.Savepoint, project *types.Project) model.DeploymentManifest {
	var (
		recorded    = map[string][]int{}
		layers      = map[string][]int{}
		notRestored model.DeploymentNotRestored
	)
	for _, layer := range savepoint.WritableLayers {
		recorded[layer.Service] = append(recorded[layer.Service], layer.Replica)
		layers[layer.Service] = append(layers[layer.Service], layer.Replica)
	}
	if savepoint.NotCovered != nil {
		for _, skipped := range savepoint.NotCovered.WritableLayers {
			recorded[skipped.Service] = append(recorded[skipped.Service], skipped.Replica)
		}
		notRestored = model.DeploymentNotRestored{
			Mounts:         newExcludedMounts(savepoint.NotCovered.Mounts),
			WritableLayers: savepoint.NotCovered.WritableLayers,
		}
	}
	leftOut := model.DeploymentCounts{
		Volumes:        len(notRestored.Mounts),
		WritableLayers: len(notRestored.WritableLayers),
	}
	services := make(map[string]model.DeployedService, len(project.Services))
	for name, service := range project.Services {
		replicas := slices.Sorted(slices.Values(recorded[name]))
		if len(replicas) == 0 {
			for number := range service.GetScale() {
				replicas = append(replicas, number+1)
			}
		}
		services[name] = model.DeployedService{
			Replicas: replicas,
			Layers:   slices.Sorted(slices.Values(layers[name])),
		}
	}
	return model.DeploymentManifest{
		Services: services,
		Restored: model.DeploymentCounts{
			Volumes:        len(savepoint.Stats),
			WritableLayers: len(savepoint.WritableLayers),
		},
		LeftOut:     leftOut,
		NotRestored: notRestored,
	}
}

// newExcludedMounts are the mounts a label or the config left out of the savepoint.
func newExcludedMounts(mounts []model.SkippedVolume) []model.SkippedVolume {
	var excluded []model.SkippedVolume
	for _, mount := range mounts {
		if mount.Reason == model.SkipReasonExcluded {
			excluded = append(excluded, mount)
		}
	}
	return excluded
}

// withDeploymentLabels is a copy of project whose every service carries labels; the project and
// its services are left as they are.
func withDeploymentLabels(project *types.Project, labels map[string]string) *types.Project {
	labeled := *project
	labeled.Services = make(types.Services, len(project.Services))
	for name, service := range project.Services {
		merged := make(types.Labels, len(service.Labels)+len(labels))
		maps.Copy(merged, service.Labels)
		maps.Copy(merged, labels)
		service.Labels = merged
		labeled.Services[name] = service
	}
	return &labeled
}
