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

package status

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	savepointrepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	sessionrepo "github.com/greenmaskio/arcave/internal/repository/session"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

var (
	statusCheckedAt = time.Date(2026, 11, 7, 17, 14, 0, 0, time.UTC)
	statusStarted   = time.Date(2026, 11, 7, 14, 2, 30, 0, time.UTC)
	statusFeat      = model.Session{ID: model.SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x01}, Name: "feat"}
	statusFeatTwin  = model.Session{ID: model.SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x02}, Name: "feat"}
	statusDeployed  = model.Savepoint{
		ID:             "019e843e-1650-7000-8000-000000000000",
		Name:           "before-migration",
		Description:    "the schema before 0042",
		CreatedAt:      time.Date(2026, 11, 6, 19, 41, 0, 0, time.UTC),
		Stats:          []model.SnapshotStat{{Volume: demoVolume()}},
		WritableLayers: []model.WritableLayer{demoWritableLayer()},
	}
	statusNewer = model.Savepoint{
		ID:        "019e9000-0000-7000-8000-000000000000",
		Name:      "after-fix",
		CreatedAt: time.Date(2026, 11, 7, 16, 14, 0, 0, time.UTC),
	}
)

func TestFindDeployments(t *testing.T) {
	deployment := newStatusDeployment(statusDeployed, model.DefaultSessionRecord)
	earlier := newStatusDeployment(statusNewer, model.DefaultSessionRecord)
	earlier.DeployedAt = deployment.DeployedAt.Add(-time.Hour)
	broken := deployment.Labels()
	broken[string(model.SystemLabelDeploymentSessionID)] = "nope"
	tests := []struct {
		name           string
		labels         [][]map[string]string // per service, per replica
		wantSource     model.DeploymentSource
		wantDeployed   bool
		wantLabelled   int
		wantUnreadable int
	}{
		{name: "no container is no source"},
		{
			name:         "every container of the deployment",
			labels:       [][]map[string]string{{deployment.Labels(), deployment.Labels()}},
			wantSource:   model.DeploymentSourceSavepoint,
			wantDeployed: true,
			wantLabelled: 2,
		},
		{
			name:         "some containers of none",
			labels:       [][]map[string]string{{nil}, {deployment.Labels()}},
			wantSource:   model.DeploymentSourceMixed,
			wantDeployed: true,
			wantLabelled: 1,
		},
		{
			name:       "no container of any",
			labels:     [][]map[string]string{{nil, nil}},
			wantSource: model.DeploymentSourceCompose,
		},
		{
			name:         "an earlier deployment a restored layer carried counts as none",
			labels:       [][]map[string]string{{earlier.Labels()}, {deployment.Labels()}},
			wantSource:   model.DeploymentSourceMixed,
			wantDeployed: true,
			wantLabelled: 1,
		},
		{
			name:         "the latest deployment whatever the order",
			labels:       [][]map[string]string{{deployment.Labels()}, {earlier.Labels(), deployment.Labels()}},
			wantSource:   model.DeploymentSourceMixed,
			wantDeployed: true,
			wantLabelled: 2,
		},
		{
			name:           "unreadable labels are kept apart and count as none",
			labels:         [][]map[string]string{{deployment.Labels(), broken}},
			wantSource:     model.DeploymentSourceMixed,
			wantDeployed:   true,
			wantLabelled:   1,
			wantUnreadable: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var services []model.ServiceContainers
			for i, replicas := range tt.labels {
				service := model.ServiceContainers{Name: fmt.Sprintf("svc%d", i)}
				for number, labels := range replicas {
					service.Containers = append(service.Containers, newStatusContainer(service.Name, number+1, labels, runningState()))
				}
				services = append(services, service)
			}

			// Act
			deployments := findDeployments(model.ComposeProjectContainers{Services: services})

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
		name  string
		state container.State
		job   bool
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
		{name: "a job that exited 0 completed", state: container.State{Status: container.StateExited}, job: true, want: model.ReplicaStateCompleted},
		{name: "a job that failed exited", state: container.State{Status: container.StateExited, ExitCode: 1}, job: true, want: model.ReplicaStateExited},
		{name: "a job still running runs", state: container.State{Status: container.StateRunning, Running: true}, job: true, want: model.ReplicaStateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newReplicaState(tt.state, tt.job))
		})
	}
}

func TestNewServiceState(t *testing.T) {
	var (
		running   = model.ReplicaStatus{State: model.ReplicaStateRunning}
		completed = model.ReplicaStatus{State: model.ReplicaStateCompleted}
		exited    = model.ReplicaStatus{State: model.ReplicaStateExited}
	)
	tests := []struct {
		name     string
		replicas []model.ReplicaStatus
		want     model.ReplicaState
	}{
		{name: "every replica completed", replicas: []model.ReplicaStatus{completed, completed}, want: model.ReplicaStateCompleted},
		{name: "a completed replica beside a running one runs", replicas: []model.ReplicaStatus{completed, running}, want: model.ReplicaStateRunning},
		{name: "an exited replica outranks a completed one", replicas: []model.ReplicaStatus{completed, exited}, want: model.ReplicaStateExited},
		{name: "a service with no replica runs", want: model.ReplicaStateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newServiceState(tt.replicas))
		})
	}
}

