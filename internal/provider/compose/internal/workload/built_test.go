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
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// Every line a volume's restore logs is attributed to the volume, its backend and its savepoint.
func TestBuilt_RestoreData_LogsAreAttributedPerVolume(t *testing.T) {
	// Arrange
	record := composetest.DemoRecord()
	backend := composetest.NewMockBackend(composetest.DemoVolume())
	backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
	built := builtWorkload{
		vacant: vacantWorkload{service: newComposeWorkload(Deps{})},
		target: preparedSavepoint{Target: composemodel.Target{Savepoint: record, Backends: []composemodel.VolumeSnapshotBackend{backend}}},
	}
	logs := &composetest.SyncBuffer{}
	ctx := zerolog.New(logs).Level(zerolog.DebugLevel).WithContext(context.Background())

	// Act
	_, err := built.RestoreData(ctx)

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(), `"volume":"volume-named-demo_db"`)
	assert.Contains(t, logs.String(), `"backend":"fullcopy"`)
	assert.Contains(t, logs.String(), `"snapshot_id":"`+record.Header.ID.String()+`"`)
	assert.Contains(t, logs.String(), `"op":"restore"`)
	backend.AssertExpectations(t)
}

// A volume whose restore fails fails the stage, and no restored project is handed back.
func TestBuilt_RestoreData_FailedRestore(t *testing.T) {
	// Arrange
	record := composetest.DemoRecord()
	backend := composetest.NewMockBackend(composetest.DemoVolume())
	backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(errors.New("boom")).Once()
	building := builtWorkload{
		vacant: vacantWorkload{service: newComposeWorkload(Deps{})},
		target: preparedSavepoint{Target: composemodel.Target{Savepoint: record, Backends: []composemodel.VolumeSnapshotBackend{backend}}},
	}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	handle, err := building.RestoreData(ctx)

	// Assert
	require.EqualError(t, err, "restore volume-named-demo_db: restore capture: boom")
	assert.Nil(t, handle)
	backend.AssertExpectations(t)
}
