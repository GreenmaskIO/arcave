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
	"cmp"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/greenmaskio/arcave/internal/model"
)

// replicaStateRank orders the replica states from the best to the worst: a service is as bad as
// its worst replica.
var replicaStateRank = map[model.ReplicaState]int{
	model.ReplicaStateRunning:    0,
	model.ReplicaStateCompleted:  1,
	model.ReplicaStateExited:     2,
	model.ReplicaStateRestarting: 3,
	model.ReplicaStateCreated:    4,
	model.ReplicaStatePaused:     5,
	model.ReplicaStateRemoving:   6,
	model.ReplicaStateDead:       7,
	model.ReplicaStateMissing:    8,
}

// replicaHealthRank orders the healthchecks from none to the worst.
var replicaHealthRank = map[model.ReplicaHealth]int{
	"":                           0,
	model.ReplicaHealthHealthy:   1,
	model.ReplicaHealthStarting:  2,
	model.ReplicaHealthUnhealthy: 3,
}

// deployedContainers is what the containers' labels say built them.
type deployedContainers struct {
	// source is empty for a compose project with no container.
	source model.DeploymentSource
	// deployment is the latest deployment the containers carry; nil for none.
	deployment *model.SavepointDeployment
	// builtByLatest holds the id of every container that deployment built.
	builtByLatest map[string]bool
	// notices name the containers whose labels could not be read.
	notices []model.Notice
}

// findDeployments reads every container's deployment labels, the latest build being the
// deployment: a container created outside arcave from a restored layer inherits an earlier one,
// and counts as built by none. Labels that cannot be read are warned of and count the same.
func findDeployments(containers model.ComposeProjectContainers) deployedContainers {
	var (
		deployment *model.SavepointDeployment
		// deploymentByContainer is the deployment each container's labels name, by container id.
		deploymentByContainer = map[string]model.SavepointDeployment{}
		// builtByLatest holds the id of every container the latest deployment built.
		builtByLatest = map[string]bool{}
		notices       []model.Notice
		total         int
	)
	for _, service := range containers.Services {
		for _, replica := range service.Containers {
			total++
			labels, ok, err := model.ParseSavepointDeployment(replica.Labels)
			switch {
			case err != nil:
				notices = append(notices, newUnreadableLabelsNotice(service.Name, replica, err))
			case ok:
				deploymentByContainer[replica.ID] = labels
				if deployment == nil || labels.DeployedAt.After(deployment.DeployedAt) {
					deployment = &labels
				}
			}
		}
	}
	for id, labels := range deploymentByContainer {
		if isSameDeployment(labels, *deployment) {
			builtByLatest[id] = true
		}
	}
	return deployedContainers{
		source:        newDeploymentSource(total, len(builtByLatest)),
		deployment:    deployment,
		builtByLatest: builtByLatest,
		notices:       notices,
	}
}

// isSameDeployment reports whether a and b are one build: the same savepoint, session and arcave
// project deployed at the same instant.
func isSameDeployment(a, b model.SavepointDeployment) bool {
	return a.DeployedAt.Equal(b.DeployedAt) &&
		a.ArcaveProject == b.ArcaveProject &&
		a.Session.ID == b.Session.ID &&
		a.Savepoint.ID == b.Savepoint.ID
}

// newDeploymentSource says what built the total containers, labelled of them by a rollback.
func newDeploymentSource(total, labelled int) model.DeploymentSource {
	switch {
	case total == 0:
		return ""
	case labelled == 0:
		return model.DeploymentSourceCompose
	case labelled == total:
		return model.DeploymentSourceSavepoint
	default:
		return model.DeploymentSourceMixed
	}
}