func TestNewServiceStatuses(t *testing.T) {
	deployment := newStatusDeployment(statusDeployed, model.DefaultSessionRecord)
	deployment.ServiceReplicas = map[string][]int{
		"web":     {1, 3},
		"migrate": {1},
	}
	labels := deployment.Labels()
	tests := []struct {
		name       string
		services   []model.ServiceContainers
		deployment *model.SavepointDeployment
		want       []model.ServiceStatus
	}{
		{
			name: "a recorded gap is no missing replica, a recorded replica the daemon lacks is",
			services: []model.ServiceContainers{{Name: "web", Containers: []model.Container{
				newStatusContainer("web", 1, labels, runningState()),
			}}},
			deployment: &deployment,
			want: []model.ServiceStatus{
				{Name: "migrate", State: model.ReplicaStateMissing, ReplicasExpected: 1, Replicas: []model.ReplicaStatus{
					{Number: 1, State: model.ReplicaStateMissing, InSavepoint: true},
				}},
				{Name: "web", State: model.ReplicaStateMissing, ReplicasExpected: 2, ReplicasUp: 1, Replicas: []model.ReplicaStatus{
					newRunningStatusReplica("web", 1, true, true),
					{Number: 3, State: model.ReplicaStateMissing, InSavepoint: true},
				}},
			},
		},
		{
			name: "a replica added since, one rebuilt outside arcave, and a job another waits on that exited 0",
			services: []model.ServiceContainers{{Name: "web", AwaitsCompletion: []string{"migrate"}, Containers: []model.Container{
				newStatusContainer("web", 1, nil, runningState()),
				newStatusContainer("web", 2, labels, runningState()),
				newStatusContainer("web", 3, labels, runningState()),
			}}, {Name: "migrate", Containers: []model.Container{
				newStatusContainer("migrate", 1, labels, container.State{Status: container.StateExited}),
			}}},
			deployment: &deployment,
			want: []model.ServiceStatus{
				{Name: "migrate", State: model.ReplicaStateCompleted, ReplicasExpected: 1, ReplicasUp: 1, Replicas: []model.ReplicaStatus{
					withReplicaState(newRunningStatusReplica("migrate", 1, true, true), model.ReplicaStateCompleted),
				}},
				{Name: "web", State: model.ReplicaStateRunning, ReplicasExpected: 2, ReplicasUp: 3, Replicas: []model.ReplicaStatus{
					newRunningStatusReplica("web", 1, true, false),
					newRunningStatusReplica("web", 2, false, true),
					newRunningStatusReplica("web", 3, true, true),
				}},
			},
		},
		{
			name: "with none deployed nothing is expected, so nothing is missing; an exit 0 no service waits on is no job's",
			services: []model.ServiceContainers{{Name: "migrate", Containers: []model.Container{
				newStatusContainer("migrate", 1, nil, container.State{Status: container.StateExited}),
			}}},
			want: []model.ServiceStatus{
				{Name: "migrate", State: model.ReplicaStateExited, Replicas: []model.ReplicaStatus{
					withReplicaState(newRunningStatusReplica("migrate", 1, false, false), model.ReplicaStateExited),
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			containers := model.ComposeProjectContainers{Services: tt.services}
			deployments := findDeployments(containers)
			require.Equal(t, tt.deployment != nil, deployments.latest != nil)

			// Act
			services := newServiceStatuses(containers, deployments)

			// Assert
			assert.Equal(t, tt.want, withoutReplicaTimes(services))
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

func TestNewComposeProjectState(t *testing.T) {
	running := model.ReplicaStatus{State: model.ReplicaStateRunning}
	tests := []struct {
		name     string
		pending  bool
		replicas []model.ReplicaStatus
		want     model.ComposeProjectState
	}{
		{name: "a marker outranks everything", pending: true, replicas: []model.ReplicaStatus{running}, want: model.ComposeProjectStateRollbackPending},
		{name: "no container", want: model.ComposeProjectStateAbsent},
		{name: "every recorded replica missing", replicas: []model.ReplicaStatus{{State: model.ReplicaStateMissing}}, want: model.ComposeProjectStateAbsent},
		{
			name:     "every container stopped",
			replicas: []model.ReplicaStatus{{State: model.ReplicaStateExited}, {State: model.ReplicaStateExited, ExitCode: 1}},
			want:     model.ComposeProjectStateStopped,
		},
		{name: "a replica missing", replicas: []model.ReplicaStatus{running, {State: model.ReplicaStateMissing}}, want: model.ComposeProjectStateDegraded},
		{name: "a replica failed", replicas: []model.ReplicaStatus{running, {State: model.ReplicaStateExited, ExitCode: 1}}, want: model.ComposeProjectStateDegraded},
		{
			name:     "every container stopped, a completed job aside",
			replicas: []model.ReplicaStatus{{State: model.ReplicaStateExited}, {State: model.ReplicaStateCompleted}},
			want:     model.ComposeProjectStateStopped,
		},
		{name: "only completed jobs", replicas: []model.ReplicaStatus{{State: model.ReplicaStateCompleted}}, want: model.ComposeProjectStateRunning},
		{name: "a completed job beside a running replica", replicas: []model.ReplicaStatus{running, {State: model.ReplicaStateCompleted}}, want: model.ComposeProjectStateRunning},
		{
			name:     "a replica that exited 0 no service waits on degrades nothing: it may be a server stopped by hand",
			replicas: []model.ReplicaStatus{running, {State: model.ReplicaStateExited}},
			want:     model.ComposeProjectStateRunning,
		},
		{name: "a replica paused", replicas: []model.ReplicaStatus{running, {State: model.ReplicaStatePaused}}, want: model.ComposeProjectStateDegraded},
		{
			name:     "every replica paused is degraded, not stopped",
			replicas: []model.ReplicaStatus{{State: model.ReplicaStatePaused}, {State: model.ReplicaStatePaused}},
			want:     model.ComposeProjectStateDegraded,
		},
		{
			name:     "every replica restarting is degraded, not stopped",
			replicas: []model.ReplicaStatus{{State: model.ReplicaStateRestarting}, {State: model.ReplicaStateRestarting}},
			want:     model.ComposeProjectStateDegraded,
		},
		{
			name:     "every replica created and never started is stopped",
			replicas: []model.ReplicaStatus{{State: model.ReplicaStateCreated}},
			want:     model.ComposeProjectStateStopped,
		},
		{name: "a replica unhealthy", replicas: []model.ReplicaStatus{{State: model.ReplicaStateRunning, Health: model.ReplicaHealthUnhealthy}}, want: model.ComposeProjectStateDegraded},
		{name: "a healthcheck starting", replicas: []model.ReplicaStatus{{State: model.ReplicaStateRunning, Health: model.ReplicaHealthStarting}}, want: model.ComposeProjectStateStarting},
		{name: "every replica up", replicas: []model.ReplicaStatus{running, running}, want: model.ComposeProjectStateRunning},
		{name: "a marker with no container", pending: true, want: model.ComposeProjectStateRollbackPending},
		{
			name:     "one missing beside every container stopped is degraded",
			replicas: []model.ReplicaStatus{{State: model.ReplicaStateExited}, {State: model.ReplicaStateMissing}},
			want:     model.ComposeProjectStateDegraded,
		},
		{
			name:     "one dead beside every container stopped is degraded",
			replicas: []model.ReplicaStatus{{State: model.ReplicaStateExited, ExitCode: 137}, {State: model.ReplicaStateDead}},
			want:     model.ComposeProjectStateDegraded,
		},
		{
			name: "an unhealthy replica outranks a starting one",
			replicas: []model.ReplicaStatus{
				{State: model.ReplicaStateRunning, Health: model.ReplicaHealthStarting},
				{State: model.ReplicaStateRunning, Health: model.ReplicaHealthUnhealthy},
			},
			want: model.ComposeProjectStateDegraded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newComposeProjectState(tt.pending, []model.ServiceStatus{{Replicas: tt.replicas}}))
		})
	}
}

func TestService_Status(t *testing.T) {
	// Status reads the clock itself: a container's times are set off it, so an age reads the same
	// however long the run takes.
	now := time.Now().UTC()
	started := now.Add(-(3*time.Hour + 11*time.Minute + 30*time.Second))
	finished := now.Add(-(4*time.Minute + 30*time.Second))
	baselineDeployment := newStatusDeployment(statusDeployed, model.DefaultSessionRecord)
	featDeployment := newStatusDeployment(statusDeployed, statusFeat)
	otherProjectDeployment := newStatusDeployment(statusDeployed, statusFeat)
	otherProjectDeployment.ArcaveProject = "other-checkout"
	twoReplicas := newStatusDeployment(statusDeployed, model.DefaultSessionRecord)
	twoReplicas.ServiceReplicas[demoService] = []int{1, 2}
	twoLayers := statusDeployed
	secondLayer := demoWritableLayer()
	secondLayer.Replica, secondLayer.ContainerName = 2, "demo-db-2"
	twoLayers.WritableLayers = []model.WritableLayer{demoWritableLayer(), secondLayer}
	leftOut := statusDeployed
	leftOut.NotCovered = &model.NotCovered{
		Mounts: []model.SkippedVolume{
			{Service: demoService, Target: "/cache", Volume: "demo_cache", Reason: model.SkipReasonExcluded},
			{Service: demoService, Target: "/tmp", Reason: model.SkipReasonTmpfs},
		},
		WritableLayers: []model.SkippedWritableLayer{{Service: demoService, Replica: 1, Reason: model.SkipReasonExcluded}},
	}
	safety := model.Savepoint{
		ID: "019e8500-0000-7000-8000-000000000000", Name: "safety-snapshot-x",
		Labels: map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}, CreatedAt: statusDeployed.CreatedAt.Add(time.Hour),
	}
	tests := []struct {
		name string
		// containers runs one db replica per entry of labels, in state.
		labels   []map[string]string
		state    container.State
		replaces string
		// stored are the savepoints of the default session's compose project.
		stored   []model.Savepoint
		sessions []model.Session
		pointer  *model.SessionID
		// pointerErr is the current-session pointer's read failing.
		pointerErr error
		ref        string
		marker     *model.InflightMarker
		// markerErr is the inflight marker's read failing.
		markerErr error
		storeErr  error
		// sessionsErr and savepointsErr fail every read of sessions, of savepoints.
		sessionsErr   error
		savepointsErr error
		verify        func(t *testing.T, status model.ComposeProjectStatus)
		wantErr       error
		// wantAmbiguous expects the error to name a session name several sessions carry.
		wantAmbiguous bool
	}{
		{
			name:   "a rollback deployed every container and the store holds it",
			labels: []map[string]string{baselineDeployment.Labels()},
			state:  runningStateAt(started),
			stored: []model.Savepoint{statusDeployed, statusNewer},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStateRunning, status.State)
				assert.False(t, status.CheckedAt.Before(now), "checked when it runs")
				assert.Equal(t, &model.DeploymentStatus{
					Source:        model.DeploymentSourceSavepoint,
					DeployedAt:    baselineDeployment.DeployedAt,
					ArcaveProject: demoArcaveProject,
					Session:       model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
					Savepoint: &model.DeployedSavepointStatus{
						DeployedSavepoint: model.DeployedSavepoint{
							ID:        statusDeployed.ID,
							Name:      statusDeployed.Name,
							CreatedAt: statusDeployed.CreatedAt,
						},
						Description: statusDeployed.Description,
						Restored:    &model.RestoredCounts{Volumes: 1, WritableLayers: 1},
					},
					Record: model.DeploymentRecordFound,
				}, status.Deployment, "the labels say what was deployed, the record what it restored")
				assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, status.Session)
				assert.Empty(t, status.Notices)
			},
		},
		{
			name:     "a container recreated by compose outside arcave",
			labels:   []map[string]string{twoReplicas.Labels(), nil},
			state:    runningStateAt(started),
			replaces: "demo-db-2",
			stored:   []model.Savepoint{twoLayers},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentSourceMixed, status.Deployment.Source)
				assert.Equal(t, 1, status.Deployment.Replaced, "the replica the notice names, and no other")
				assert.Equal(t, model.ComposeProjectStateRunning, status.State)
				assertNotice(t, status.Notices, model.NoticeCodeReplicaRecreated, "db-2 was recreated outside arcave 3h 11m ago "+
					"(compose replaced demo-db-2): the writable layer the rollback restored is gone; volumes are untouched")
			},
		},
		{
			name:   "a replica whose labels do not parse is not called recreated",
			labels: []map[string]string{twoReplicas.Labels(), corruptDeployedAt(twoReplicas.Labels())},
			state:  runningStateAt(started),
			stored: []model.Savepoint{twoLayers},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Zero(t, status.Deployment.Replaced, "whether the deployment built it is unknown")
				require.Len(t, status.Notices, 1, "the unreadable labels alone: %v", status.Notices)
				assert.Equal(t, model.NoticeCodeDeploymentLabelsUnreadable, status.Notices[0].Code)
			},
		},
		{
			name:     "a container recreated outside arcave with no record says nothing of its layer",
			labels:   []map[string]string{twoReplicas.Labels(), nil},
			state:    runningStateAt(started),
			replaces: "demo-db-2",
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assertNotice(t, status.Notices, model.NoticeCodeReplicaRecreated,
					"db-2 was recreated outside arcave 3h 11m ago (compose replaced demo-db-2); volumes are untouched")
			},
		},
		{
			name:   "what the record left out",
			labels: []map[string]string{baselineDeployment.Labels()},
			state:  runningStateAt(started),
			stored: []model.Savepoint{leftOut},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, []model.Notice{
					{Code: model.NoticeCodeVolumeNotRestored,
						Message: "volume demo_cache was not in the savepoint: it holds what it held before the rollback"},
					{Code: model.NoticeCodeLayerNotRestored,
						Message: "db-1's writable layer was not in the savepoint: it started fresh from its image"},
				}, status.Notices, "a tmpfs holds nothing to restore")
			},
		},
		{
			name:     "deployed from one session, commands working in another",
			labels:   []map[string]string{baselineDeployment.Labels()},
			state:    runningStateAt(started),
			stored:   []model.Savepoint{statusDeployed},
			pointer:  &statusFeat.ID,
			sessions: []model.Session{statusFeat},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: statusFeat.ID, Name: statusFeat.Name}, status.Session)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordFound, status.Deployment.Record, "the deployed session is read")
				assertNotice(t, status.Notices, model.NoticeCodeOtherSession, "running savepoint before-migration "+
					"(019e843e-1650-7000-8000-000000000000) of session baseline; arcave commands act on session feat")
			},
		},
		{
			name:   "deployed by another arcave project: its store is not read, and nothing reads gone",
			labels: []map[string]string{otherProjectDeployment.Labels()},
			state:  runningStateAt(started),
			stored: []model.Savepoint{statusNewer},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.ArcaveProjectName("other-checkout"), status.Deployment.ArcaveProject)
				assert.Empty(t, status.Deployment.Record, "another arcave project's store is not read")
				assert.Equal(t, []model.Notice{{
					Code: model.NoticeCodeOtherArcaveProject,
					Message: "running savepoint before-migration (019e843e-1650-7000-8000-000000000000) of arcave project " +
						"other-checkout; arcave commands act on arcave project demo-arcave",
				}}, status.Notices, "no deployed-savepoint-gone, and no other-session: the session is another project's")
			},
		},
		{
			name:   "deployed from a session since removed",
			labels: []map[string]string{featDeployment.Labels()},
			state:  runningStateAt(started),
			stored: []model.Savepoint{statusDeployed},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordMissing, status.Deployment.Record)
				assert.Equal(t, statusDeployed.Name, status.Deployment.Savepoint.Name, "the labels name it with no record")
				assert.Empty(t, status.Deployment.Savepoint.Description)
				assert.Nil(t, status.Deployment.Savepoint.Restored)
				assertNotice(t, status.Notices, model.NoticeCodeDeployedSavepointGone,
					"savepoint before-migration (019e843e-1650-7000-8000-000000000000) is no longer in session feat")
				assertNotice(t, status.Notices, model.NoticeCodeOtherSession, "running savepoint before-migration "+
					"(019e843e-1650-7000-8000-000000000000) of session feat; arcave commands act on session baseline")
			},
		},
		{
			name:   "a deployed savepoint removed since",
			labels: []map[string]string{baselineDeployment.Labels()},
			state:  runningStateAt(started),
			stored: []model.Savepoint{statusNewer},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordMissing, status.Deployment.Record)
				assert.Equal(t, []model.Notice{{
					Code:    model.NoticeCodeDeployedSavepointGone,
					Message: "savepoint before-migration (019e843e-1650-7000-8000-000000000000) is no longer in session baseline",
				}}, status.Notices)
			},
		},
		{
			name:   "a rollback in flight",
			labels: []map[string]string{baselineDeployment.Labels()},
			state:  container.State{Status: container.StateCreated},
			stored: []model.Savepoint{statusDeployed},
			marker: &model.InflightMarker{Phase: model.InflightRestoring, Session: model.DefaultSession,
				ComposeProject: demoProject, Target: statusDeployed.ID, Safety: safety.ID, At: statusCheckedAt.Add(-time.Minute)},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStateRollbackPending, status.State)
				require.NotNil(t, status.RollbackInFlight)
				assert.Equal(t, safety.ID, status.RollbackInFlight.Recover)
				require.NotEmpty(t, status.Notices)
				assert.Equal(t, model.Notice{
					Code:    model.NoticeCodeRollbackPending,
					Message: "a rollback to 019e843e-1650-7000-8000-000000000000 is in progress or was interrupted while restoring the volumes",
					Severe:  true,
				}, status.Notices[0])
			},
		},
		{
			name:   "another compose project's marker is not this one's",
			labels: []map[string]string{baselineDeployment.Labels()},
			state:  runningStateAt(started),
			stored: []model.Savepoint{statusDeployed},
			marker: &model.InflightMarker{Phase: model.InflightRestoring, Session: model.DefaultSession,
				ComposeProject: "other", Target: statusDeployed.ID},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStateRunning, status.State)
				assert.Nil(t, status.RollbackInFlight)
			},
		},
		{
			name:   "every container stopped",
			labels: []map[string]string{baselineDeployment.Labels()},
			state:  container.State{Status: container.StateExited, FinishedAt: finished.Format(time.RFC3339Nano)},
			stored: []model.Savepoint{statusDeployed},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStateStopped, status.State)
				assert.Equal(t, []model.Notice{{Code: model.NoticeCodeComposeProjectStopped, Message: "no container runs"}},
					status.Notices, "a clean exit adds nothing to a stopped project")
			},
		},
		{
			name:   "nothing running",
			stored: []model.Savepoint{statusDeployed},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStateAbsent, status.State)
				assert.Nil(t, status.Deployment)
				assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, status.Session)
				encoded, err := json.Marshal(status)
				require.NoError(t, err)
				assert.Contains(t, string(encoded), `"services":[]`, "an empty list is [], never null")
				assert.Contains(t, string(encoded), `"unreadable":[]`)
				assert.Contains(t, string(encoded), `"notices":[]`)
			},
		},
		{
			name:     "a store that does not open leaves the daemon's part whole",
			labels:   []map[string]string{baselineDeployment.Labels()},
			state:    runningStateAt(started),
			storeErr: model.ErrWrongStoreKey,
			wantErr:  model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStateRunning, status.State)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordUnreadable, status.Deployment.Record)
				assert.Equal(t, statusDeployed.Name, status.Deployment.Savepoint.Name, "the labels name it with no store")
				assert.Nil(t, status.Session)
				require.Len(t, status.Unreadable, 1)
				assert.Equal(t, model.StatusUnreadableStore, status.Unreadable[0].Source)
				assertNotice(t, status.Notices, model.NoticeCodeStoreUnreadable,
					"the store could not be read: "+model.ErrWrongStoreKey.Error()+"; showing the daemon alone")
				require.Len(t, status.Services, 1)
				assert.Equal(t, 1, status.Services[0].ReplicasUp)
			},
		},
		{
			name:     "a store that does not open reads no record of another arcave project",
			labels:   []map[string]string{otherProjectDeployment.Labels()},
			state:    runningStateAt(started),
			storeErr: model.ErrWrongStoreKey,
			wantErr:  model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Empty(t, status.Deployment.Record, "another arcave project's record is not read, store or not")
			},
		},
		{
			name:   "a safety snapshot deployed",
			labels: []map[string]string{newStatusDeployment(safety, model.DefaultSessionRecord).Labels()},
			state:  runningStateAt(started),
			stored: []model.Savepoint{statusDeployed, safety},
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.True(t, status.Deployment.Savepoint.IsSafetySnapshot)
				assert.Equal(t, []model.Notice{{
					Code:    model.NoticeCodeSafetySnapshotDeployed,
					Message: "running safety-snapshot-x (019e8500-0000-7000-8000-000000000000), the safety snapshot of a rollback",
				}}, status.Notices)
			},
		},
		{
			name:     "a session named on the request",
			labels:   []map[string]string{baselineDeployment.Labels()},
			state:    runningStateAt(started),
			stored:   []model.Savepoint{statusDeployed},
			sessions: []model.Session{statusFeat},
			ref:      "feat",
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: statusFeat.ID, Name: statusFeat.Name}, status.Session)
			},
		},
		{
			name:     "a session named by id on the request",
			labels:   []map[string]string{baselineDeployment.Labels()},
			state:    runningStateAt(started),
			stored:   []model.Savepoint{statusDeployed},
			sessions: []model.Session{statusFeat},
			ref:      statusFeat.ID.String(),
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: statusFeat.ID, Name: statusFeat.Name}, status.Session)
			},
		},
		{
			name:     "a session ref outranks the pointer",
			labels:   []map[string]string{baselineDeployment.Labels()},
			state:    runningStateAt(started),
			stored:   []model.Savepoint{statusDeployed},
			sessions: []model.Session{statusFeat},
			pointer:  &statusFeat.ID,
			ref:      model.DefaultSessionName,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, status.Session)
			},
		},
		{
			name:     "a session name two sessions carry is the user's error",
			labels:   []map[string]string{baselineDeployment.Labels()},
			state:    runningStateAt(started),
			stored:   []model.Savepoint{statusDeployed},
			sessions: []model.Session{statusFeat, statusFeatTwin},
			ref:      statusFeat.Name,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStatus{}, status)
			},
			wantAmbiguous: true,
		},
		{
			name:       "a pointer that cannot be read is partial",
			labels:     []map[string]string{baselineDeployment.Labels()},
			state:      runningStateAt(started),
			stored:     []model.Savepoint{statusDeployed},
			pointerErr: errors.New("current_session: not a session id"),
			wantErr:    model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				assert.Equal(t, []model.StatusUnreadable{{
					Source: model.StatusUnreadableSession,
					Error:  "current_session: not a session id",
				}}, status.Unreadable)
			},
		},
		{
			name:    "a session ref that names none is the user's error",
			labels:  []map[string]string{baselineDeployment.Labels()},
			state:   runningStateAt(started),
			stored:  []model.Savepoint{statusDeployed},
			ref:     "nope",
			wantErr: model.ErrSessionNotFound,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStatus{}, status)
			},
		},
		{
			name:    "a keyword session ref is the user's error",
			labels:  []map[string]string{baselineDeployment.Labels()},
			state:   runningStateAt(started),
			stored:  []model.Savepoint{statusDeployed},
			ref:     model.RefCurrent,
			wantErr: model.ErrSessionRefKeyword,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStatus{}, status)
			},
		},
		{
			name:    "a pointer to a session since removed is partial",
			labels:  []map[string]string{baselineDeployment.Labels()},
			state:   runningStateAt(started),
			stored:  []model.Savepoint{statusDeployed},
			pointer: &statusFeat.ID,
			wantErr: model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				require.Len(t, status.Unreadable, 1)
				assert.Equal(t, model.StatusUnreadableSession, status.Unreadable[0].Source)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordFound, status.Deployment.Record, "the deployed session is still read")
				require.NotEmpty(t, status.Notices)
				assert.Equal(t, model.NoticeCodeSessionUnresolved, status.Notices[0].Code)
			},
		},
		{
			name:        "sessions that cannot be listed leave the session unresolved and the record read",
			labels:      []map[string]string{baselineDeployment.Labels()},
			state:       runningStateAt(started),
			stored:      []model.Savepoint{statusDeployed},
			pointer:     &statusFeat.ID,
			sessionsErr: errors.New("list failed"),
			wantErr:     model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordFound, status.Deployment.Record)
				assert.Equal(t, []model.StatusUnreadable{{
					Source: model.StatusUnreadableSession,
					Error:  "listing sessions: list failed",
				}}, status.Unreadable)
				assertNotice(t, status.Notices, model.NoticeCodeSessionUnresolved,
					"the current session could not be resolved: listing sessions: list failed")
			},
		},
		{
			name:        "a session named on the request whose sessions cannot be listed names it",
			labels:      []map[string]string{baselineDeployment.Labels()},
			state:       runningStateAt(started),
			stored:      []model.Savepoint{statusDeployed},
			ref:         "feat",
			sessionsErr: errors.New("list failed"),
			wantErr:     model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Nil(t, status.Session)
				assertNotice(t, status.Notices, model.NoticeCodeSessionUnresolved,
					`session "feat" could not be resolved: listing sessions: list failed`)
			},
		},
		{
			name:          "a record that cannot be read is unreadable",
			labels:        []map[string]string{baselineDeployment.Labels()},
			state:         runningStateAt(started),
			savepointsErr: errors.New("read failed"),
			wantErr:       model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				require.NotNil(t, status.Deployment)
				assert.Equal(t, model.DeploymentRecordUnreadable, status.Deployment.Record)
				assert.Nil(t, status.Deployment.Savepoint.Restored)
				assert.Equal(t, []model.StatusUnreadable{{
					Source: model.StatusUnreadableStore,
					Error:  "read savepoint 019e843e-1650-7000-8000-000000000000 of compose project demo: read failed",
				}}, status.Unreadable)
			},
		},
		{
			name:      "a marker that cannot be read is partial",
			labels:    []map[string]string{baselineDeployment.Labels()},
			state:     runningStateAt(started),
			stored:    []model.Savepoint{statusDeployed},
			markerErr: errors.New("inflight.json: unexpected end of JSON input"),
			wantErr:   model.ErrStatusPartial,
			verify: func(t *testing.T, status model.ComposeProjectStatus) {
				assert.Equal(t, model.ComposeProjectStateRunning, status.State)
				require.Len(t, status.Unreadable, 1)
				assert.Equal(t, model.StatusUnreadableInflightMarker, status.Unreadable[0].Source)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			store := newDirStore(t)
			seedStatusStore(t, store, tt.sessions, tt.stored)
			service := model.ServiceContainers{Name: demoService}
			for i, labels := range tt.labels {
				replica := newStatusContainer(demoService, i+1, labels, tt.state)
				replica.Inspect.Created = started.Format(time.RFC3339Nano)
				if labels == nil {
					replica.Replaces = tt.replaces
				}
				service.Containers = append(service.Containers, replica)
			}
			var containers model.ComposeProjectContainers
			if len(service.Containers) > 0 {
				containers.Services = []model.ServiceContainers{service}
			}
			reader := &mockContainersReader{}
			reader.On("ReadContainers", mock.Anything).Return(containers, nil).Once()
			marker := &mockInflight{}
			if tt.marker != nil {
				marker.On("Get", mock.Anything).Return(*tt.marker, true, nil).Once()
			} else {
				marker.On("Get", mock.Anything).Return(nil, false, tt.markerErr).Once()
			}
			svc := New(Deps{
				ComposeContainers: reader,
				Inflight:          marker,
				Current:           &fakePointer{id: tt.pointer, err: tt.pointerErr},
				OpenArcaveProjectStore: func(context.Context) (storages.Storager, error) {
					if tt.storeErr != nil {
						return nil, tt.storeErr
					}
					return store, nil
				},
				NewSessionReader: func(store storages.Storager) SessionReader {
					if tt.sessionsErr != nil {
						return failingReader{err: tt.sessionsErr}
					}
					return sessionrepo.New(store)
				},
				NewSavepointLoader: func(store storages.Storager) SavepointLoader {
					if tt.savepointsErr != nil {
						return failingReader{err: tt.savepointsErr}
					}
					return savepointrepo.New(store)
				},
				ArcaveProject:  demoArcaveProject,
				ComposeProject: demoProject,
			})

			// Act
			status, err := svc.Status(ctx, model.StatusRequest{Session: tt.ref})

			// Assert
			var ambiguous *model.AmbiguousSessionNameError
			switch {
			case tt.wantAmbiguous:
				require.ErrorAs(t, err, &ambiguous)
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
			}
			tt.verify(t, status)
			reader.AssertExpectations(t)
			marker.AssertExpectations(t)
		})
	}
}

