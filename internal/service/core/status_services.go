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

	"github.com/compose-spec/compose-go/v2/types"
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
	// primary is the deployment most containers carry, the newest on a tie; nil for none.
	primary *model.SavepointDeployment
	// deployments is the deployment each labelled container carries, by container id.
	deployments map[string]model.DeploymentID
	// notices name the containers whose labels could not be read.
	notices []model.Notice
}

// findDeployments reads every container's deployment labels; one that cannot be read is warned
// of and counts as built by no deployment.
func findDeployments(containers model.ComposeProjectContainers) deployedContainers {
	var (
		deployments = map[string]model.DeploymentID{}
		byID        = map[model.DeploymentID]model.SavepointDeployment{}
		counts      = map[model.DeploymentID]int{}
		notices     []model.Notice
		total       int
	)
	for _, service := range containers.Services {
		for _, replica := range service.Containers {
			total++
			deployment, ok, err := model.ParseSavepointDeployment(replica.Labels)
			switch {
			case err != nil:
				notices = append(notices, newUnreadableLabelsNotice(service.Name, replica, err))
			case ok:
				deployments[replica.ID] = deployment.ID
				byID[deployment.ID] = deployment
				counts[deployment.ID]++
			}
		}
	}
	var primary *model.SavepointDeployment
	for _, id := range slices.Sorted(maps.Keys(counts)) {
		if primary == nil || counts[id] >= counts[primary.ID] {
			deployment := byID[id]
			primary = &deployment
		}
	}
	return deployedContainers{
		source:      newDeploymentSource(total, primary, counts),
		primary:     primary,
		deployments: deployments,
		notices:     notices,
	}
}

// newDeploymentSource says what built the total containers, primary having built counts[its id].
func newDeploymentSource(
	total int, primary *model.SavepointDeployment, counts map[model.DeploymentID]int,
) model.DeploymentSource {
	switch {
	case total == 0:
		return ""
	case primary == nil:
		return model.DeploymentSourceCompose
	case counts[primary.ID] == total:
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
		recorded map[string]model.DeployedService
	)
	if deployed.primary != nil {
		recorded = deployed.primary.Manifest.Services
	}
	for _, service := range containers.Services {
		live[service.Name] = service.Containers
	}
	names := slices.Sorted(maps.Keys(live))
	for name := range recorded {
		if _, ok := live[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	services := make([]model.ServiceStatus, 0, len(names))
	for _, name := range names {
		services = append(services, newServiceStatus(name, live[name], recorded[name], jobs[name], deployed))
	}
	return services
}

// newJobServices are the services another waits on to complete successfully, as compose recorded
// it on the containers; a server stopped by hand exits 0 too, so nothing else makes a job.
func newJobServices(containers model.ComposeProjectContainers) map[string]bool {
	jobs := map[string]bool{}
	for _, service := range containers.Services {
		for name, condition := range service.DependsOn {
			if condition == types.ServiceConditionCompletedSuccessfully {
				jobs[name] = true
			}
		}
	}
	return jobs
}

// newServiceStatus is one service: its live replicas, and every replica the deployment recorded
// that the daemon does not hold; job marks one whose exit 0 is its success.
func newServiceStatus(
	name string, containers []model.Container, recorded model.DeployedService, job bool, deployed deployedContainers,
) model.ServiceStatus {
	replicas := make([]model.ReplicaStatus, 0, max(len(containers), len(recorded.Replicas)))
	held := map[int]bool{}
	for _, replica := range containers {
		held[replica.Number] = true
		inDeployment := deployed.primary != nil && deployed.deployments[replica.ID] == deployed.primary.ID
		replicas = append(replicas, newReplicaStatus(replica, job, inDeployment,
			slices.Contains(recorded.Replicas, replica.Number)))
	}
	for _, number := range recorded.Replicas {
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
		ReplicasExpected: len(recorded.Replicas),
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
		image   string
		failing int
	)
	if inspect.Config != nil {
		image = inspect.Config.Image
	}
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
		Image:         image,
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
			default:
				stopped++
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
