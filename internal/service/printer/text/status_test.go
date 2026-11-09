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

package text

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	checkedAt  = time.Date(2026, 11, 7, 17, 14, 0, 0, time.UTC)
	deployedAt = time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC)
	startedAt  = deployedAt.Add(30 * time.Second)
	baseline   = model.SessionRef{
		ID:   model.DefaultSession,
		Name: model.DefaultSessionName,
	}
)

func TestPrinter_Print_Status(t *testing.T) {
	tests := []struct {
		name   string
		status model.ComposeProjectStatus
	}{
		{
			name:   "status_running",
			status: newRunningStatus(),
		},
		{
			name: "status_degraded_mixed",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.State = model.ComposeProjectStateDegraded
				status.Deployment.Source = model.DeploymentSourceMixed
				status.Deployment.Replaced = 1
				status.Services[0].Health = model.ReplicaHealthUnhealthy
				status.Services[0].Replicas[0].Health = model.ReplicaHealthUnhealthy
				status.Services[1].Replicas[1].InDeployment = false
				status.Services = append(status.Services, model.ServiceStatus{
					Name: "worker", State: model.ReplicaStateExited, ReplicasExpected: 1,
					Replicas: []model.ReplicaStatus{{
						Number: 1, Container: "myapp-worker-1", State: model.ReplicaStateExited, ExitCode: 1,
						InDeployment: true, InSavepoint: true,
					}},
				})
				status.Notices = []model.Notice{
					{Code: model.NoticeCodeReplicaExited, Message: "worker-1 exited with code 1 (out of memory) 3h 09m ago", Severe: true},
					{Code: model.NoticeCodeReplicaUnhealthy, Message: "db-1 is unhealthy: 5 failing checks", Severe: true},
					{Code: model.NoticeCodeReplicaRecreated, Message: "web-2 was recreated outside arcave 34m ago " +
						"(compose replaced myapp-web-2): the writable layer the rollback restored is gone; volumes are untouched"},
				}
				return status
			}(),
		},
		{
			name: "status_replaced_undated",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.Deployment.Source = model.DeploymentSourceMixed
				status.Deployment.DeployedAt = nil
				status.Deployment.Replaced = 2
				return status
			}(),
		},
		{
			name: "status_compose",
			status: model.ComposeProjectStatus{
				ComposeProject: "myapp",
				CheckedAt:      checkedAt,
				State:          model.ComposeProjectStateRunning,
				Session:        &baseline,
				Deployment:     &model.DeploymentStatus{Source: model.DeploymentSourceCompose},
				Services: []model.ServiceStatus{{
					Name: "db", State: model.ReplicaStateRunning, ReplicasUp: 1,
					Replicas: []model.ReplicaStatus{newRunningReplica("myapp-db-1", 1, publishedPort("127.0.0.1", 5432, 5432))},
				}},
			},
		},
		{
			name: "status_absent",
			status: model.ComposeProjectStatus{
				ComposeProject: "myapp",
				CheckedAt:      checkedAt,
				State:          model.ComposeProjectStateAbsent,
				Session:        &baseline,
			},
		},
		{
			name: "status_rollback_pending",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.State = model.ComposeProjectStateRollbackPending
				status.RollbackInFlight = &model.RollbackInFlight{
					Phase: model.InflightRestoring, Session: model.DefaultSession, Target: savepointID, Safety: safetyID,
					At: checkedAt.Add(-3 * time.Minute), Recover: safetyID,
				}
				status.Notices = []model.Notice{{
					Code:    model.NoticeCodeRollbackPending,
					Message: "a rollback to " + savepointID.String() + " is in progress or was interrupted while restoring the volumes",
					Severe:  true,
				}}
				return status
			}(),
		},
		{
			name: "status_stopped",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.State = model.ComposeProjectStateStopped
				status.Services = status.Services[:1]
				status.Services[0].State, status.Services[0].Health, status.Services[0].ReplicasUp =
					model.ReplicaStateExited, "", 0
				status.Services[0].Replicas[0].State, status.Services[0].Replicas[0].Health =
					model.ReplicaStateExited, ""
				status.Notices = []model.Notice{{
					Code:    model.NoticeCodeReplicaExited,
					Message: "db-1 exited with code 0 4m ago",
				}, {
					Code:    model.NoticeCodeComposeProjectStopped,
					Message: "no container runs",
				}}
				return status
			}(),
		},
		{
			name: "status_partial",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.Session = nil
				status.Deployment.Record = model.DeploymentRecordUnreadable
				status.Deployment.Savepoint.Description, status.Deployment.Savepoint.Restored = "", nil
				status.Unreadable = []model.StatusUnreadable{{Source: model.StatusUnreadableStore, Error: "the store is encrypted"}}
				status.Notices = []model.Notice{{
					Code:    model.NoticeCodeStoreUnreadable,
					Message: "the store could not be read: the store is encrypted; showing the daemon alone",
				}}
				return status
			}(),
		},
		{
			name: "status_other_session",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.Session = &model.SessionRef{ID: sessionID, Name: "feat"}
				status.Notices = []model.Notice{{
					Code: model.NoticeCodeOtherSession,
					Message: "running savepoint before-migration (" + savepointID.String() + ") of session baseline; " +
						"arcave commands act on session feat",
				}}
				return status
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var out bytes.Buffer
			printer := New(&out, Options{ComposeProject: "myapp", Location: time.UTC})

			// Act
			err := printer.Print(tt.status)

			// Assert
			require.NoError(t, err)
			assertGolden(t, tt.name+".golden", out.String())
		})
	}
}