func TestService_Status_ADaemonThatCannotBeReadFailsIt(t *testing.T) {
	// Arrange: nothing past the daemon is read, so nothing else is expected.
	reader := &mockContainersReader{}
	reader.On("ReadContainers", mock.Anything).Return(model.ComposeProjectContainers{}, errors.New("daemon unreachable")).Once()
	svc := New(Deps{
		ComposeContainers: reader,
		ComposeProject:    demoProject,
	})

	// Act
	status, err := svc.Status(zerolog.Nop().WithContext(context.Background()), model.StatusRequest{})

	// Assert
	require.ErrorContains(t, err, "daemon unreachable")
	assert.Equal(t, model.ComposeProjectStatus{}, status)
	reader.AssertExpectations(t)
}

// newStatusDeployment is a rollback to savepoint from session, one replica of demoService recorded.
func newStatusDeployment(savepoint model.Savepoint, session model.Session) model.SavepointDeployment {
	return model.SavepointDeployment{
		DeployedAt:    time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC),
		ArcaveProject: demoArcaveProject,
		Session:       model.SessionRef{ID: session.ID, Name: session.Name},
		Savepoint: model.DeployedSavepoint{
			ID:               savepoint.ID,
			Name:             savepoint.Name,
			CreatedAt:        savepoint.CreatedAt,
			IsSafetySnapshot: savepoint.IsSafetySnapshot(),
		},
		ServiceReplicas: map[string][]int{demoService: {1}},
	}
}

