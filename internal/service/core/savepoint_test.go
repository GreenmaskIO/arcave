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
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// savepoint create checks the name, inspects the workload and plans the capture before anything is
// held: a plan that cannot be built must leave the project running. Reversed, it compiles.
func TestService_CreateSavepoint(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	savepoints := &mockSavepoints{}
	nameCheck := savepoints.On("List", mock.Anything).
		Return([]model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000", Name: "other"}}, nil).Once()
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000", Name: "before-migration"}
	stages := newMockStages(false)
	mock.InOrder(
		nameCheck,
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{}, nil),
		stages.expectHold(nil),
		stages.expectCaptureOf(provider.CaptureRequest{
			Name:        "before-migration",
			Description: "trying a schema change",
			InResult:    true,
		}, nil),
		stages.expectCommit(committed, nil),
		stages.expectRelease(90*time.Second, nil),
	)
	svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		Name:        "before-migration",
		Description: "trying a schema change",
		WaitTimeout: 90 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	locker.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// Labels reach the capture's request as they were asked for.
func TestService_CreateSavepoint_CarriesLabels(t *testing.T) {
	// Arrange
	labels := map[string]string{"team": "payments"}
	locker := &mockLocker{}
	expectLock(locker)
	savepoints := emptySavepoints()
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000", Labels: labels}
	stages := newMockStages(false)
	stages.expectInspect(nil)
	stages.expectPlanCapture(provider.CaptureOptions{}, nil)
	stages.expectHold(nil)
	stages.expectCaptureOf(provider.CaptureRequest{Labels: labels, InResult: true}, nil)
	stages.expectCommit(committed, nil)
	stages.expectRelease(0, nil)
	svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		Labels: labels,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	locker.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// The capture runs under the session lock the workflow was granted: the one its commit is verified
// against.
func TestService_CreateSavepoint_CapturesUnderTheSessionLock(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	savepoints := emptySavepoints()
	grantedLock := &mockGrantedSessionLock{}
	grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	sessionLocker := &mockSessionLocker{}
	sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).Return(grantedLock, nil).Once()
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000"}
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{}, nil),
		stages.expectHold(nil),
		stages.expectCaptureOf(provider.CaptureRequest{InResult: true}, nil),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		stages.expectCommit(committed, nil),
		stages.expectRelease(0, nil),
	)
	svc := New(Deps{
		Store:          newDirStore(t),
		ArcaveProject:  demoArcaveProject,
		SessionLocker:  sessionLocker,
		WriteAdmission: fakeWriteAdmission{},
		Savepoints:     savepoints,
		ProjectLocker:  locker,
		Workload:       &stages.workload,
		Inflight:       &fakeInflight{},
	})

	// Act
	savepoint, err := svc.CreateSavepoint(zerolog.Nop().WithContext(context.Background()), model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	locker.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A plan that cannot be built leaves the compose project running: nothing is held or captured.
func TestService_CreateSavepoint_APlanNotBuiltHoldsNothing(t *testing.T) {
	// Arrange
	errPlan := errors.New("compose files are gone")
	locker := &mockLocker{}
	expectLock(locker)
	// Only the plan is expected: a hold would fail the test where it happened.
	savepoints := emptySavepoints()
	stages := newMockStages(false)
	stages.expectInspect(nil)
	stages.expectPlanCapture(provider.CaptureOptions{}, errPlan)
	svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.ErrorIs(t, err, errPlan)
	assert.Zero(t, savepoint)
	locker.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A name the savepoint could not carry is refused while the project still runs:
// nothing is introspected, stopped or captured.
func TestService_CreateSavepoint_RefusesItsName(t *testing.T) {
	tests := []struct {
		name     string
		reqName  string
		existing []model.Savepoint
		wantErr  error
	}{
		{name: "a slash", reqName: "feat/v1", wantErr: model.ErrInvalidSavepointName},
		{name: "a savepoint id", reqName: "019e843e-1650-7000-8000-000000000000", wantErr: model.ErrInvalidSavepointName},
		{
			name:     "a name already taken",
			reqName:  "v1",
			existing: []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000", Name: "v1"}},
			wantErr:  model.ErrSavepointNameTaken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSavepoints{}
			if tt.existing != nil {
				repo.On("List", mock.Anything).Return(tt.existing, nil).Once()
			}
			stages := newMockStages(false)
			svc := newStagedService(t, stages, repo, locker, &fakeInflight{})

			// Act
			_, err := svc.CreateSavepoint(zerolog.Nop().WithContext(context.Background()),
				model.CreateSavepointRequest{Name: tt.reqName})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			locker.AssertExpectations(t)
			repo.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

func TestRequireUniqueName(t *testing.T) {
	const id = model.SavepointID("019e843e-1650-7000-8000-000000000000")
	safety := model.Savepoint{ID: id, Name: "pre-rollback", Labels: newSafetySnapshotLabels()}

	tests := []struct {
		name     string
		reqName  string
		existing []model.Savepoint
		wantErr  error
	}{
		{name: "no name", existing: []model.Savepoint{{ID: id}}},
		{name: "a free name", reqName: "v2", existing: []model.Savepoint{{ID: id, Name: "v1"}}},
		{name: "a taken name", reqName: "v1", existing: []model.Savepoint{{ID: id, Name: "v1"}}, wantErr: model.ErrSavepointNameTaken},
		{name: "a safety snapshot's name does not count", reqName: "pre-rollback", existing: []model.Savepoint{safety}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := requireUniqueName(visibleSavepoints(tt.existing), tt.reqName)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// A savepoint's capture is planned off the inspected workload with no fact of a restore.
func TestService_planCapture(t *testing.T) {
	// Arrange
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{}, nil),
	)
	svc := newStagedService(t, stages, &mockSavepoints{}, &mockLocker{}, &fakeInflight{})

	// Act
	plan, err := svc.planCapture(zerolog.Nop().WithContext(context.Background()))

	// Assert
	require.NoError(t, err)
	assert.Same(t, &stages.plan, plan)
	stages.AssertExpectations(t)
}

// A workload that cannot be inspected — a daemon refused among the reasons — plans nothing.
func TestService_planCapture_AnInspectionFailurePlansNothing(t *testing.T) {
	// Arrange
	stages := newMockStages(false)
	stages.expectInspect(errUnsupportedDaemon)
	svc := newStagedService(t, stages, &mockSavepoints{}, &mockLocker{}, &fakeInflight{})

	// Act
	plan, err := svc.planCapture(zerolog.Nop().WithContext(context.Background()))

	// Assert
	require.ErrorIs(t, err, errUnsupportedDaemon)
	assert.Nil(t, plan)
	stages.AssertExpectations(t)
}

// savepoint create releases on every path; the hold's or capture's error outranks the release's,
// an interrupt outranks the failure it caused, and a lock lost before the commit abandons. The
// heartbeat ends done once the record is written, failed on every other end.
func TestCaptureHeld(t *testing.T) {
	errBoom := errors.New("boom")
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000", Name: "sp"}

	tests := []struct {
		name       string
		holdErr    error
		captureErr error
		verifyErr  error
		commitErr  error
		releaseErr error
		// interruptOn cancels the command's context inside that call: Hold, Capture or Commit.
		interruptOn string
		wantErr     string
		// wantLogOrder are lines the log must hold in this order; "release" is the release's.
		wantLogOrder []string
		// wantBeat is how the savepoint's heartbeat ends.
		wantBeat heartbeat.Status
	}{
		{
			name:     "hold, capture, release",
			wantBeat: heartbeat.StatusDone,
		},
		{
			name:     "a failed hold releases and captures nothing",
			holdErr:  errBoom,
			wantErr:  "boom; partial capture " + committed.ID.String() + " is retained for a later gc",
			wantBeat: heartbeat.StatusFailed,
		},
		{
			name:        "an interrupted hold releases and captures nothing",
			holdErr:     errBoom,
			interruptOn: "Hold",
			wantErr:     "interrupted: context canceled; partial capture " + committed.ID.String() + " is retained for a later gc",
			wantBeat:    heartbeat.StatusFailed,
		},
		{
			name:       "a failed capture releases",
			captureErr: errBoom,
			wantErr:    "capturing the savepoint: boom; partial capture " + committed.ID.String() + " is retained for a later gc",
			wantBeat:   heartbeat.StatusFailed,
		},
		{
			name:        "an interrupted capture releases",
			captureErr:  errBoom,
			interruptOn: "Capture",
			wantErr: "capturing the savepoint: interrupted: context canceled; partial capture " + committed.ID.String() +
				" is retained for a later gc",
			wantBeat: heartbeat.StatusFailed,
		},
		{
			name:      "a lost lock abandons the capture and releases",
			verifyErr: model.ErrSessionLockLost,
			wantErr: "capturing the savepoint: commit savepoint " + committed.ID.String() + ": " + model.ErrSessionLockLost.Error() +
				"; partial capture " + committed.ID.String() + " is retained for a later gc",
			wantBeat: heartbeat.StatusFailed,
		},
		{
			name:      "a failed commit releases",
			commitErr: errBoom,
			wantErr: "capturing the savepoint: commit savepoint " + committed.ID.String() + ": boom; partial capture " +
				committed.ID.String() + " is retained for a later gc",
			wantBeat: heartbeat.StatusFailed,
		},
		{
			name:         "an interrupt past the commit releases and fails nothing",
			interruptOn:  "Commit",
			wantLogOrder: []string{"savepoint committed before the interrupt", "release"},
			wantBeat:     heartbeat.StatusDone,
		},
		{
			name:       "a failed release past the commit names the savepoint",
			releaseErr: errBoom,
			wantErr:    "savepoint " + committed.ID.String() + " committed; bringing the project back: boom",
			wantBeat:   heartbeat.StatusDone,
		},
		{
			name:       "a failed release under a failed capture is named in the error",
			captureErr: errors.New("capture boom"),
			releaseErr: errBoom,
			wantErr: "capturing the savepoint: capture boom; partial capture " + committed.ID.String() +
				" is retained for a later gc; the services were not resumed: boom",
			wantBeat: heartbeat.StatusFailed,
		},
		{
			name:       "a failed release under a failed hold is named in the error",
			holdErr:    errors.New("stop boom"),
			releaseErr: errBoom,
			wantErr:    "stop boom; partial capture " + committed.ID.String() + " is retained for a later gc; the services were not resumed: boom",
			wantBeat:   heartbeat.StatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &syncBuffer{}
			ctx, cancel := context.WithCancel(zerolog.New(logs).WithContext(context.Background()))
			defer cancel()
			interruptIf := func(call string) func(mock.Arguments) {
				return func(mock.Arguments) {
					if tt.interruptOn == call {
						cancel()
					}
				}
			}
			req := provider.CaptureRequest{ID: committed.ID, Name: "sp", InResult: true}
			store := newDirStore(t)
			svc := New(Deps{Store: store})
			grantedLock := &mockGrantedSessionLock{}
			capturer := newMockStages(false)
			calls := []*mock.Call{capturer.expectHold(tt.holdErr).Run(interruptIf("Hold"))}
			if tt.holdErr == nil {
				calls = append(calls, capturer.expectCaptureOf(req, tt.captureErr).Run(interruptIf("Capture")))
			}
			if tt.holdErr == nil && tt.captureErr == nil {
				calls = append(calls, grantedLock.On("Verify", mock.Anything).Return(tt.verifyErr).Once())
			}
			if tt.holdErr == nil && tt.captureErr == nil && tt.verifyErr == nil {
				var savepoint model.Savepoint
				if tt.commitErr == nil {
					savepoint = committed
				}
				calls = append(calls, capturer.expectCommit(savepoint, tt.commitErr).Run(interruptIf("Commit")))
			}
			calls = append(calls, capturer.expectRelease(time.Minute, tt.releaseErr).
				Run(func(args mock.Arguments) {
					zerolog.Ctx(args.Get(0).(context.Context)).Info().Msg("release")
				}))
			mock.InOrder(calls...)

			// Act
			savepoint, err := svc.captureHeld(ctx, grantedLock, &capturer.plan, req, time.Minute)

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, committed, savepoint)
			} else {
				require.EqualError(t, err, tt.wantErr)
				assert.Zero(t, savepoint)
			}
			for _, cause := range []error{tt.holdErr, tt.captureErr, tt.verifyErr, tt.commitErr} {
				switch {
				case cause == nil:
				case tt.interruptOn == "":
					assert.ErrorIs(t, err, cause)
				default:
					// The interrupt is the error, in place of the failure it caused.
					_, ok := errors.AsType[*model.InterruptedError](err)
					assert.True(t, ok)
					assert.NotErrorIs(t, err, cause)
				}
			}
			assertLogOrder(t, logs.String(), tt.wantLogOrder)
			assert.Equal(t, tt.wantBeat, readSavepointHeartbeat(t, store, committed.ID).Status)
			capturer.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}