// newRunningStatus is a compose project a rollback deployed, every replica up but a job that
// finished.
func newRunningStatus() model.ComposeProjectStatus {
	return model.ComposeProjectStatus{
		ComposeProject: "myapp",
		CheckedAt:      checkedAt,
		State:          model.ComposeProjectStateRunning,
		Session:        &baseline,
		Deployment: &model.DeploymentStatus{
			Source:     model.DeploymentSourceSavepoint,
			DeployedAt: &deployedAt,
			Session:    &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
			Savepoint: &model.DeployedSavepointStatus{
				ID:          savepointID,
				Name:        "before-migration",
				Description: "the schema before 0042",
				CreatedAt:   &createdAt,
				Restored:    &model.DeploymentCounts{Volumes: 3, WritableLayers: 2},
			},
			Record: model.DeploymentRecordFound,
		},
		Services: []model.ServiceStatus{
			{
				Name: "db", State: model.ReplicaStateRunning, Health: model.ReplicaHealthHealthy,
				ReplicasExpected: 1, ReplicasUp: 1,
				Replicas: []model.ReplicaStatus{withHealth(
					newRunningReplica("myapp-db-1", 1, publishedPort("127.0.0.1", 5432, 5432)), model.ReplicaHealthHealthy)},
			},
			{
				Name: "web", State: model.ReplicaStateRunning, ReplicasExpected: 2, ReplicasUp: 2,
				Replicas: []model.ReplicaStatus{
					newRunningReplica("myapp-web-1", 1, publishedPort("0.0.0.0", 8080, 80), publishedPort("::", 8080, 80)),
					newRunningReplica("myapp-web-2", 2),
				},
			},
			{
				Name: "migrate", State: model.ReplicaStateCompleted, ReplicasExpected: 1, ReplicasUp: 1,
				Replicas: []model.ReplicaStatus{{
					Number: 1, Container: "myapp-migrate-1", State: model.ReplicaStateCompleted, InDeployment: true, InSavepoint: true,
				}},
			},
		},
	}
}

func newRunningReplica(container string, number int, ports ...model.PublishedPort) model.ReplicaStatus {
	return model.ReplicaStatus{
		Number:       number,
		Container:    container,
		State:        model.ReplicaStateRunning,
		StartedAt:    &startedAt,
		InDeployment: true,
		InSavepoint:  true,
		Ports:        ports,
	}
}

func withHealth(replica model.ReplicaStatus, health model.ReplicaHealth) model.ReplicaStatus {
	replica.Health = health
	return replica
}

func publishedPort(hostIP string, hostPort, containerPort int) model.PublishedPort {
	return model.PublishedPort{
		ContainerPort: containerPort,
		Protocol:      "tcp",
		HostIP:        netip.MustParseAddr(hostIP).String(),
		HostPort:      hostPort,
	}
}

func TestFormatServicePorts(t *testing.T) {
	tests := []struct {
		name  string
		ports []model.PublishedPort
		want  string
	}{
		{name: "none"},
		{
			name:  "one port on both unspecified addresses is one",
			ports: []model.PublishedPort{publishedPort("0.0.0.0", 8080, 80), publishedPort("::", 8080, 80)},
			want:  "0.0.0.0:8080→80/tcp",
		},
		{
			name:  "one port on two given addresses is two",
			ports: []model.PublishedPort{publishedPort("127.0.0.1", 5432, 5432), publishedPort("192.168.1.5", 5432, 5432)},
			want:  "127.0.0.1:5432→5432/tcp, 192.168.1.5:5432→5432/tcp",
		},
		{
			name:  "a port with no address",
			ports: []model.PublishedPort{{ContainerPort: 80, Protocol: "udp", HostPort: 8080}},
			want:  "8080→80/udp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			service := model.ServiceStatus{Replicas: []model.ReplicaStatus{newRunningReplica("demo-web-1", 1, tt.ports...)}}

			// Act
			got := formatServicePorts(service)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatServiceState(t *testing.T) {
	tests := []struct {
		name    string
		service model.ServiceStatus
		want    string
	}{
		{
			name: "every replica exited 0",
			service: model.ServiceStatus{State: model.ReplicaStateExited, Replicas: []model.ReplicaStatus{
				{Number: 1, State: model.ReplicaStateExited},
				{Number: 2, State: model.ReplicaStateExited},
			}},
			want: "exited 0",
		},
		{
			name: "a failing replica after one that exited 0",
			service: model.ServiceStatus{State: model.ReplicaStateExited, Replicas: []model.ReplicaStatus{
				{Number: 1, State: model.ReplicaStateExited},
				{Number: 2, State: model.ReplicaStateExited, ExitCode: 137},
			}},
			want: "exited 137",
		},
		{
			name:    "running with a healthcheck",
			service: model.ServiceStatus{State: model.ReplicaStateRunning, Health: model.ReplicaHealthHealthy},
			want:    "healthy",
		},
		{
			name:    "running with none",
			service: model.ServiceStatus{State: model.ReplicaStateRunning},
			want:    "running",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := formatServiceState(tt.service)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