// newStatusContainer is replica number of service as the daemon reports it, labelled with labels.
func newStatusContainer(service string, number int, labels map[string]string, state container.State) model.Container {
	return model.Container{
		ID:     fmt.Sprintf("%s-%d-id", service, number),
		Name:   fmt.Sprintf("demo-%s-%d", service, number),
		Number: number,
		Labels: labels,
		Inspect: container.InspectResponse{
			State:   &state,
			Created: statusStarted.Format(time.RFC3339Nano),
		},
	}
}

func runningState() container.State {
	return runningStateAt(statusStarted)
}

// runningStateAt is a container running since started.
func runningStateAt(started time.Time) container.State {
	return container.State{
		Status:    container.StateRunning,
		Running:   true,
		StartedAt: started.Format(time.RFC3339Nano),
	}
}

// newRunningStatusReplica is newStatusContainer of a running replica as a status reports it, its
// times left out.
func newRunningStatusReplica(service string, number int, inSavepoint, inDeployment bool) model.ReplicaStatus {
	return model.ReplicaStatus{
		Number:       number,
		Container:    fmt.Sprintf("demo-%s-%d", service, number),
		ContainerID:  fmt.Sprintf("%s-%d-id", service, number),
		State:        model.ReplicaStateRunning,
		InDeployment: inDeployment,
		InSavepoint:  inSavepoint,
	}
}

