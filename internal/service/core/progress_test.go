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

package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// progressRecorder renders every step's last event as "<path> <status>[ error=…]", in the order
// the steps end; the tracker calls it under its own lock.
type progressRecorder struct {
	paths map[progress.StepID]string
	ended []string
}

func (r *progressRecorder) Render(evt progress.Event) {
	if evt.Open != nil {
		segment := string(evt.Open.Step.Kind)
		if id := evt.Open.Step.ID; id != "" {
			segment += "[" + id + "]"
		}
		if parent := r.paths[evt.Open.Parent]; parent != "" {
			segment = parent + "/" + segment
		}
		r.paths[evt.StepID] = segment
	}
	if !evt.Status.IsTerminal() {
		return
	}
	line := r.paths[evt.StepID] + " " + string(evt.Status)
	if evt.Error != "" {
		line += " error=" + evt.Error
	}
	r.ended = append(r.ended, line)
}

func newProgressContext() (context.Context, *progressRecorder) {
	recorder := &progressRecorder{paths: map[progress.StepID]string{}}
	ctx := zerolog.Nop().WithContext(context.Background())
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	return progress.WithTracker(ctx, progress.New(recorder, now)), recorder
}

// openStep is a mock's Run opening and ending a step of kind under the step its context carries:
// where a provider's own steps land.
func openStep(kind progress.Kind) func(mock.Arguments) {
	return func(args mock.Arguments) {
		_, step := progress.Run(args.Get(0).(context.Context), progress.Step{Kind: kind})
		step.End(nil)
	}
}

// Every stage of savepoint create runs under the create's own step.
func TestService_CreateSavepointProgress(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	locker := &mockLocker{}
	expectLock(locker)
	savepoints := emptySavepoints()
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil).Run(openStep("stage.inspect")),
		stages.expectPlanCapture(provider.CaptureOptions{}, nil).Run(openStep("stage.plan")),
		stages.expectHold(nil).Run(openStep("stage.hold")),
		stages.expectCaptureOf(provider.CaptureRequest{InResult: true}, nil).Run(openStep("stage.capture")),
		stages.expectCommit(demoRecord(), nil).Run(openStep("stage.commit")),
		stages.expectRelease(0, nil).Run(openStep("stage.release")),
	)
	svc := newStagedService(stages, savepoints, locker, &fakeInflight{})

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"savepoint.create/stage.inspect succeeded",
		"savepoint.create/stage.plan succeeded",
		"savepoint.create/stage.hold succeeded",
		"savepoint.create/stage.capture succeeded",
		"savepoint.create/stage.commit succeeded",
		"savepoint.create/stage.release succeeded",
		"savepoint.create succeeded",
	}, recorder.ended)
	locker.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// Every stage of a live rollback runs under the rollback's step that frames it: the plan and the
// prepare under rollback.prepare, the safety snapshot's under rollback.safety-snapshot, the
// teardown and the build under rollback.apply.
func TestService_Rollback_Progress(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	record := demoRecord()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil).Run(openStep("stage.inspect")),
		stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil).Run(openStep("stage.plan")),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil).Run(openStep("stage.prepare")),
		stages.expectHold(nil).Run(openStep("stage.hold")),
		stages.expectCaptureOf(newSafetySnapshotRequest(record), nil).Run(openStep("stage.capture")),
		stages.expectCommit(demoSafetySnapshot(), nil).Run(openStep("stage.commit")),
		stages.expectHeldTeardown(&stages.target, nil).Run(openStep("stage.teardown")),
		stages.target.expectBuild(nil).Run(openStep("stage.build")),
		stages.target.expectRestoreData(nil).Run(openStep("stage.restore")),
		stages.target.expectStart(60*time.Second, nil).Run(openStep("stage.start")),
		// rollback.cleanup is an activity: a provider warns on it and opens no step under it.
		stages.target.expectReap(),
	)
	svc := newStagedService(stages, savepoints, locker, &fakeInflight{})

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID(record.ID),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"rollback/stage.inspect succeeded",
		"rollback/rollback.prepare/stage.plan succeeded",
		"rollback/rollback.prepare/stage.prepare succeeded",
		"rollback/rollback.prepare succeeded",
		"rollback/rollback.safety-snapshot/stage.hold succeeded",
		"rollback/rollback.safety-snapshot/stage.capture succeeded",
		"rollback/rollback.safety-snapshot/stage.commit succeeded",
		"rollback/rollback.safety-snapshot succeeded",
		"rollback/rollback.apply/stage.teardown succeeded",
		"rollback/rollback.apply/stage.build succeeded",
		"rollback/rollback.apply/stage.restore succeeded",
		"rollback/rollback.apply/stage.start succeeded",
		"rollback/rollback.apply succeeded",
		"rollback/rollback.cleanup succeeded",
		"rollback succeeded",
	}, recorder.ended)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A remove reports the savepoints it drops and no volume: it reaches no backend.
