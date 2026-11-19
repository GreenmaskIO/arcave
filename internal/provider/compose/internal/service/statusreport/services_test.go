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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

func TestFindDeployments(t *testing.T) {
	deployment := newStatusDeployment(statusDeployed)
	earlier := newStatusDeployment(statusNewer)
	earlier.DeployedAt = deployment.DeployedAt.Add(-time.Hour)
	tests := []struct {
		name           string
		deployments    [][]observedDeployment // per service, per replica
		wantSource     composemodel.DeploymentSource
		wantDeployed   bool
		wantLabelled   int
		wantUnreadable int
	}{
		{name: "no container is no source"},
		{
			name:         "every container of the deployment",
			deployments:  [][]observedDeployment{{deployedBy(deployment), deployedBy(deployment)}},
			wantSource:   composemodel.DeploymentSourceSavepoint,
			wantDeployed: true,
			wantLabelled: 2,
		},
		{
			name:         "some containers of none",
			deployments:  [][]observedDeployment{{{}}, {deployedBy(deployment)}},
			wantSource:   composemodel.DeploymentSourceMixed,
			wantDeployed: true,
			wantLabelled: 1,
		},
		{
			name:        "no container of any",
			deployments: [][]observedDeployment{{{}, {}}},
			wantSource:  composemodel.DeploymentSourceCompose,
		},
		{
			name:         "an earlier deployment a restored layer carried counts as none",
			deployments:  [][]observedDeployment{{deployedBy(earlier)}, {deployedBy(deployment)}},
			wantSource:   composemodel.DeploymentSourceMixed,
			wantDeployed: true,
			wantLabelled: 1,
		},
		{
			name:         "the latest deployment whatever the order",
			deployments:  [][]observedDeployment{{deployedBy(deployment)}, {deployedBy(earlier), deployedBy(deployment)}},
			wantSource:   composemodel.DeploymentSourceMixed,
			wantDeployed: true,
			wantLabelled: 2,
		},
		{
			name:           "unreadable labels are kept apart and count as none",
			deployments:    [][]observedDeployment{{deployedBy(deployment), unreadableDeployment}},
			wantSource:     composemodel.DeploymentSourceMixed,
			wantDeployed:   true,
			wantLabelled:   1,
			wantUnreadable: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var services []composemodel.ObservedService
			for i, replicas := range tt.deployments {
				service := composemodel.ObservedService{Name: fmt.Sprintf("svc%d", i)}
				for number, deployed := range replicas {
					service.Replicas = append(service.Replicas, newStatusContainer(service.Name, number+1, deployed, runningState()))
				}
				services = append(services, service)
			}

			// Act
			deployments := findDeployments(composemodel.ObservedComposeProject{Services: services})

			// Assert
			assert.Equal(t, tt.wantSource, deployments.source)
			if tt.wantDeployed {
				require.NotNil(t, deployments.latest)
				assert.Equal(t, deployment, *deployments.latest)
			} else {
				assert.Nil(t, deployments.latest)
			}
			assert.Len(t, deployments.builtByLatest, tt.wantLabelled)
			assert.Len(t, deployments.unreadable, tt.wantUnreadable)
		})
	}
}

func TestNewReplicaState(t *testing.T) {
	tests := []struct {
		name    string
		replica composemodel.ObservedReplica
		job     bool
		want    composemodel.ReplicaState
	}{
		{name: "exited 0", replica: composemodel.ObservedReplica{State: composemodel.ReplicaStateExited}, want: composemodel.ReplicaStateExited},
		{name: "a job that exited 0 completed", replica: composemodel.ObservedReplica{State: composemodel.ReplicaStateExited}, job: true, want: composemodel.ReplicaStateCompleted},
		{name: "a job that failed exited", replica: composemodel.ObservedReplica{State: composemodel.ReplicaStateExited, ExitCode: 1}, job: true, want: composemodel.ReplicaStateExited},
		{name: "a job still running runs", replica: composemodel.ObservedReplica{State: composemodel.ReplicaStateRunning}, job: true, want: composemodel.ReplicaStateRunning},
		{name: "a dead job is dead", replica: composemodel.ObservedReplica{State: composemodel.ReplicaStateDead}, job: true, want: composemodel.ReplicaStateDead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newReplicaState(tt.replica, tt.job))
		})
	}
}