func withReplicaState(replica model.ReplicaStatus, state model.ReplicaState) model.ReplicaStatus {
	replica.State = state
	return replica
}

// withoutReplicaTimes drops the times every replica's container carries.
func withoutReplicaTimes(services []model.ServiceStatus) []model.ServiceStatus {
	for i := range services {
		for j := range services[i].Replicas {
			services[i].Replicas[j].CreatedAt, services[i].Replicas[j].StartedAt = nil, nil
		}
	}
	return services
}

// seedStatusStore commits sessions, and savepoints to demoProject in the default session.
func seedStatusStore(t *testing.T, store storages.Storager, sessions []model.Session, savepoints []model.Savepoint) {
	t.Helper()
	ctx := context.Background()
	for _, session := range sessions {
		require.NoError(t, sessionrepo.New(store).SaveSession(ctx, session))
	}
	sessionStore, err := storefactory.Session(store, model.DefaultSession)
	require.NoError(t, err)
	composeProjectStore, err := storefactory.ComposeProject(sessionStore, demoProject)
	require.NoError(t, err)
	for _, savepoint := range savepoints {
		require.NoError(t, savepointrepo.New(composeProjectStore).SaveSavepoint(ctx, savepoint))
	}
}

// assertNotice asserts a notice of code carries message.
func assertNotice(t *testing.T, notices []model.Notice, code model.NoticeCode, message string) {
	t.Helper()
	assert.True(t, slices.ContainsFunc(notices, func(notice model.Notice) bool {
		return notice.Code == code && notice.Message == message
	}), "no %s notice %q among %v", code, message, notices)
}

