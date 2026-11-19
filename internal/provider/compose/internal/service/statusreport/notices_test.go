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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

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
		Code:    composemodel.NoticeCodeDeploymentLabelsUnreadable,
		Message: "db-2 carries unreadable deployment labels: bad time; read as deployed by none",
	}}, notices)
}

func TestNewNotRestoredNotices(t *testing.T) {
	tests := []struct {
		name       string
		notCovered *composemodel.NotCovered
		want       []model.Notice
	}{
		{name: "nothing left out"},
		{
			name: "what was excluded; a mount nothing excluded, a tmpfs, is none",
			notCovered: &composemodel.NotCovered{
				Mounts: []composemodel.SkippedVolume{
					{Service: "db", Target: "/data", Volume: "demo_cache", Reason: composemodel.SkipReasonExcluded},
					{Service: "web", Target: "/srv", RawType: string(composemodel.VolumeTypeBind), Reason: composemodel.SkipReasonExcluded},
					{Service: "web", Replica: 2, Target: "/tmp/x", Reason: composemodel.SkipReasonExcluded},
					{Service: "web", Target: "/run", Reason: composemodel.SkipReasonTmpfs},
				},
				WritableLayers: []composemodel.SkippedWritableLayer{{Service: "web", Replica: 1}},
			},
			want: []model.Notice{
				{Code: composemodel.NoticeCodeVolumeNotRestored,
					Message: "volume demo_cache was not in the savepoint: it holds what it held before the rollback"},
				{Code: composemodel.NoticeCodeVolumeNotRestored,
					Message: "web /srv was not in the savepoint: the host folder holds what it held before the rollback"},
				{Code: composemodel.NoticeCodeVolumeNotRestored, Message: "web-2 /tmp/x was not in the savepoint: it came back empty"},
				{Code: composemodel.NoticeCodeLayerNotRestored,
					Message: "web-1's writable layer was not in the savepoint: it started fresh from its image"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newNotRestoredNotices(tt.notCovered)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewReplicaNotices(t *testing.T) {
	deployment := newStatusDeployment(statusDeployed)
	finished := statusCheckedAt.Add(-4 * time.Minute)
	created := statusCheckedAt.Add(-time.Hour)
	tests := []struct {
		name    string
		replica composemodel.ReplicaStatus
		record  *composemodel.Savepoint
		// state is the compose project's; running when empty.
		state composemodel.ComposeProjectState
		want  []model.Notice
	}{
		{
			name:    "missing",
			replica: composemodel.ReplicaStatus{Number: 2, State: composemodel.ReplicaStateMissing, InSavepoint: true},
			want: []model.Notice{{Code: composemodel.NoticeCodeReplicaMissing, Severe: true,
				Message: "db-2 is missing: savepoint before-migration (019e843e-1650-7000-8000-000000000000) recorded it"}},
		},
		{
			name:    "exited 0 is no failure",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateExited, FinishedAt: &finished, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaExited, Message: "db-1 exited with code 0 4m ago"}},
		},
		{
			name:    "exited 0 with every container stopped is the stopped project's to say",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateExited, FinishedAt: &finished, InDeployment: true, InSavepoint: true},
			state:   composemodel.ComposeProjectStateStopped,
		},
		{
			name: "a failing exit with every container stopped is still a failure",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateExited, ExitCode: 1, FinishedAt: &finished,
				InDeployment: true, InSavepoint: true},
			state: composemodel.ComposeProjectStateStopped,
			want:  []model.Notice{{Code: composemodel.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 1 4m ago"}},
		},
		{
			name: "terminated by a stop it did not handle, every container stopped",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateExited, ExitCode: 143, FinishedAt: &finished,
				InDeployment: true, InSavepoint: true},
			state: composemodel.ComposeProjectStateStopped,
			want: []model.Notice{{Code: composemodel.NoticeCodeReplicaExited, Severe: true,
				Message: "db-1 exited with code 143 (terminated) 4m ago"}},
		},
		{
			name: "out of memory",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateExited, ExitCode: 137, OOMKilled: true,
				InDeployment: true, InSavepoint: true},
			want: []model.Notice{{Code: composemodel.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 137 (out of memory)"}},
		},
		{
			name:    "killed",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateExited, ExitCode: 137, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 137 (killed)"}},
		},
		{
			name:    "terminated",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateExited, ExitCode: 143, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaExited, Severe: true, Message: "db-1 exited with code 143 (terminated)"}},
		},
		{
			name:    "dead",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateDead, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaDead, Severe: true, Message: "db-1 is dead"}},
		},
		{
			name:    "removing",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateRemoving, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaRemoving, Severe: true, Message: "db-1 is being removed"}},
		},
		{
			name:    "restarting",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateRestarting, RestartCount: 4, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaRestarting, Severe: true, Message: "db-1 is restarting (4 restarts)"}},
		},
		{
			name:    "created",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateCreated, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaCreated, Severe: true, Message: "db-1 was created but never started"}},
		},
		{
			name:    "paused",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStatePaused, InDeployment: true, InSavepoint: true},
			want:    []model.Notice{{Code: composemodel.NoticeCodeReplicaPaused, Message: "db-1 is paused"}},
		},
		{
			name: "running after restarts, unhealthy",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateRunning, RestartCount: 2,
				Health: composemodel.ReplicaHealthUnhealthy, FailingChecks: 5, InDeployment: true, InSavepoint: true},
			want: []model.Notice{
				{Code: composemodel.NoticeCodeReplicaRestarted, Message: "db-1 restarted 2 times since it was created"},
				{Code: composemodel.NoticeCodeReplicaUnhealthy, Severe: true, Message: "db-1 is unhealthy: 5 failing checks"},
			},
		},
		{
			name:    "added after the rollback",
			replica: composemodel.ReplicaStatus{Number: 2, State: composemodel.ReplicaStateRunning},
			want: []model.Notice{{Code: composemodel.NoticeCodeReplicaAdded,
				Message: "db-2 is not in savepoint before-migration (019e843e-1650-7000-8000-000000000000): added after the rollback"}},
		},
		{
			name:    "recreated outside arcave, its layer in the record",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateRunning, CreatedAt: &created, InSavepoint: true},
			record:  &statusDeployed,
			want: []model.Notice{{Code: composemodel.NoticeCodeReplicaRecreated, Message: "db-1 was recreated outside arcave 1h 00m ago: " +
				"the writable layer the rollback restored is gone; volumes are untouched"}},
		},
		{
			name:    "recreated outside arcave, the record holding no layer of it",
			replica: composemodel.ReplicaStatus{Number: 2, State: composemodel.ReplicaStateRunning, CreatedAt: &created, InSavepoint: true},
			record:  &statusDeployed,
			want: []model.Notice{{Code: composemodel.NoticeCodeReplicaRecreated,
				Message: "db-2 was recreated outside arcave 1h 00m ago; volumes are untouched"}},
		},
		{
			name:    "recreated outside arcave, no record read",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateRunning, CreatedAt: &created, InSavepoint: true},
			want: []model.Notice{{Code: composemodel.NoticeCodeReplicaRecreated,
				Message: "db-1 was recreated outside arcave 1h 00m ago; volumes are untouched"}},
		},
		{
			name:    "as the rollback built it",
			replica: composemodel.ReplicaStatus{Number: 1, State: composemodel.ReplicaStateRunning, InDeployment: true, InSavepoint: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			facts := statusFacts{
				checkedAt:   statusCheckedAt,
				state:       cmp.Or(tt.state, composemodel.ComposeProjectStateRunning),
				deployments: containerDeployments{latest: &deployment},
				savepoint:   tt.record,
			}

			// Act
			notices := newReplicaNotices(facts, demoService, tt.replica)

			// Assert
			assert.Equal(t, tt.want, notices)
		})
	}
}
