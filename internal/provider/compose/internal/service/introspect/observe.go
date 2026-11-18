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

package introspect

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// Observe reads the compose project's replicas as the daemon runs them, the deployment each
// carries and the services another waits on to complete, nothing they mount or run on; it refuses
// no daemon. None means never started or taken down. A container gone between the listing and its
// inspect is left out, its service too.
func (s *Reader) Observe(ctx context.Context) (provider.ObservedWorkload, error) {
	listed, err := s.listContainers(ctx)
	if err != nil {
		return provider.ObservedWorkload{}, fmt.Errorf("read containers of compose project %q: %w", s.composeProject, err)
	}
	var (
		byService = groupByService(keepProjectContainers(listed))
		jobs      = newJobServices(byService)
		services  = make([]provider.ObservedService, 0, len(byService))
	)
	for _, name := range slices.Sorted(maps.Keys(byService)) {
		containers, err := s.readPresentContainers(ctx, byService[name])
		if err != nil {
			return provider.ObservedWorkload{}, fmt.Errorf("read service %q of compose project %q: %w",
				name, s.composeProject, err)
		}
		if len(containers) == 0 {
			continue
		}
		services = append(services, provider.ObservedService{
			Name:     name,
			Job:      jobs[name],
			Replicas: newObservedReplicas(containers),
		})
	}
	return provider.ObservedWorkload{Services: services}, nil
}

// newJobServices are the services another waits on to complete successfully, as compose recorded
// it on the containers; a server stopped by hand exits 0 too, so nothing else makes a job.
func newJobServices(byService map[string][]container.Summary) map[string]bool {
	jobs := map[string]bool{}
	for _, summaries := range byService {
		for _, awaited := range parseAwaitsCompletion(summaries[0].Labels[api.DependenciesLabel]) {
			jobs[awaited] = true
		}
	}
	return jobs
}

// parseAwaitsCompletion reads compose's depends_on label, service:condition:restart entries
// comma-separated, into the services waited on to complete successfully; nil for none.
func parseAwaitsCompletion(label string) []string {
	var awaited []string
	for entry := range strings.SplitSeq(label, ",") {
		service, rest, _ := strings.Cut(entry, ":")
		condition, _, _ := strings.Cut(rest, ":")
		if service != "" && condition == types.ServiceConditionCompletedSuccessfully {
			awaited = append(awaited, service)
		}
	}
	return awaited
}

// newObservedReplicas is each container as the daemon reports it.
func newObservedReplicas(containers []composemodel.Container) []provider.ObservedReplica {
	replicas := make([]provider.ObservedReplica, 0, len(containers))
	for _, c := range containers {
		replicas = append(replicas, newObservedReplica(c))
	}
	return replicas
}

// newObservedReplica is one container as the daemon reports it, its deployment labels parsed.
func newObservedReplica(c composemodel.Container) provider.ObservedReplica {
	var (
		inspect       = c.Inspect
		state         = cmp.Or(inspect.State, &container.State{})
		failing       int
		deployment    *model.SavepointDeployment
		deploymentErr error
	)
	if state.Health != nil {
		failing = state.Health.FailingStreak
	}
	if parsed, ok, err := model.ParseSavepointDeployment(c.Labels); err != nil {
		deploymentErr = err
	} else if ok {
		deployment = &parsed
	}
	return provider.ObservedReplica{
		Number:        c.Number,
		Container:     c.Name,
		ContainerID:   c.ID,
		Deployment:    deployment,
		DeploymentErr: deploymentErr,
		State:         newReplicaState(*state),
		Health:        newReplicaHealth(*state),
		ExitCode:      state.ExitCode,
		OOMKilled:     state.OOMKilled,
		RestartCount:  inspect.RestartCount,
		FailingChecks: failing,
		CreatedAt:     parseDaemonTime(inspect.Created),
		StartedAt:     parseDaemonTime(state.StartedAt),
		FinishedAt:    parseDaemonTime(state.FinishedAt),
		Replaces:      c.Replaces,
		Ports:         newPublishedPorts(inspect.NetworkSettings),
	}
}

// newReplicaState is the daemon's state in one word; an exit is exited, whatever its code.
func newReplicaState(state container.State) model.ReplicaState {
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