func TestNewReplicaStatus(t *testing.T) {
	// Arrange
	created := time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)
	started := time.Date(2026, 11, 7, 14, 2, 30, 0, time.UTC)
	finished := time.Date(2026, 11, 7, 15, 0, 0, 0, time.UTC)
	ports := []composemodel.PublishedPort{{ContainerPort: 5432, Protocol: "tcp", HostIP: "127.0.0.1", HostPort: 5432}}
	replica := composemodel.ObservedReplica{
		Number:        2,
		Container:     "demo-db-2",
		ContainerID:   "db-2-id",
		State:         composemodel.ReplicaStateRunning,
		Health:        composemodel.ReplicaHealthUnhealthy,
		ExitCode:      137,
		OOMKilled:     true,
		RestartCount:  4,
		FailingChecks: 3,
		CreatedAt:     &created,
		StartedAt:     &started,
		FinishedAt:    &finished,
		Replaces:      "demo-db-1",
		Ports:         ports,
	}

	// Act
	got := newReplicaStatus(replica, false, true, false)

	// Assert
	assert.Equal(t, composemodel.ReplicaStatus{
		Number:        2,
		Container:     "demo-db-2",
		ContainerID:   "db-2-id",
		State:         composemodel.ReplicaStateRunning,
		Health:        composemodel.ReplicaHealthUnhealthy,
		ExitCode:      137,
		OOMKilled:     true,
		RestartCount:  4,
		FailingChecks: 3,
		CreatedAt:     &created,
		StartedAt:     &started,
		FinishedAt:    &finished,
		Replaces:      "demo-db-1",
		InDeployment:  true,
		Ports:         ports,
	}, got)
}

func TestNewServiceState(t *testing.T) {
	var (
		running   = composemodel.ReplicaStatus{State: composemodel.ReplicaStateRunning}
		completed = composemodel.ReplicaStatus{State: composemodel.ReplicaStateCompleted}
		exited    = composemodel.ReplicaStatus{State: composemodel.ReplicaStateExited}
	)
	tests := []struct {
		name     string
		replicas []composemodel.ReplicaStatus
		want     composemodel.ReplicaState
	}{
		{name: "every replica completed", replicas: []composemodel.ReplicaStatus{completed, completed}, want: composemodel.ReplicaStateCompleted},
		{name: "a completed replica beside a running one runs", replicas: []composemodel.ReplicaStatus{completed, running}, want: composemodel.ReplicaStateRunning},
		{name: "an exited replica outranks a completed one", replicas: []composemodel.ReplicaStatus{completed, exited}, want: composemodel.ReplicaStateExited},
		{name: "a service with no replica runs", want: composemodel.ReplicaStateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newServiceState(tt.replicas))
		})
	}
}