func TestNewInflightNotice(t *testing.T) {
	tests := []struct {
		phase model.InflightPhase
		want  string
	}{
		{phase: model.InflightRecreating, want: "while recreating the containers"},
		{phase: model.InflightRestoring, want: "while restoring the volumes"},
		{phase: model.InflightRestoreDone, want: "while starting the services"},
		{phase: model.InflightInconsistent, want: "and its revert failed"},
		{phase: "unknown-phase", want: `in phase "unknown-phase"`},
	}
	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			// Arrange
			marker := model.InflightMarker{Phase: tt.phase, Target: statusDeployed.ID}

			// Act
			notice := newInflightNotice(marker)

			// Assert
			assert.Equal(t, model.Notice{
				Code:    model.NoticeCodeRollbackPending,
				Message: "a rollback to 019e843e-1650-7000-8000-000000000000 is in progress or was interrupted " + tt.want,
				Severe:  true,
			}, notice)
		})
	}
}

func TestNewStatusNotices_UnreadableLabels(t *testing.T) {
	// Arrange
	facts := statusFacts{deployments: containerDeployments{unreadable: []unreadableLabels{{
		service: demoService,
		replica: 2,
		err:     errors.New("bad time"),
	}}}}

	// Act
	notices := newStatusNotices(facts)

	// Assert
	assert.Equal(t, []model.Notice{{
		Code:    model.NoticeCodeDeploymentLabelsUnreadable,
		Message: "db-2 carries unreadable deployment labels: bad time; read as deployed by none",
	}}, notices)
}

