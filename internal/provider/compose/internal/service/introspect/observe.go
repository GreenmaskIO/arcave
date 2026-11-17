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
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/greenmaskio/arcave/internal/model"
)

// Observe reads the compose project's replicas as the daemon runs them and the services each waits
// on to complete, nothing they mount or run on; it refuses no daemon. None means never started or
// taken down. A container gone between the listing and its inspect is left out, its service too.
func (s *Inspector) Observe(ctx context.Context) (model.ObservedWorkload, error) {
	listed, err := s.listContainers(ctx)
	if err != nil {
		return model.ObservedWorkload{}, fmt.Errorf("read containers of compose project %q: %w", s.composeProject, err)
	}
	byService := groupByService(keepProjectContainers(listed))
	services := make([]model.ObservedService, 0, len(byService))
	for _, name := range slices.Sorted(maps.Keys(byService)) {
		containers, err := s.readPresentContainers(ctx, byService[name])
		if err != nil {
			return model.ObservedWorkload{}, fmt.Errorf("read service %q of compose project %q: %w",
				name, s.composeProject, err)
		}
		if len(containers) == 0 {
			continue
		}
		services = append(services, model.ObservedService{
			Name:             name,
			AwaitsCompletion: parseAwaitsCompletion(byService[name][0].Labels[api.DependenciesLabel]),
			Replicas:         newObservedReplicas(containers),
		})
	}
	return model.ObservedWorkload{Services: services}, nil
}

// newObservedReplicas is each container as the daemon reports it.
func newObservedReplicas(containers []composemodel.Container) []model.ObservedReplica {
	replicas := make([]model.ObservedReplica, 0, len(containers))
	for _, c := range containers {
		replicas = append(replicas, newObservedReplica(c))
	}
	return replicas
}

// newObservedReplica is one container as the daemon reports it.
func newObservedReplica(c composemodel.Container) model.ObservedReplica {
	var (
		inspect = c.Inspect
		state   = cmp.Or(inspect.State, &container.State{})
		failing int
	)
	if state.Health != nil {
		failing = state.Health.FailingStreak
	}
	return model.ObservedReplica{
		Number:        c.Number,
		Container:     c.Name,
		ContainerID:   c.ID,
		Labels:        c.Labels,
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
