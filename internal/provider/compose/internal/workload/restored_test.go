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
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// The reap removes the anonymous volumes the teardown orphaned whose data the restore replaced, by
// the docker name they had before it; one it cannot remove is a warning, never a failure.
func TestRestored_Reap(t *testing.T) {
	anonVol := composemodel.Volume{ObjectID: "volume-anon-app-1-scratch", Backend: composeconfig.SnapshotBackendFullCopy}
	tests := []struct {
		name      string
		removeErr error
		wantWarn  []string
	}{
		{name: "the orphan is removed"},
		{
			name:      "an orphan that cannot be removed is left with a warning",
			removeErr: errors.New("in use"),
			wantWarn:  []string{"orphaned anonymous volume 9f8e7d left behind: in use"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := composetest.DemoRecord()
			record.Detail.Stats = []composemodel.SnapshotStat{{Volume: anonVol}, {Volume: composetest.DemoVolume()}}
			remover := &composetest.MockVolumeRemover{}
			remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(tt.removeErr).Once()
			restored := restoredWorkload{
				vacant: vacantWorkload{
					service:              newComposeWorkload(Deps{VolumeRemover: remover}),
					anonymousVolumeNames: map[string]string{anonVol.ObjectID: "9f8e7d", "volume-anon-app-1-kept": "kept"},
				},
				target: preparedSavepoint{Target: composemodel.Target{Savepoint: record}},
			}
			recorder := &composetest.StepRecorder{}

			// Act
			restored.Reap(composetest.NewProgressContext(recorder))

			// Assert
			assert.Equal(t, tt.wantWarn, recorder.Warnings)
			remover.AssertExpectations(t)
		})
	}
}

// A reap of a build that restored writable layers says where they live; one that restored none says
// nothing.
func TestRestored_Reap_LogsTheRestoredLayers(t *testing.T) {
	tests := []struct {
		name     string
		restored []composemodel.WritableLayerRestoreResult
		wantLog  bool
	}{
		{name: "no layer restored"},
		{
			name:     "layers restored",
			restored: []composemodel.WritableLayerRestoreResult{composetest.DemoWritableLayerRestoreResult()},
			wantLog:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &composetest.SyncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())
			restored := restoredWorkload{
				vacant: vacantWorkload{service: newComposeWorkload(Deps{})},
				target: preparedSavepoint{Target: composemodel.Target{Savepoint: composetest.DemoRecord(), Restored: tt.restored}},
			}

			// Act
			restored.Reap(ctx)

			// Assert
			assert.Equal(t, tt.wantLog, strings.Contains(logs.String(), "the containers run on the savepoint's writable layers"))
		})
	}
}
