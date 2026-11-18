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

package gc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// endedRecorder renders every step's last event as "<kind>[<id>] <status>".
type endedRecorder struct {
	steps map[progress.StepID]progress.Step
	ended []string
}

func (r *endedRecorder) Render(evt progress.Event) {
	if evt.Open != nil {
		r.steps[evt.StepID] = evt.Open.Step
	}
	if !evt.Status.IsTerminal() {
		return
	}
	step := r.steps[evt.StepID]
	r.ended = append(r.ended, fmt.Sprintf("%s[%s] %s", step.Kind, step.ID, evt.Status))
}

// A run that deletes reports each retired safety snapshot in a savepoint.drop batch under the
// deletion; the lazy gc.find and gc.retain show nothing, since these mocks report nothing.
func TestGarbageCollector_GarbageCollect_Progress(t *testing.T) {
	// Arrange
	var (
		oldest = model.SavepointID("019e843e-1650-7000-8000-00000000000a")
		newest = model.SavepointID("019e843e-1650-7000-8000-00000000000c")
		safety = map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}
		trees  = newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
	)
	trees.On("Delete", mock.Anything).Return(newDeletedOps(deadTreeOp)).Once()
	demoSavepoints := &mockSavepoints{}
	demoSavepoints.On("List", mock.Anything).Return([]model.Savepoint{
		{ID: oldest, Labels: safety}, {ID: newest, Labels: safety},
	}, nil).Once()
	demoSavepoints.On("Remove", mock.Anything, oldest).Return(nil).Once()
	session := newFindingSession(nil, trees)
	session.On("Workloads", mock.Anything).Return([]provider.WorkloadName{"demo"}, nil).Once()
	session.On("Savepoints", provider.WorkloadName("demo")).Return(demoSavepoints, nil).Once()
	recorder := &endedRecorder{steps: map[progress.StepID]progress.Step{}}
	ctx := progress.WithTracker(context.Background(), progress.New(recorder, func() time.Time {
		return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	}))
	svc := New(newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t)))

	// Act
	_, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"savepoint[" + oldest.String() + "] succeeded",
		"savepoint.drop[] succeeded",
		"gc.erase[] succeeded",
		"gc[] succeeded",
	}, recorder.ended)
	trees.AssertExpectations(t)
	session.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
}