// newServiceStatuses is every service the daemon runs or the deployment recorded, by name.
func newServiceStatuses(containers model.ComposeProjectContainers, deployed deployedContainers) []model.ServiceStatus {
	var (
		live     = map[string][]model.Container{}
		jobs     = newJobServices(containers)
		recorded map[string][]int
	)
	if deployed.deployment != nil {
		recorded = deployed.deployment.ServiceReplicas
	}
	for _, service := range containers.Services {
		live[service.Name] = service.Containers
	}
	names := map[string]bool{}
	for name := range live {
		names[name] = true
	}
	for name := range recorded {
		names[name] = true
	}
	services := make([]model.ServiceStatus, 0, len(names))
	for _, name := range slices.Sorted(maps.Keys(names)) {
		services = append(services, newServiceStatus(name, live[name], recorded[name], jobs[name], deployed))
	}
	return services
}

// newJobServices are the services another waits on to complete successfully, as compose recorded
// it on the containers; a server stopped by hand exits 0 too, so nothing else makes a job.
func newJobServices(containers model.ComposeProjectContainers) map[string]bool {
	jobs := map[string]bool{}
	for _, service := range containers.Services {
		for _, awaited := range service.AwaitsCompletion {
			jobs[awaited] = true
		}
	}
	return jobs
}

// newServiceStatus is one service: its live replicas, and every replica the deployment recorded
// that the daemon does not hold; job marks one whose exit 0 is its success.
func newServiceStatus(
	name string, containers []model.Container, recorded []int, job bool, deployed deployedContainers,
) model.ServiceStatus {
	replicas := make([]model.ReplicaStatus, 0, max(len(containers), len(recorded)))
	held := map[int]bool{}
	for _, replica := range containers {
		held[replica.Number] = true
		replicas = append(replicas, newReplicaStatus(replica, job, deployed.builtByLatest[replica.ID],
			slices.Contains(recorded, replica.Number)))
	}
	for _, number := range recorded {
		if !held[number] {
			replicas = append(replicas, model.ReplicaStatus{
				Number:      number,
				State:       model.ReplicaStateMissing,
				InSavepoint: true,
			})
		}
	}
	slices.SortStableFunc(replicas, func(a, b model.ReplicaStatus) int { return a.Number - b.Number })
	up := 0
	for _, replica := range replicas {
		if replica.State == model.ReplicaStateRunning || replica.State == model.ReplicaStateCompleted {
			up++
		}
	}
	return model.ServiceStatus{
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
func newReplicaStatus(replica model.Container, job, inDeployment, inSavepoint bool) model.ReplicaStatus {
	var (
		inspect = replica.Inspect
		state   = cmp.Or(inspect.State, &container.State{})
		failing int
	)
	if state.Health != nil {
		failing = state.Health.FailingStreak
	}
	return model.ReplicaStatus{
		Number:        replica.Number,
		Container:     replica.Name,
		ContainerID:   replica.ID,
		State:         newReplicaState(*state, job),
		Health:        newReplicaHealth(*state),
		ExitCode:      state.ExitCode,
		OOMKilled:     state.OOMKilled,
		RestartCount:  inspect.RestartCount,
		FailingChecks: failing,
		CreatedAt:     parseDaemonTime(inspect.Created),
		StartedAt:     parseDaemonTime(state.StartedAt),
		FinishedAt:    parseDaemonTime(state.FinishedAt),
		Replaces:      replica.Replaces,
		InDeployment:  inDeployment,
		InSavepoint:   inSavepoint,
		Ports:         newPublishedPorts(inspect.NetworkSettings),
	}
}

// newReplicaState is the daemon's state in one word. A replica that exited 0 completed only when it
// is a job: a server stopped by hand exits 0 too.
func newReplicaState(state container.State, job bool) model.ReplicaState {
	switch {
	case state.Restarting:
		return model.ReplicaStateRestarting
	case state.Paused:
		return model.ReplicaStatePaused
	case state.Running:
		return model.ReplicaStateRunning
	case state.Status == container.StateCreated:
		return model.ReplicaStateCreated
	case state.Status == container.StateRemoving:
		return model.ReplicaStateRemoving
	case state.Dead || state.Status == container.StateDead:
		return model.ReplicaStateDead
	case state.ExitCode == 0 && job:
		return model.ReplicaStateCompleted
	default:
		return model.ReplicaStateExited
	}
}

// newReplicaHealth is the healthcheck's status; empty for a container that runs none, or is not
// running: the daemon keeps a stopped container's last status.
func newReplicaHealth(state container.State) model.ReplicaHealth {
	if !state.Running || state.Health == nil || state.Health.Status == container.NoHealthcheck {
		return ""
	}
	return model.ReplicaHealth(state.Health.Status)
}

// newServiceState is the worst of the replicas' states; completed only when every one is.
func newServiceState(replicas []model.ReplicaStatus) model.ReplicaState {
	if len(replicas) > 0 && !slices.ContainsFunc(replicas, func(replica model.ReplicaStatus) bool {
		return replica.State != model.ReplicaStateCompleted
	}) {
		return model.ReplicaStateCompleted
	}
	worst := model.ReplicaStateRunning
	for _, replica := range replicas {
		if replica.State != model.ReplicaStateCompleted && replicaStateRank[replica.State] > replicaStateRank[worst] {
			worst = replica.State
		}
	}
	return worst
}

// newServiceHealth is the worst of the replicas' healthchecks.
func newServiceHealth(replicas []model.ReplicaStatus) model.ReplicaHealth {
	var worst model.ReplicaHealth
	for _, replica := range replicas {
		if replicaHealthRank[replica.Health] > replicaHealthRank[worst] {
			worst = replica.Health
		}
	}
	return worst
}

// newPublishedPorts are the ports the container publishes on the host, sorted.
func newPublishedPorts(settings *container.NetworkSettings) []model.PublishedPort {
	if settings == nil {
		return nil
	}
	var ports []model.PublishedPort
	for port, bindings := range settings.Ports {
		for _, binding := range bindings {
			hostPort, err := strconv.Atoi(binding.HostPort)
			if err != nil {
				continue
			}
			ports = append(ports, model.PublishedPort{
				ContainerPort: int(port.Num()),
				Protocol:      string(port.Proto()),
				HostIP:        formatHostIP(binding.HostIP),
				HostPort:      hostPort,
			})
		}
	}
	slices.SortFunc(ports, func(a, b model.PublishedPort) int {
		return cmp.Or(cmp.Compare(a.ContainerPort, b.ContainerPort), strings.Compare(a.Protocol, b.Protocol),
			cmp.Compare(a.HostPort, b.HostPort), strings.Compare(a.HostIP, b.HostIP))
	})
	return ports
}

// formatHostIP is the address a port is published on; empty for none, which the zero netip.Addr
// would spell "invalid IP".
func formatHostIP(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}

// parseDaemonTime reads a time the daemon reports; nil for none, or the zero time it writes for a
// container never started or never finished.
func parseDaemonTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return nil
	}
	return &parsed
}