func TestNewRollbackInFlight_Recover(t *testing.T) {
	tests := []struct {
		name   string
		safety model.SavepointID
		want   model.SavepointID
	}{
		{name: "the safety snapshot recovers a rollback that took one", safety: statusNewer.ID, want: statusNewer.ID},
		{name: "the target recovers a rollback that took none", want: statusDeployed.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			marker := &model.InflightMarker{Phase: model.InflightRestoring, Target: statusDeployed.ID, Safety: tt.safety}

			// Act
			inFlight := newRollbackInFlight(marker)

			// Assert
			require.NotNil(t, inFlight)
			assert.Equal(t, tt.want, inFlight.Recover)
		})
	}
}

func TestNewNotRestoredNotices(t *testing.T) {
	// Arrange
	notCovered := model.NotCovered{
		Mounts: []model.SkippedVolume{
			{Service: "db", Target: "/data", Volume: "demo_cache", Reason: model.SkipReasonExcluded},
			{Service: "web", Target: "/srv", RawType: string(model.VolumeTypeBind), Reason: model.SkipReasonExcluded},
			{Service: "web", Replica: 2, Target: "/tmp/x", Reason: model.SkipReasonExcluded},
			{Service: "web", Target: "/run", Reason: model.SkipReasonTmpfs},
		},
		WritableLayers: []model.SkippedWritableLayer{{Service: "web", Replica: 1}},
	}

	// Act
	notices := newNotRestoredNotices(notCovered)

	// Assert
	assert.Equal(t, []model.Notice{
		{Code: model.NoticeCodeVolumeNotRestored,
			Message: "volume demo_cache was not in the savepoint: it holds what it held before the rollback"},
		{Code: model.NoticeCodeVolumeNotRestored,
			Message: "web /srv was not in the savepoint: the host folder holds what it held before the rollback"},
		{Code: model.NoticeCodeVolumeNotRestored, Message: "web-2 /tmp/x was not in the savepoint: it came back empty"},
		{Code: model.NoticeCodeLayerNotRestored,
			Message: "web-1's writable layer was not in the savepoint: it started fresh from its image"},
	}, notices, "a mount nothing excluded, a tmpfs, holds nothing to restore")
}

