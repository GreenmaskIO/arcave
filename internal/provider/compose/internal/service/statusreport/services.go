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

package statusreport

import (
	"cmp"
	"maps"
	"slices"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// replicaStateRank orders the replica states from the best to the worst: a service is as bad as
// its worst replica, and completed only when every one is.
var replicaStateRank = map[composemodel.ReplicaState]int{
	composemodel.ReplicaStateCompleted:  0,
	composemodel.ReplicaStateRunning:    1,
	composemodel.ReplicaStateExited:     2,
	composemodel.ReplicaStateRestarting: 3,
	composemodel.ReplicaStateCreated:    4,
	composemodel.ReplicaStatePaused:     5,
	composemodel.ReplicaStateRemoving:   6,
	composemodel.ReplicaStateDead:       7,
	composemodel.ReplicaStateMissing:    8,
}

// replicaHealthRank orders the healthchecks from none to the worst.
var replicaHealthRank = map[composemodel.ReplicaHealth]int{
	"":                                  0,
	composemodel.ReplicaHealthHealthy:   1,
	composemodel.ReplicaHealthStarting:  2,
	composemodel.ReplicaHealthUnhealthy: 3,
}

// containerDeployments is what the containers' labels say built them.
type containerDeployments struct {
	// source is empty for a compose project with no container.
	source composemodel.DeploymentSource
	// latest is the latest deployment the containers carry; nil for none.
	latest *composemodel.Deployment
	// builtByLatest holds the id of every container that deployment built.
	builtByLatest map[string]bool
	// unreadable are the replicas whose deployment labels do not parse.
	unreadable []unreadableLabels
}

// unreadableLabels is a replica whose deployment labels do not parse.
type unreadableLabels struct {
	service string
	replica int
	err     error
}

// findDeployments takes every container's deployment, the latest build being the deployment; a
// container carrying another, none or unreadable labels counts as built by none.
func findDeployments(containers composemodel.ObservedComposeProject) containerDeployments {
	var (
		latest *composemodel.Deployment
		// deploymentByContainer is the deployment each container's labels name, by container id.
		deploymentByContainer = map[string]composemodel.Deployment{}
		builtByLatest         = map[string]bool{}
		unreadable            []unreadableLabels
		total                 int
	)
	for _, service := range containers.Services {
		for _, replica := range service.Replicas {
			total++
			switch {
			case replica.DeploymentErr != nil:
				unreadable = append(unreadable, unreadableLabels{
					service: service.Name,
					replica: replica.Number,
					err:     replica.DeploymentErr,
				})
			case replica.Deployment != nil:
				deploymentByContainer[replica.ContainerID] = *replica.Deployment
				if latest == nil || replica.Deployment.DeployedAt.After(latest.DeployedAt) {
					latest = replica.Deployment
				}
			}
		}
	}
	for id, labels := range deploymentByContainer {
		if isSameDeployment(labels, *latest) {
			builtByLatest[id] = true
		}
	}
	return containerDeployments{
		source:        newDeploymentSource(total, len(builtByLatest)),
		latest:        latest,
		builtByLatest: builtByLatest,
		unreadable:    unreadable,
	}
}

// isUnreadable reports whether the replica numbered number of service carries deployment labels
// that do not parse: whether the deployment built it is unknown.
func (d containerDeployments) isUnreadable(service string, number int) bool {
	return slices.ContainsFunc(d.unreadable, func(labels unreadableLabels) bool {
		return labels.service == service && labels.replica == number
	})
}

// isSameDeployment reports whether a and b are one build: the same savepoint, session and arcave
// project deployed at the same instant.
func isSameDeployment(a, b composemodel.Deployment) bool {
	return a.DeployedAt.Equal(b.DeployedAt) &&
		a.ArcaveProject == b.ArcaveProject &&
		a.Session.ID == b.Session.ID &&
		a.Savepoint.ID == b.Savepoint.ID
}

// newDeploymentSource says what built the total containers, labelled of them by a rollback.
func newDeploymentSource(total, labelled int) composemodel.DeploymentSource {
	switch {
	case total == 0:
		return ""
	case labelled == 0:
		return composemodel.DeploymentSourceCompose
	case labelled == total:
		return composemodel.DeploymentSourceSavepoint
	default:
		return composemodel.DeploymentSourceMixed
	}
}

// newServiceStatuses is every service the daemon runs or the deployment recorded, by name.
func newServiceStatuses(containers composemodel.ObservedComposeProject, deployments containerDeployments) []composemodel.ServiceStatus {
	var (
		live     = map[string][]composemodel.ObservedReplica{}
		jobs     = map[string]bool{}
		recorded map[string][]int
	)
	if deployments.latest != nil {
		recorded = deployments.latest.ServiceReplicas
	}
	for _, service := range containers.Services {
		live[service.Name] = service.Replicas
		jobs[service.Name] = service.Job
	}
	names := map[string]bool{}
	for name := range live {
		names[name] = true
	}
	for name := range recorded {
		names[name] = true
	}
	services := make([]composemodel.ServiceStatus, 0, len(names))
	for _, name := range slices.Sorted(maps.Keys(names)) {
		services = append(services, newServiceStatus(name, live[name], recorded[name], jobs[name], deployments.builtByLatest))
	}
	return services
}

// newServiceStatus is one service: its live replicas, and every replica the deployment recorded
// that the daemon does not hold; job marks one whose exit 0 is its success.
func newServiceStatus(
	name string, containers []composemodel.ObservedReplica, recorded []int, job bool, builtByLatest map[string]bool,
) composemodel.ServiceStatus {
	replicas := make([]composemodel.ReplicaStatus, 0, max(len(containers), len(recorded)))
	held := map[int]bool{}
	for _, replica := range containers {
		held[replica.Number] = true
		replicas = append(replicas, newReplicaStatus(replica, job, builtByLatest[replica.ContainerID],
			slices.Contains(recorded, replica.Number)))
	}
	for _, number := range recorded {
		if !held[number] {
			replicas = append(replicas, composemodel.ReplicaStatus{
				Number:      number,
				State:       composemodel.ReplicaStateMissing,
				InSavepoint: true,
			})
		}
	}
	slices.SortStableFunc(replicas, func(a, b composemodel.ReplicaStatus) int { return a.Number - b.Number })
	up := 0
	for _, replica := range replicas {
		if replica.State == composemodel.ReplicaStateRunning || replica.State == composemodel.ReplicaStateCompleted {
			up++
		}
	}
	return composemodel.ServiceStatus{
		Name:             name,
		State:            newServiceState(replicas),
		Health:           newServiceHealth(replicas),
		ReplicasExpected: len(recorded),
		ReplicasUp:       up,
		Replicas:         replicas,
	}
}

// newReplicaStatus is one container as the daemon reports it. job marks a replica whose exit 0 is
// its success.
func newReplicaStatus(replica composemodel.ObservedReplica, job, inDeployment, inSavepoint bool) composemodel.ReplicaStatus {
	return composemodel.ReplicaStatus{
		Number:        replica.Number,
		Container:     replica.Container,
		ContainerID:   replica.ContainerID,
		State:         newReplicaState(replica, job),
		Health:        replica.Health,
		ExitCode:      replica.ExitCode,
		OOMKilled:     replica.OOMKilled,
		RestartCount:  replica.RestartCount,
		FailingChecks: replica.FailingChecks,
		CreatedAt:     replica.CreatedAt,
		StartedAt:     replica.StartedAt,
		FinishedAt:    replica.FinishedAt,
		Replaces:      replica.Replaces,
		InDeployment:  inDeployment,
		InSavepoint:   inSavepoint,
		Ports:         replica.Ports,
	}
}

// newReplicaState is the daemon's state, but a replica that exited 0 completed when it is a job: a
// server stopped by hand exits 0 too.
func newReplicaState(replica composemodel.ObservedReplica, job bool) composemodel.ReplicaState {
	if replica.State == composemodel.ReplicaStateExited && replica.ExitCode == 0 && job {
		return composemodel.ReplicaStateCompleted
	}
	return replica.State
}

// newServiceState is the worst of the replicas' states; completed only when every one is.
func newServiceState(replicas []composemodel.ReplicaStatus) composemodel.ReplicaState {
	if len(replicas) == 0 {
		return composemodel.ReplicaStateRunning
	}
	return slices.MaxFunc(replicas, func(a, b composemodel.ReplicaStatus) int {
		return cmp.Compare(replicaStateRank[a.State], replicaStateRank[b.State])
	}).State
}

// newServiceHealth is the worst of the replicas' healthchecks.
func newServiceHealth(replicas []composemodel.ReplicaStatus) composemodel.ReplicaHealth {
	var worst composemodel.ReplicaHealth
	for _, replica := range replicas {
		if replicaHealthRank[replica.Health] > replicaHealthRank[worst] {
			worst = replica.Health
		}
	}
	return worst
}

// newComposeProjectState sums the compose project up: the first state of
// composemodel.ComposeProjectState that applies.
func newComposeProjectState(services []composemodel.ServiceStatus) composemodel.ComposeProjectState {
	var (
		live, running, stopped     int
		broken, degraded, starting bool
	)
	for _, service := range services {
		for _, replica := range service.Replicas {
			switch replica.State {
			case composemodel.ReplicaStateMissing:
				broken = true
				continue
			case composemodel.ReplicaStateRunning:
				running++
			case composemodel.ReplicaStateCompleted:
			case composemodel.ReplicaStateExited:
				// An exit 0 is a finished job or a server stopped by hand alike: it degrades nothing.
				stopped++
				degraded = degraded || replica.ExitCode != 0
			case composemodel.ReplicaStateCreated:
				stopped++
				degraded = true
			default:
				// Paused, restarting, dead or being removed: not stopped, and not doing its work.
				broken = true
			}
			live++
			degraded = degraded || replica.Health == composemodel.ReplicaHealthUnhealthy
			starting = starting || replica.Health == composemodel.ReplicaHealthStarting
		}
	}
	switch {
	case live == 0:
		return composemodel.ComposeProjectStateAbsent
	case running == 0 && stopped > 0 && !broken:
		return composemodel.ComposeProjectStateStopped
	case broken, degraded:
		return composemodel.ComposeProjectStateDegraded
	case starting:
		return composemodel.ComposeProjectStateStarting
	default:
		return composemodel.ComposeProjectStateRunning
	}
}
