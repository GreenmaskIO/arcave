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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	checkedAt  = time.Date(2026, 11, 7, 17, 14, 0, 0, time.UTC)
	deployedAt = time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC)
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
				status.State = "degraded"
				status.Detail = fakeDetail{table: model.Table{Columns: statusColumns, Rows: [][]string{
					{"db", "1/1", "unhealthy", "3h 11m", "127.0.0.1:5432→5432/tcp"},
					{"web", "2/2", "running", "3h 11m", "0.0.0.0:8080→80/tcp"},
					{"migrate", "1/1", "completed", "—", ""},
					{"worker", "0/1", "exited 1", "—", ""},
				}}}
				status.Notices = []model.Notice{
					{Code: "replica-exited", Message: "worker-1 exited with code 1 (out of memory) 3h 09m ago", Severe: true},
					{Code: "replica-unhealthy", Message: "db-1 is unhealthy: 5 failing checks", Severe: true},
					{Code: "replica-recreated", Message: "web-2 was recreated outside arcave 34m ago " +
						"(compose replaced myapp-web-2): the writable layer the rollback restored is gone; volumes are untouched"},
				}
				return status
			}(),
		},
		{
			name: "status_compose",
			status: model.ComposeProjectStatus{
				ComposeProject: "myapp",
				CheckedAt:      checkedAt,
				State:          "running",
				Session:        &baseline,
				Detail: fakeDetail{table: model.Table{Columns: statusColumns, Rows: [][]string{
					{"db", "1", "running", "3h 11m", "127.0.0.1:5432→5432/tcp"},
				}}},
			},
		},
		{
			name: "status_absent",
			status: model.ComposeProjectStatus{
				ComposeProject: "myapp",
				CheckedAt:      checkedAt,
				State:          "absent",
				Session:        &baseline,
				Detail:         fakeDetail{table: model.Table{Columns: statusColumns}},
			},
		},
		{
			name: "status_rollback_pending",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.State = model.StatusStateRollbackPending
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
				status.State = "stopped"
				status.Detail = fakeDetail{table: model.Table{Columns: statusColumns, Rows: [][]string{
					{"db", "0/1", "exited 0", "—", "127.0.0.1:5432→5432/tcp"},
				}}}
				status.Notices = []model.Notice{{
					Code:    "compose-project-stopped",
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
				status.Deployment.Savepoint.Description = ""
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
		{
			name: "status_unnamed_session",
			status: func() model.ComposeProjectStatus {
				status := newRunningStatus()
				status.Session = &model.SessionRef{ID: sessionID}
				status.Deployment.Session = model.SessionRef{ID: sessionID}
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

// statusColumns are the columns of the compose provider's table.
var statusColumns = []string{"SERVICE", "REPLICAS", "STATE", "UP", "PORTS"}

// fakeDetail is a provider's body of a status that lays out table.
type fakeDetail struct {
	table model.Table
}

func (f fakeDetail) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func (f fakeDetail) Table() model.Table { return f.table }

// newRunningStatus is a compose project a rollback deployed, every replica up but a job that
// finished.
func newRunningStatus() model.ComposeProjectStatus {
	return model.ComposeProjectStatus{
		ComposeProject: "myapp",
		CheckedAt:      checkedAt,
		State:          "running",
		Session:        &baseline,
		Deployment: &model.DeploymentStatus{
			DeployedAt: deployedAt,
			Session:    model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
			Savepoint: &model.DeployedSavepointStatus{
				DeployedSavepoint: model.DeployedSavepoint{
					ID:        savepointID,
					Name:      "before-migration",
					CreatedAt: createdAt,
				},
				Description: "the schema before 0042",
			},
			Record: model.DeploymentRecordFound,
		},
		Detail: fakeDetail{table: model.Table{Columns: statusColumns, Rows: [][]string{
			{"db", "1/1", "healthy", "3h 11m", "127.0.0.1:5432→5432/tcp"},
			{"web", "2/2", "running", "3h 11m", "0.0.0.0:8080→80/tcp"},
			{"migrate", "1/1", "completed", "—", ""},
		}}},
	}
}
