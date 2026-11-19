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

package workload

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

type mockObservationReader struct{ mock.Mock }

func (m *mockObservationReader) Observe(ctx context.Context) (composemodel.ObservedComposeProject, error) {
	args := m.Called(ctx)
	return args.Get(0).(composemodel.ObservedComposeProject), args.Error(1)
}

func TestObserver_Observe_ADaemonThatCannotBeReadFailsIt(t *testing.T) {
	// Arrange
	reader := &mockObservationReader{}
	reader.On("Observe", mock.Anything).Return(composemodel.ObservedComposeProject{}, errors.New("daemon unreachable")).Once()

	// Act
	observed, err := NewObserver(reader).Observe(context.Background())

	// Assert
	require.ErrorContains(t, err, "daemon unreachable")
	assert.Nil(t, observed)
	reader.AssertExpectations(t)
}

func TestObservedWorkload(t *testing.T) {
	checkedAt := time.Date(2026, 11, 7, 17, 14, 0, 0, time.UTC)
	created := checkedAt.Add(-time.Hour)
	// The record holds db-1's writable layer.
	savepoint := composetest.DemoRecordWithImages()
	record, err := composemodel.ToSharedSavepoint(savepoint)
	require.NoError(t, err)
	foreign := record
	foreign.Provider = "another"
	deployment := composemodel.Deployment{
		SavepointDeployment: composetest.DemoDeployment(savepoint),
		ServiceReplicas:     map[string][]int{composetest.DemoService: {1, 2}},
	}
	// db-1 was recreated outside arcave: it carries no deployment, the savepoint recorded it.
	observed := composemodel.ObservedComposeProject{Services: []composemodel.ObservedService{{
		Name: composetest.DemoService,
		Replicas: []composemodel.ObservedReplica{
			{Number: 1, Container: composetest.DemoContainerName(1), ContainerID: "c1", State: composemodel.ReplicaStateRunning, CreatedAt: &created},
			{Number: 2, Container: composetest.DemoContainerName(2), ContainerID: "c2", State: composemodel.ReplicaStateRunning, Deployment: &deployment},
		},
	}}}
	tests := []struct {
		name        string
		deployed    *model.Savepoint
		wantMessage string
	}{
		{
			name:        "the record names the layer the recreation lost",
			deployed:    &record,
			wantMessage: "db-1 was recreated outside arcave 1h 00m ago: the writable layer the rollback restored is gone; volumes are untouched",
		},
		{
			name:        "another provider's record is judged as none",
			deployed:    &foreign,
			wantMessage: "db-1 was recreated outside arcave 1h 00m ago; volumes are untouched",
		},
		{
			name:        "no record read",
			wantMessage: "db-1 was recreated outside arcave 1h 00m ago; volumes are untouched",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			reader := &mockObservationReader{}
			reader.On("Observe", mock.Anything).Return(observed, nil).Once()
			handle, err := NewObserver(reader).Observe(zerolog.Nop().WithContext(context.Background()))
			require.NoError(t, err)

			// Act
			gotDeployment := handle.Deployment()
			report := handle.Report(checkedAt, tt.deployed)

			// Assert
			assert.Equal(t, &deployment.SavepointDeployment, gotDeployment, "arcave's part of the deployment alone")
			assert.Equal(t, string(composemodel.ComposeProjectStateRunning), report.State)
			assert.Contains(t, report.Notices, model.Notice{Code: composemodel.NoticeCodeReplicaRecreated, Message: tt.wantMessage})
			encoded, err := json.Marshal(report.Detail)
			require.NoError(t, err)
			var detail composemodel.StatusDetail
			require.NoError(t, json.Unmarshal(encoded, &detail), "the detail encodes the compose provider's body")
			assert.Equal(t, composemodel.DeploymentSourceMixed, detail.Source)
			assert.Equal(t, 1, detail.Replaced)
			table := report.Detail.Table()
			require.Len(t, table.Rows, 1)
			assert.Equal(t, []string{composetest.DemoService, "2/2", "running", "—", ""}, table.Rows[0])
			reader.AssertExpectations(t)
		})
	}
}