// newComposeProjectState sums the compose project up: the first state of model.ComposeProjectState
// that applies.
func newComposeProjectState(rollbackPending bool, services []model.ServiceStatus) model.ComposeProjectState {
	var (
		live, running, stopped int
		degraded, starting     bool
	)
	for _, service := range services {
		for _, replica := range service.Replicas {
			switch replica.State {
			case model.ReplicaStateMissing:
				degraded = true
				continue
			case model.ReplicaStateRunning:
				running++
			case model.ReplicaStateCompleted:
			case model.ReplicaStateExited:
				// An exit 0 is a finished job or a server stopped by hand alike: it degrades nothing.
				stopped++
				degraded = degraded || replica.ExitCode != 0
			case model.ReplicaStateCreated:
				stopped++
				degraded = true
			default:
				// Paused, restarting, dead or being removed: not stopped, and not doing its work.
				degraded = true
			}
			live++
			degraded = degraded || replica.Health == model.ReplicaHealthUnhealthy
			starting = starting || replica.Health == model.ReplicaHealthStarting
		}
	}
	switch {
	case rollbackPending:
		return model.ComposeProjectStateRollbackPending
	case live == 0:
		return model.ComposeProjectStateAbsent
	case running == 0 && stopped > 0:
		return model.ComposeProjectStateStopped
	case degraded:
		return model.ComposeProjectStateDegraded
	case starting:
		return model.ComposeProjectStateStarting
	default:
		return model.ComposeProjectStateRunning
	}
}
