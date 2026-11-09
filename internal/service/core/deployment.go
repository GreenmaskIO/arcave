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
	"time"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// newSavepointDeployment is the build of project from savepoint of session in arcaveProject this
// process is about to run, its replicas the ones recorded.
func newSavepointDeployment(
	arcaveProject model.ArcaveProjectName, session model.SessionRef, savepoint model.Savepoint,
	project *types.Project, recorded []recordedReplica,
) model.SavepointDeployment {
	return model.SavepointDeployment{
		DeployedAt:    time.Now(),
		ArcaveProject: arcaveProject,
		Session:       session,
		Savepoint: model.DeployedSavepoint{
			ID:               savepoint.ID,
			Name:             savepoint.Name,
			CreatedAt:        savepoint.CreatedAt,
			IsSafetySnapshot: savepoint.IsSafetySnapshot(),
		},
		ServiceReplicas: newDeployedReplicas(project, recorded),
	}
}

// newDeployedReplicas are the replica numbers each service of project is built with: the recorded
// ones, else the service's scale; a service scaled to none is left out.
func newDeployedReplicas(project *types.Project, recorded []recordedReplica) map[string][]int {
	numbers := map[string][]int{}
	for _, replica := range recorded {
		numbers[replica.service] = append(numbers[replica.service], replica.number)
	}
	replicas := make(map[string][]int, len(project.Services))
	for name, service := range project.Services {
		serviceNumbers := slices.Sorted(slices.Values(numbers[name]))
		if len(serviceNumbers) == 0 {
			for number := range service.GetScale() {
				serviceNumbers = append(serviceNumbers, number+1)
			}
		}
		if len(serviceNumbers) > 0 {
			replicas[name] = serviceNumbers
		}
	}
	return replicas
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