func TestNewServiceStatuses(t *testing.T) {
	deployment := newStatusDeployment(statusDeployed)
	deployment.ServiceReplicas = map[string][]int{
		"web":     {1, 3},
		"migrate": {1},
	}
	deployed := deployedBy(deployment)
	tests := []struct {
		name       string
		services   []composemodel.ObservedService
		deployment *composemodel.Deployment
		want       []composemodel.ServiceStatus
	}{
		{
			name: "a recorded gap is no missing replica, a recorded replica the daemon lacks is",
			services: []composemodel.ObservedService{{Name: "web", Replicas: []composemodel.ObservedReplica{
				newStatusContainer("web", 1, deployed, runningState()),
			}}},
			deployment: &deployment,
			want: []composemodel.ServiceStatus{
				{Name: "migrate", State: composemodel.ReplicaStateMissing, ReplicasExpected: 1, Replicas: []composemodel.ReplicaStatus{
					{Number: 1, State: composemodel.ReplicaStateMissing, InSavepoint: true},
				}},
				{Name: "web", State: composemodel.ReplicaStateMissing, ReplicasExpected: 2, ReplicasUp: 1, Replicas: []composemodel.ReplicaStatus{
					newRunningStatusReplica("web", 1, true, true),
					{Number: 3, State: composemodel.ReplicaStateMissing, InSavepoint: true},
				}},
			},
		},
		{
			name: "a replica added since, one rebuilt outside arcave, and a job another waits on that exited 0",
			services: []composemodel.ObservedService{{Name: "web", Replicas: []composemodel.ObservedReplica{
				newStatusContainer("web", 1, observedDeployment{}, runningState()),
				newStatusContainer("web", 2, deployed, runningState()),
				newStatusContainer("web", 3, deployed, runningState()),
			}}, {Name: "migrate", Job: true, Replicas: []composemodel.ObservedReplica{
				newStatusContainer("migrate", 1, deployed, composemodel.ObservedReplica{State: composemodel.ReplicaStateExited}),
			}}},
			deployment: &deployment,
			want: []composemodel.ServiceStatus{
				{Name: "migrate", State: composemodel.ReplicaStateCompleted, ReplicasExpected: 1, ReplicasUp: 1, Replicas: []composemodel.ReplicaStatus{
					withReplicaState(newRunningStatusReplica("migrate", 1, true, true), composemodel.ReplicaStateCompleted),
				}},
				{Name: "web", State: composemodel.ReplicaStateRunning, ReplicasExpected: 2, ReplicasUp: 3, Replicas: []composemodel.ReplicaStatus{
					newRunningStatusReplica("web", 1, true, false),
					newRunningStatusReplica("web", 2, false, true),
					newRunningStatusReplica("web", 3, true, true),
				}},
			},
		},
		{
			name: "with none deployed nothing is expected, so nothing is missing; an exit 0 no service waits on is no job's",
			services: []composemodel.ObservedService{{Name: "migrate", Replicas: []composemodel.ObservedReplica{
				newStatusContainer("migrate", 1, observedDeployment{}, composemodel.ObservedReplica{State: composemodel.ReplicaStateExited}),
			}}},
			want: []composemodel.ServiceStatus{
				{Name: "migrate", State: composemodel.ReplicaStateExited, Replicas: []composemodel.ReplicaStatus{
					withReplicaState(newRunningStatusReplica("migrate", 1, false, false), composemodel.ReplicaStateExited),
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			containers := composemodel.ObservedComposeProject{Services: tt.services}
			deployments := findDeployments(containers)
			require.Equal(t, tt.deployment != nil, deployments.latest != nil)

			// Act
			services := newServiceStatuses(containers, deployments)

			// Assert
			assert.Equal(t, tt.want, withoutReplicaTimes(services))
		})
	}
}

func TestNewComposeProjectState(t *testing.T) {
	running := composemodel.ReplicaStatus{State: composemodel.ReplicaStateRunning}
	tests := []struct {
		name     string
		replicas []composemodel.ReplicaStatus
		want     composemodel.ComposeProjectState
	}{
		{name: "no container", want: composemodel.ComposeProjectStateAbsent},
		{name: "every recorded replica missing", replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateMissing}}, want: composemodel.ComposeProjectStateAbsent},
		{
			name:     "every container stopped",
			replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateExited}, {State: composemodel.ReplicaStateExited, ExitCode: 1}},
			want:     composemodel.ComposeProjectStateStopped,
		},
		{name: "a replica missing", replicas: []composemodel.ReplicaStatus{running, {State: composemodel.ReplicaStateMissing}}, want: composemodel.ComposeProjectStateDegraded},
		{name: "a replica failed", replicas: []composemodel.ReplicaStatus{running, {State: composemodel.ReplicaStateExited, ExitCode: 1}}, want: composemodel.ComposeProjectStateDegraded},
		{
			name:     "every container stopped, a completed job aside",
			replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateExited}, {State: composemodel.ReplicaStateCompleted}},
			want:     composemodel.ComposeProjectStateStopped,
		},
		{name: "only completed jobs", replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateCompleted}}, want: composemodel.ComposeProjectStateRunning},
		{name: "a completed job beside a running replica", replicas: []composemodel.ReplicaStatus{running, {State: composemodel.ReplicaStateCompleted}}, want: composemodel.ComposeProjectStateRunning},
		{
			name:     "a replica that exited 0 no service waits on degrades nothing: it may be a server stopped by hand",
			replicas: []composemodel.ReplicaStatus{running, {State: composemodel.ReplicaStateExited}},
			want:     composemodel.ComposeProjectStateRunning,
		},
		{name: "a replica paused", replicas: []composemodel.ReplicaStatus{running, {State: composemodel.ReplicaStatePaused}}, want: composemodel.ComposeProjectStateDegraded},
		{
			name:     "every replica paused is degraded, not stopped",
			replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStatePaused}, {State: composemodel.ReplicaStatePaused}},
			want:     composemodel.ComposeProjectStateDegraded,
		},
		{
			name:     "every replica restarting is degraded, not stopped",
			replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateRestarting}, {State: composemodel.ReplicaStateRestarting}},
			want:     composemodel.ComposeProjectStateDegraded,
		},
		{
			name:     "every replica created and never started is stopped",
			replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateCreated}},
			want:     composemodel.ComposeProjectStateStopped,
		},
		{name: "a replica unhealthy", replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateRunning, Health: composemodel.ReplicaHealthUnhealthy}}, want: composemodel.ComposeProjectStateDegraded},
		{name: "a healthcheck starting", replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateRunning, Health: composemodel.ReplicaHealthStarting}}, want: composemodel.ComposeProjectStateStarting},
		{name: "every replica up", replicas: []composemodel.ReplicaStatus{running, running}, want: composemodel.ComposeProjectStateRunning},
		{
			name:     "one missing beside every container stopped is degraded",
			replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateExited}, {State: composemodel.ReplicaStateMissing}},
			want:     composemodel.ComposeProjectStateDegraded,
		},
		{
			name:     "one dead beside every container stopped is degraded",
			replicas: []composemodel.ReplicaStatus{{State: composemodel.ReplicaStateExited, ExitCode: 137}, {State: composemodel.ReplicaStateDead}},
			want:     composemodel.ComposeProjectStateDegraded,
		},
		{
			name: "an unhealthy replica outranks a starting one",
			replicas: []composemodel.ReplicaStatus{
				{State: composemodel.ReplicaStateRunning, Health: composemodel.ReplicaHealthStarting},
				{State: composemodel.ReplicaStateRunning, Health: composemodel.ReplicaHealthUnhealthy},
			},
			want: composemodel.ComposeProjectStateDegraded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newComposeProjectState([]composemodel.ServiceStatus{{Replicas: tt.replicas}}))
		})
	}
}
