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

// Package statusreport sums an observed compose project up for a status: what built it, every
// service and replica set against the deployed savepoint, its state, notices and table. Nothing
// here does I/O.
package statusreport

import (
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// Report is what a status says of a compose project.
type Report struct {
	State   composemodel.ComposeProjectState
	Notices []model.Notice
	Detail  composemodel.StatusDetail
}

// FindLatestDeployment is the latest build the containers carry; nil for none.
func FindLatestDeployment(observed composemodel.ObservedComposeProject) *model.SavepointDeployment {
	latest := findDeployments(observed).latest
	if latest == nil {
		return nil
	}
	return &latest.SavepointDeployment
}

// NewReport sets observed against its latest deployment and deployed, that deployment's savepoint
// as its record holds it, nil for none read; every age is counted from checkedAt.
func NewReport(
	observed composemodel.ObservedComposeProject, checkedAt time.Time, deployed *composemodel.Savepoint,
) Report {
	var (
		deployments = findDeployments(observed)
		services    = newServiceStatuses(observed, deployments)
		state       = newComposeProjectState(services)
	)
	return Report{
		State: state,
		Notices: newStatusNotices(statusFacts{
			checkedAt:   checkedAt,
			state:       state,
			deployments: deployments,
			savepoint:   deployed,
			services:    services,
		}),
		Detail: composemodel.StatusDetail{
			Source:   deployments.source,
			Replaced: countRecreated(deployments, services),
			Services: services,
		},
	}
}

// countRecreated counts the replicas recreated outside arcave since the deployment, none whose
// labels do not parse among them.
func countRecreated(deployments containerDeployments, services []composemodel.ServiceStatus) int {
	recreated := 0
	for _, service := range services {
		for _, replica := range service.Replicas {
			if isRecreated(replica) && !deployments.isUnreadable(service.Name, replica.Number) {
				recreated++
			}
		}
	}
	return recreated
}