func TestNewReplicaNotices(t *testing.T) {
	deployment := newStatusDeployment(statusDeployed, model.DefaultSessionRecord)
	finished := statusCheckedAt.Add(-4 * time.Minute)
	created := statusCheckedAt.Add(-time.Hour)
	tests := []struct {
		name    string
		replica model.ReplicaStatus
		record  *model.Savepoint
		// state is the compose project's; running when empty.
		state model.ComposeProjectState
		want  []model.Notice
	}{
		{
			name:    "missing",
			replica: model.ReplicaStatus{Number: 2, State: model.ReplicaStateMissing, InSavepoint: true},
			want: []model.Notice{{Code: model.NoticeCodeReplicaMissing, Severe: true,
				Message: "db-2 is missing: savepoint before-migration (019e843e-1650-7000-8000-000000000000) recorded it"}},
		},
		{
			name:    "exited 0 is no failure",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateExited, FinishedAt: &finished, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaExited, Message: "db-1 exited with code 0 4m ago"}},
		},
		{
			name:    "exited 0 with every container stopped is the stopped project's to say",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateExited, FinishedAt: &finished, InDeployment: true, InSavepoint: true},
			state:   model.ComposeProjectStateStopped,
		},
		{
			name: "a failing exit with every container stopped is still a failure",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateExited, ExitCode: 1, FinishedAt: &finished,
				InDeployment: true, InSavepoint: true},
			state: model.ComposeProjectStateStopped,
			want:  []model.Notice{{Code: model.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 1 4m ago"}},
		},
		{
			name: "terminated by a stop it did not handle, every container stopped",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateExited, ExitCode: 143, FinishedAt: &finished,
				InDeployment: true, InSavepoint: true},
			state: model.ComposeProjectStateStopped,
			want: []model.Notice{{Code: model.NoticeCodeReplicaExited, Severe: true,
				Message: "db-1 exited with code 143 (terminated) 4m ago"}},
		},
		{
			name: "out of memory",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateExited, ExitCode: 137, OOMKilled: true,
				InDeployment: true, InSavepoint: true},
			want: []model.Notice{{Code: model.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 137 (out of memory)"}},
		},
		{
			name:    "killed",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateExited, ExitCode: 137, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 137 (killed)"}},
		},
		{
			name:    "terminated",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateExited, ExitCode: 143, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 143 (terminated)"}},
		},
		{
			name:    "dead",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateDead, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaDead, Severe: true, Message: "db-1 is dead"}},
		},
		{
			name:    "removing",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateRemoving, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaRemoving, Severe: true, Message: "db-1 is being removed"}},
		},
		{
			name:    "restarting",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateRestarting, RestartCount: 4, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaRestarting, Severe: true, Message: "db-1 is restarting (4 restarts)"}},
		},
		{
			name:    "created",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateCreated, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaCreated, Severe: true, Message: "db-1 was created but never started"}},
		},
		{
			name:    "paused",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStatePaused, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: model.NoticeCodeReplicaPaused, Message: "db-1 is paused"}},
		},
		{
			name: "running after restarts, unhealthy",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateRunning, RestartCount: 2,
				Health: model.ReplicaHealthUnhealthy, FailingChecks: 5, InDeployment: true, InSavepoint: true},
			want: []model.Notice{
				{Code: model.NoticeCodeReplicaRestarted, Message: "db-1 restarted 2 times since it was created"},
				{Code: model.NoticeCodeReplicaUnhealthy, Severe: true, Message: "db-1 is unhealthy: 5 failing checks"},
			},
		},
		{
			name:    "added after the rollback",
			replica: model.ReplicaStatus{Number: 2, State: model.ReplicaStateRunning},
			want: []model.Notice{{Code: model.NoticeCodeReplicaAdded,
				Message: "db-2 is not in savepoint before-migration (019e843e-1650-7000-8000-000000000000): added after the rollback"}},
		},
		{
			name:    "recreated outside arcave, its layer in the record",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateRunning, CreatedAt: &created, InSavepoint: true},
			record:  &statusDeployed,
			want: []model.Notice{{Code: model.NoticeCodeReplicaRecreated, Message: "db-1 was recreated outside arcave 1h 00m ago: " +
				"the writable layer the rollback restored is gone; volumes are untouched"}},
		},
		{
			name:    "recreated outside arcave, the record holding no layer of it",
			replica: model.ReplicaStatus{Number: 2, State: model.ReplicaStateRunning, CreatedAt: &created, InSavepoint: true},
			record:  &statusDeployed,
			want: []model.Notice{{Code: model.NoticeCodeReplicaRecreated,
				Message: "db-2 was recreated outside arcave 1h 00m ago; volumes are untouched"}},
		},
		{
			name:    "recreated outside arcave, no record read",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateRunning, CreatedAt: &created, InSavepoint: true},
			want: []model.Notice{{Code: model.NoticeCodeReplicaRecreated,
				Message: "db-1 was recreated outside arcave 1h 00m ago; volumes are untouched"}},
		},
		{
			name:    "as the rollback built it",
			replica: model.ReplicaStatus{Number: 1, State: model.ReplicaStateRunning, InDeployment: true, InSavepoint: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			facts := statusFacts{
				checkedAt:   statusCheckedAt,
				state:       cmp.Or(tt.state, model.ComposeProjectStateRunning),
				deployments: containerDeployments{latest: &deployment},
				view:        storeView{savepoint: tt.record},
			}

			// Act
			notices := newReplicaNotices(facts, demoService, tt.replica)

			// Assert
			assert.Equal(t, tt.want, notices)
		})
	}
}

// corruptDeployedAt is labels with a deployment time that does not parse.
func corruptDeployedAt(labels map[string]string) map[string]string {
	corrupt := maps.Clone(labels)
	corrupt[string(model.SystemLabelDeploymentDeployedAt)] = "not-a-time"
	return corrupt
}
