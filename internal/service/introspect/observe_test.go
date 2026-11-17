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
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestNewReplicaState(t *testing.T) {
	tests := []struct {
		name  string
		state container.State
		want  model.ReplicaState
	}{
		{name: "running", state: container.State{Status: container.StateRunning, Running: true}, want: model.ReplicaStateRunning},
		{name: "restarting outranks running", state: container.State{Status: container.StateRestarting, Running: true, Restarting: true}, want: model.ReplicaStateRestarting},
		{name: "paused outranks running", state: container.State{Status: container.StatePaused, Running: true, Paused: true}, want: model.ReplicaStatePaused},
		{name: "created", state: container.State{Status: container.StateCreated}, want: model.ReplicaStateCreated},
		{name: "removing", state: container.State{Status: container.StateRemoving}, want: model.ReplicaStateRemoving},
		{name: "dead", state: container.State{Status: container.StateDead, Dead: true}, want: model.ReplicaStateDead},
		{name: "exited 0", state: container.State{Status: container.StateExited}, want: model.ReplicaStateExited},
		{name: "exited 1", state: container.State{Status: container.StateExited, ExitCode: 1}, want: model.ReplicaStateExited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newReplicaState(tt.state))
		})
	}
}

func TestNewObservedReplica(t *testing.T) {
	// Arrange
	created := time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)
	started := time.Date(2026, 11, 7, 14, 2, 30, 0, time.UTC)
	replica := model.Container{
		ID:       "c-db-1",
		Name:     "demo-db-1",
		Number:   1,
		Labels:   map[string]string{"k": "v"},
		Replaces: "demo-db-0",
		Inspect: container.InspectResponse{
			Created:      created.Format(time.RFC3339Nano),
			RestartCount: 2,
			State: &container.State{
				Status:    container.StateRunning,
				Running:   true,
				OOMKilled: true,
				StartedAt: started.Format(time.RFC3339Nano),
				// The daemon writes the zero time for a container never finished.
				FinishedAt: "0001-01-01T00:00:00Z",
				Health:     &container.Health{Status: container.Unhealthy, FailingStreak: 3},
			},
			NetworkSettings: &container.NetworkSettings{Ports: network.PortMap{
				network.MustParsePort("5432/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "5432"}},
			}},
		},
	}

	// Act
	got := newObservedReplica(replica)

	// Assert
	assert.Equal(t, model.ObservedReplica{
		Number:        1,
		Container:     "demo-db-1",
		ContainerID:   "c-db-1",
		Labels:        map[string]string{"k": "v"},
		State:         model.ReplicaStateRunning,
		Health:        model.ReplicaHealthUnhealthy,
		OOMKilled:     true,
		RestartCount:  2,
		FailingChecks: 3,
		CreatedAt:     &created,
		StartedAt:     &started,
		Replaces:      "demo-db-0",
		Ports:         []model.PublishedPort{{ContainerPort: 5432, Protocol: "tcp", HostIP: "127.0.0.1", HostPort: 5432}},
	}, got)
}

func TestNewObservedReplica_NoState(t *testing.T) {
	// Act: an inspect with no state reads as an exit, with nothing else known.
	got := newObservedReplica(model.Container{ID: "c-db-1", Number: 1})

	// Assert
	assert.Equal(t, model.ObservedReplica{
		Number:      1,
		ContainerID: "c-db-1",
		State:       model.ReplicaStateExited,
	}, got)
}

func TestNewReplicaHealth(t *testing.T) {
	tests := []struct {
		name  string
		state container.State
		want  model.ReplicaHealth
	}{
		{name: "healthy", state: container.State{Running: true, Health: &container.Health{Status: container.Healthy}}, want: model.ReplicaHealthHealthy},
		{name: "no healthcheck", state: container.State{Running: true}},
		{name: "a healthcheck of none", state: container.State{Running: true, Health: &container.Health{Status: container.NoHealthcheck}}},
		{name: "a stopped container's last status", state: container.State{Health: &container.Health{Status: container.Unhealthy}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newReplicaHealth(tt.state))
		})
	}
}

func TestNewPublishedPorts(t *testing.T) {
	// Arrange
	http := network.MustParsePort("80/tcp")
	dns := network.MustParsePort("53/udp")
	settings := &container.NetworkSettings{Ports: network.PortMap{
		http: {
			{HostIP: netip.MustParseAddr("::"), HostPort: "8080"},
			{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: "8080"},
		},
		dns:                               {{HostPort: "5353"}},
		network.MustParsePort("9000/tcp"): nil,
	}}

	// Act
	ports := newPublishedPorts(settings)

	// Assert
	assert.Equal(t, []model.PublishedPort{
		{ContainerPort: 53, Protocol: "udp", HostPort: 5353},
		{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 8080},
		{ContainerPort: 80, Protocol: "tcp", HostIP: "::", HostPort: 8080},
	}, ports, "a zero address is no address, and an exposed port publishes nothing")
}