func TestService_RemoveSavepoints_Progress(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	savepoints := &mockSavepoints{}
	savepoints.On("List", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	savepoints.On("Remove", mock.Anything, record.ID).Return(nil).Once()
	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		SessionLocker:  fakeSessionLocker{},
		WriteAdmission: fakeWriteAdmission{},
		Savepoints:     savepoints,
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"savepoint.remove/savepoint.drop/savepoint[" + record.ID.String() + "] succeeded",
		"savepoint.remove/savepoint.drop succeeded",
		"savepoint.remove succeeded",
	}, recorder.ended)
	locker.AssertExpectations(t)
	savepoints.AssertExpectations(t)
}

// A lost lock cancels the work under it, and the root, opened outside the lock, carries why.
func TestWithSessionLock_LostLockCancelsTheWork(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	locker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{lostCause: model.ErrSessionLockLost}
	locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).Return(grantedLock, nil).Once()
	grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	ctx, root := progress.Begin(ctx, progress.Step{Kind: kindSavepointCreate})

	// Act
	_, err := withSessionLock(ctx, locker, model.SessionLockOperationSavepointCreate,
		func(ctx context.Context, _ model.GrantedSessionLock) (string, error) {
			_, step := progress.Run(ctx, progress.Step{Kind: "stage.teardown"})
			err := preferInterrupt(ctx, errWorkflow)
			step.End(err)
			return "", err
		})
	root.End(err)

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	require.Len(t, recorder.ended, 2)
	assert.Equal(t, "savepoint.create/stage.teardown cancelled", recorder.ended[0])
	assert.True(t, strings.HasPrefix(recorder.ended[1], "savepoint.create failed error=interrupted: "),
		"the cause is on the root: %s", recorder.ended[1])
	assert.Contains(t, recorder.ended[1], model.ErrSessionLockLost.Error())
	locker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// The lock's step is shown only once the locker waits on another holder.
func TestWithSessionLock_ShowsTheWaitOnlyWhenHeld(t *testing.T) {
	tests := []struct {
		name string
		held bool
		want []string
	}{
		{
			name: "a free lock shows nothing, so the lazy root never opens",
		},
		{
			name: "a held lock shows the wait",
			held: true,
			want: []string{"savepoint.create/session.lock succeeded", "savepoint.create succeeded"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, recorder := newProgressContext()
			locker := &mockSessionLocker{}
			grantedLock := &mockGrantedSessionLock{}
			locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).
				Run(func(args mock.Arguments) {
					if tt.held {
						progress.Current(args.Get(0).(context.Context)).Warn("waiting: held by rollback")
					}
				}).
				Return(grantedLock, nil).Once()
			grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
			ctx, root := progress.Begin(ctx, progress.Step{Kind: kindSavepointCreate})

			// Act
			_, err := withSessionLock(ctx, locker, model.SessionLockOperationSavepointCreate,
				func(context.Context, model.GrantedSessionLock) (string, error) { return "", nil })
			root.End(err)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, recorder.ended)
			locker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}
