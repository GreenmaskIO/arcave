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
	"github.com/greenmaskio/arcave/pkg/progress"
)

// newStagedService is a Service walking stages, its savepoints listed by savepoints, the
// session's lock always held.
func newStagedService(
	stages *mockStages, savepoints *mockSavepoints, locker *mockLocker, inflight inflightMarkers,
) *Service {
	return New(Deps{
		Savepoints:     savepoints,
		ProjectLocker:  locker,
		WriteAdmission: fakeWriteAdmission{},
		SessionLocker:  fakeSessionLocker{},
		Workload:       &stages.workload,
		Inflight:       inflight,
		ArcaveProject:  demoArcaveProject,
		ComposeProject: demoProject,
	})
}

// isInflightMarker matches a marker naming no safety snapshot.
func isInflightMarker(target model.SavepointID, phase model.InflightPhase) any {
	return isInflightMarkerOf(target, phase, "")
}

// isInflightMarkerOf matches a marker of the default session and demoProject naming safety as its
// safety snapshot.
func isInflightMarkerOf(target model.SavepointID, phase model.InflightPhase, safety model.SavepointID) any {
	return mock.MatchedBy(func(marker model.InflightMarker) bool {
		return marker.Target == target && marker.Phase == phase && marker.Safety == safety &&
			marker.Session == model.DefaultSession && marker.ComposeProject == demoProject
	})
}

// newRecoveredMarker is the marker a rollback to id recovers from: naming id as its safety snapshot
// under toSafety, as its target otherwise.
func newRecoveredMarker(id model.SavepointID, phase model.InflightPhase, toSafety bool) model.InflightMarker {
	marker := model.InflightMarker{
		Phase:          phase,
		Session:        model.DefaultSession,
		ComposeProject: demoProject,
		Target:         id,
		At:             time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
	}
	if toSafety {
		marker.Target = demoSafetySnapshot().ID
		marker.Safety = id
	}
	return marker
}

// warnRecorder keeps every warning the result repeats.
type warnRecorder struct {
	inResult []string
}

func (r *warnRecorder) Render(evt progress.Event) {
	if evt.Status == progress.StatusWarned && evt.InResult {
		r.inResult = append(r.inResult, evt.Msg)
	}
}

// A live rollback walks the stages in order, the marker phased between them; the safety snapshot
// stays, and the result is the summary of what the build restored.
func TestService_Rollback_RestoresAndKeepsTheSafetySnapshot(t *testing.T) {
	// Arrange
	record := demoRecord()
	safetySnapshot := demoSafetySnapshot()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	summary := provider.PreparedSummary{
		Volumes:        []model.Volume{demoVolume()},
		WritableLayers: []model.WritableLayerRestoreResult{{Service: "db", Replica: 1, Tag: "arcave/demo-db-1:sp"}},
	}
	stages.target.prepared.summary = summary
	inflight := &mockInflight{}
	mock.InOrder(
		inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
		stages.expectHold(nil),
		stages.expectCaptureOf(newSafetySnapshotRequest(record), nil),
		stages.expectCommit(safetySnapshot, nil),
		inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRecreating, safetySnapshot.ID)).Return(nil).Once(),
		stages.expectHeldTeardown(&stages.target, nil),
		stages.target.expectBuild(nil),
		inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, safetySnapshot.ID)).Return(nil).Once(),
		stages.target.expectRestoreData(nil),
		inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoreDone, safetySnapshot.ID)).Return(nil).Once(),
		stages.target.expectStart(60*time.Second, nil),
		stages.target.expectReap(),
		inflight.On("Clear", mock.Anything).Return(nil).Once(),
	)
	svc := newStagedService(stages, savepoints, locker, inflight)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.RollbackResult{
		RolledBackTo:   record,
		Restored:       summary.Volumes,
		WritableLayers: summary.WritableLayers,
		SafetySnapshot: safetySnapshot.ID,
	}, res)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	inflight.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A savepoint that does not exist is refused before any lock is taken: the mocked lockers carry
// no expectations, so a Lock or an Acquire would panic.
func TestService_Rollback_RefusesAMissingSavepointBeforeTheLocks(t *testing.T) {
	tests := []struct {
		name string
		req  model.RollbackRequest
	}{
		{name: "by id", req: model.RollbackRequest{ID: ptrID("019e843e-1650-7000-8000-00000000ffff")}},
		{name: "by name", req: model.RollbackRequest{Name: ptrString("nope")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			savepoints := &mockSavepoints{}
			savepoints.On("List", mock.Anything).Return([]model.Savepoint{demoRecord()}, nil).Once()
			projectLocker, sessionLocker := &mockLocker{}, &mockSessionLocker{}
			svc := New(Deps{
				ArcaveProject: demoArcaveProject,
				Savepoints:    savepoints,
				ProjectLocker: projectLocker,
				SessionLocker: sessionLocker,
			})

			// Act
			_, err := svc.Rollback(context.Background(), tt.req)

			// Assert
			require.ErrorIs(t, err, errSavepointNotFound)
			savepoints.AssertExpectations(t)
			projectLocker.AssertExpectations(t)
			sessionLocker.AssertExpectations(t)
		})
	}
}

func TestService_Rollback_RefusesNoArcaveProject(t *testing.T) {
	// Arrange
	locker := &mockSessionLocker{}
	svc := New(Deps{SessionLocker: locker})

	// Act
	_, err := svc.Rollback(context.Background(), model.RollbackRequest{
		ID: ptrID("019e843e-1650-7000-8000-000000000000"),
	})

	// Assert
	require.ErrorContains(t, err, "invalid arcave project name")
	locker.AssertExpectations(t)
}

// The id gate stands ahead of the whole workflow: an id that could name a parent directory reaches
// no stage. The stages carry no expectation, so an inspection fails the test loudly.
func TestService_Rollback_RejectsAnUnsafeIDBeforeTheWorkload(t *testing.T) {
	// Arrange
	record := demoRecord()
	record.ID = "../../etc"
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	svc := newStagedService(stages, savepoints, locker, &fakeInflight{})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID("../../etc"), WaitTimeout: time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSavepointID)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A workload that cannot be inspected — a daemon refused among the reasons — fails the rollback
// before anything is planned or prepared.
func TestService_Rollback_AnInspectionFailureTouchesNothing(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	stages.expectInspect(model.ErrUnsupportedDaemon)
	svc := newStagedService(stages, savepoints, locker, &fakeInflight{})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrUnsupportedDaemon)
	assert.ErrorContains(t, err, "prepare: ")
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A failure past the marker reverts: the safety snapshot is prepared, torn down onto and built as
// the target was, the marker naming the target throughout; the rollback's own failure is the error.
func TestService_Rollback_RevertsPastTheMarker(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name        string
		teardownErr error
		buildErr    error
		restoreErr  error
		startErr    error
		wantErr     string
	}{
		{name: "a teardown that fails", teardownErr: errBoom, wantErr: "rollback failed: boom"},
		{name: "a build that fails", buildErr: errBoom, wantErr: "rollback failed: boom"},
		{name: "a restore that fails", restoreErr: errBoom, wantErr: "rollback failed: boom"},
		{
			name:     "services that do not become healthy",
			startErr: errBoom,
			wantErr:  "rollback failed: services did not become healthy after restore: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			safetySnapshot := demoSafetySnapshot()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(false)
			inflight := &mockInflight{}
			calls := []*mock.Call{
				inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
				stages.expectInspect(nil),
				stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
				stages.expectHold(nil),
				stages.expectCaptureOf(newSafetySnapshotRequest(record), nil),
				stages.expectCommit(safetySnapshot, nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRecreating, safetySnapshot.ID)).Return(nil).Once(),
				stages.expectHeldTeardown(&stages.target, tt.teardownErr),
			}
			if tt.teardownErr == nil {
				calls = append(calls, stages.target.expectBuild(tt.buildErr))
			}
			if tt.teardownErr == nil && tt.buildErr == nil {
				calls = append(calls,
					inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, safetySnapshot.ID)).Return(nil).Once(),
					stages.target.expectRestoreData(tt.restoreErr))
			}
			if tt.startErr != nil {
				calls = append(calls,
					inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoreDone, safetySnapshot.ID)).Return(nil).Once(),
					stages.target.expectStart(60*time.Second, tt.startErr))
			}
			calls = append(calls,
				stages.expectHeldPrepare(safetySnapshot.ID, provider.PrepareOptions{Deployment: newDeploymentOf(safetySnapshot)}, nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRecreating, safetySnapshot.ID)).Return(nil).Once(),
				stages.expectHeldTeardown(&stages.revert, nil),
				stages.revert.expectBuild(nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, safetySnapshot.ID)).Return(nil).Once(),
				stages.revert.expectRestoreData(nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoreDone, safetySnapshot.ID)).Return(nil).Once(),
				stages.revert.expectStart(60*time.Second, nil),
				stages.revert.expectReap(),
				inflight.On("Clear", mock.Anything).Return(nil).Once(),
			)
			mock.InOrder(calls...)
			svc := newStagedService(stages, savepoints, locker, inflight)
			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.EqualError(t, err, tt.wantErr)
			assert.ErrorIs(t, err, errBoom)
			assert.Contains(t, logs.String(), "reverted to the pre-rollback state")
			inflight.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// A revert that fails leaves the marker inconsistent and the safety snapshot named as the way
// back; nothing is reaped and the marker is not cleared.
func TestService_Rollback_FailedRevertRetainsTheSafetySnapshot(t *testing.T) {
	// Arrange
	errBoom := errors.New("restore boom")
	record := demoRecord()
	safetySnapshot := demoSafetySnapshot()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	inflight := &mockInflight{}
	mock.InOrder(
		inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
		stages.expectHold(nil),
		stages.expectCaptureOf(newSafetySnapshotRequest(record), nil),
		stages.expectCommit(safetySnapshot, nil),
		inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRecreating, safetySnapshot.ID)).Return(nil).Once(),
		stages.expectHeldTeardown(&stages.target, nil),
		stages.target.expectBuild(nil),
		inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, safetySnapshot.ID)).Return(nil).Once(),
		stages.target.expectRestoreData(errBoom),
		stages.expectHeldPrepare(safetySnapshot.ID, provider.PrepareOptions{Deployment: newDeploymentOf(safetySnapshot)}, nil),
		inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRecreating, safetySnapshot.ID)).Return(nil).Once(),
		stages.expectHeldTeardown(&stages.revert, nil),
		stages.revert.expectBuild(errors.New("create boom")),
		inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightInconsistent, safetySnapshot.ID)).Return(nil).Once(),
	)
	svc := newStagedService(stages, savepoints, locker, inflight)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, logs.String(), "revert failed: environment left in an inconsistent state, safety snapshot retained")
	assert.Contains(t, logs.String(), safetySnapshot.ID.String())
	inflight.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A revert that cannot prepare the safety snapshot fails as its writable layers' restore, the
// marker left inconsistent.
func TestService_Rollback_RevertThatCannotPrepare(t *testing.T) {
	// Arrange
	record := demoRecord()
	safetySnapshot := demoSafetySnapshot()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
		stages.expectHold(nil),
		stages.expectCaptureOf(newSafetySnapshotRequest(record), nil),
		stages.expectCommit(safetySnapshot, nil),
		stages.expectHeldTeardown(&stages.target, errors.New("down boom")),
		stages.expectHeldPrepare(safetySnapshot.ID, provider.PrepareOptions{Deployment: newDeploymentOf(safetySnapshot)},
			errors.New("load boom")),
	)
	inflight := &fakeInflight{}
	svc := newStagedService(stages, savepoints, locker, inflight)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.EqualError(t, err, "rollback failed: down boom")
	assert.Contains(t, logs.String(), "prepare the safety snapshot: load boom")
	marker, found, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	require.True(t, found)
	assert.Equal(t, model.InflightInconsistent, marker.Phase)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// The revert runs past the interrupt that aborted the rollback: its stages see a context no
// interrupt cancelled.
func TestService_Rollback_InterruptedRestoreReverts(t *testing.T) {
	// Arrange
	record := demoRecord()
	safetySnapshot := demoSafetySnapshot()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	live := mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil })
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
		stages.expectHold(nil),
		stages.expectCaptureOf(newSafetySnapshotRequest(record), nil),
		stages.expectCommit(safetySnapshot, nil),
		stages.expectHeldTeardown(&stages.target, nil),
		stages.target.expectBuild(nil),
		stages.target.built.On("RestoreData", mock.Anything).
			Run(func(mock.Arguments) { cancel() }).
			Return(nil, context.Canceled).Once(),
		stages.held.On("PrepareSavepoint", live, safetySnapshot.ID,
			provider.PrepareOptions{Deployment: newDeploymentOf(safetySnapshot)}).Return(&stages.revert.prepared, nil).Once(),
		stages.held.On("Teardown", live).Return(&stages.revert.vacant, nil).Once(),
		stages.revert.vacant.On("Build", live, &stages.revert.prepared).Return(&stages.revert.built, nil).Once(),
		stages.revert.built.On("RestoreData", live).Return(&stages.revert.restored, nil).Once(),
		stages.revert.restored.On("Start", live, 60*time.Second).Return(nil).Once(),
		stages.revert.restored.On("Reap", live).Once(),
	)
	svc := newStagedService(stages, savepoints, locker, &fakeInflight{})

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "rollback failed: interrupted")
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// The safety snapshot is taken before the savepoint the selector resolved to, by its id and name,
// never by the ref typed: "latest" names no savepoint.
func TestService_Rollback_SafetySnapshotNamesItsRollback(t *testing.T) {
	tests := []struct {
		name string
		req  model.RollbackRequest
		// lists is how often the savepoints are listed: a name is resolved ahead of the locks too.
		lists int
	}{
		{name: "by name", req: model.RollbackRequest{Name: ptrString("sp")}, lists: 2},
		{name: "the latest", req: model.RollbackRequest{Latest: ptrBool(true)}, lists: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints := &mockSavepoints{}
			savepoints.On("List", mock.Anything).Return([]model.Savepoint{record}, nil).Times(tt.lists)
			locker := &mockLocker{}
			expectLock(locker)
			stages := newMockStages(false)
			// A capture that fails once it has the request keeps the run to the request.
			mock.InOrder(
				stages.expectInspect(nil),
				stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
				stages.expectHold(nil),
				stages.expectCaptureOf(provider.CaptureRequest{
					Labels:      safetySnapshotLabels(),
					TakenBefore: &model.SavepointRef{ID: record.ID, Name: "sp"},
				}, errors.New("boom")),
				stages.expectRelease(60*time.Second, nil),
			)
			svc := newStagedService(stages, savepoints, locker, &fakeInflight{})
			req := tt.req
			req.WaitTimeout = 60 * time.Second

			// Act
			_, err := svc.Rollback(zerolog.Nop().WithContext(context.Background()), req)

			// Assert
			require.ErrorContains(t, err, "pre-rollback safety snapshot failed, nothing changed: boom")
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// The safety snapshot's phase: the hold, then the capture of what it holds, recorded as taken
// before savepoint and not the command's result, then its commit once the session's lock is
// checked. A failure in any is the caller's to release; a lost lock abandons the capture.
func TestTakeSafetySnapshot(t *testing.T) {
	boom := errors.New("boom")
	unnamed := demoRecord()
	unnamed.Name = ""
	tests := []struct {
		name       string
		savepoint  model.Savepoint
		holdErr    error
		captureErr error
		verifyErr  error
		wantErr    string
	}{
		{name: "a failed hold captures nothing", savepoint: demoRecord(), holdErr: boom, wantErr: "stop services: boom"},
		{
			name:       "a failed capture",
			savepoint:  demoRecord(),
			captureErr: boom,
			wantErr:    "pre-rollback safety snapshot failed, nothing changed: boom",
		},
		{
			name:      "a lost lock commits nothing",
			savepoint: demoRecord(),
			verifyErr: model.ErrSessionLockLost,
			wantErr: "pre-rollback safety snapshot failed, nothing changed: commit savepoint " +
				demoSafetySnapshot().ID.String() + ": " + model.ErrSessionLockLost.Error(),
		},
		{name: "a named savepoint", savepoint: demoRecord()},
		{name: "an unnamed savepoint", savepoint: unnamed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var want model.Savepoint
			stages := newMockStages(false)
			grantedLock := &mockGrantedSessionLock{}
			calls := []*mock.Call{stages.expectHold(tt.holdErr)}
			if tt.holdErr == nil {
				calls = append(calls, stages.expectCaptureOf(provider.CaptureRequest{
					Labels:      safetySnapshotLabels(),
					TakenBefore: &model.SavepointRef{ID: tt.savepoint.ID, Name: tt.savepoint.Name},
				}, tt.captureErr))
			}
			if tt.holdErr == nil && tt.captureErr == nil {
				calls = append(calls, grantedLock.On("Verify", mock.Anything).Return(tt.verifyErr).Once())
			}
			switch {
			case tt.verifyErr != nil:
				named, abandon := stages.expectAbandon(demoSafetySnapshot().ID, tt.verifyErr)
				calls = append(calls, named, abandon)
			case tt.holdErr == nil && tt.captureErr == nil:
				want = demoSafetySnapshot()
				calls = append(calls, stages.expectCommit(want, nil))
			}
			mock.InOrder(calls...)

			// Act
			held, got, err := takeSafetySnapshot(context.Background(), grantedLock, tt.savepoint, &stages.plan)

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Zero(t, got)
				assert.Nil(t, held)
			} else {
				require.NoError(t, err)
				assert.Equal(t, want, got)
				assert.Same(t, &stages.held, held)
			}
			stages.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}

// Every way a live rollback stops before its marker releases what the safety snapshot held, an
// interrupt's included; past the marker the revert owns the project and nothing is released.
func TestService_Rollback_ReleasesBeforeTheMarker(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		holdErr error
		// interruptOn cancels the run during the hold or the commit.
		interruptOn string
		captureErr  error
		putErr      error
		teardownErr error
		wantErr     string
		wantRelease bool
		// wantKept is a safety snapshot committed and then not used, which the log names.
		wantKept bool
	}{
		{name: "a failed stop", holdErr: boom, wantErr: "stop services: boom", wantRelease: true},
		{
			name:        "a stop the interrupt cut short",
			holdErr:     context.Canceled,
			interruptOn: "Hold",
			wantErr:     "stop services: interrupted: context canceled",
			wantRelease: true,
		},
		{
			name:        "a failed safety snapshot",
			captureErr:  boom,
			wantErr:     "pre-rollback safety snapshot failed, nothing changed: boom",
			wantRelease: true,
		},
		{
			name:        "an interrupt right after the safety snapshot",
			interruptOn: "Commit",
			wantErr:     "rollback interrupted, nothing changed: context canceled",
			wantRelease: true,
			wantKept:    true,
		},
		{
			name:        "a marker that cannot be written",
			putErr:      errors.New("disk full"),
			wantErr:     "recording the inflight marker, nothing changed: disk full",
			wantRelease: true,
			wantKept:    true,
		},
		{name: "a failure past the marker", teardownErr: boom, wantErr: "rollback failed: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &syncBuffer{}
			ctx, cancel := context.WithCancel(zerolog.New(logs).WithContext(context.Background()))
			defer cancel()
			record := demoRecord()
			safetySnapshot := demoSafetySnapshot()
			savepoints, locker := demoResolution(record)
			interrupt := func(mock.Arguments) { cancel() }
			stages := newMockStages(false)
			inflight := &mockInflight{}
			gate := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
			hold := stages.expectHold(tt.holdErr)
			if tt.interruptOn == "Hold" {
				hold.Run(interrupt)
			}
			calls := []*mock.Call{
				gate,
				stages.expectInspect(nil),
				stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
				hold,
			}
			if tt.holdErr == nil {
				calls = append(calls, stages.expectCaptureOf(newSafetySnapshotRequest(record), tt.captureErr))
			}
			if tt.holdErr == nil && tt.captureErr == nil {
				commit := stages.expectCommit(safetySnapshot, nil)
				if tt.interruptOn == "Commit" {
					commit.Run(interrupt)
				}
				calls = append(calls, commit)
			}
			reachesMarker := tt.holdErr == nil && tt.captureErr == nil && tt.interruptOn == ""
			if reachesMarker {
				calls = append(calls, inflight.On("Put", mock.Anything,
					isInflightMarkerOf(record.ID, model.InflightRecreating, safetySnapshot.ID)).Return(tt.putErr).Once())
			}
			if reachesMarker && tt.putErr == nil {
				// The revert's own teardown fails too, which leaves the marker inconsistent.
				calls = append(calls,
					stages.expectHeldTeardown(&stages.target, tt.teardownErr),
					stages.expectHeldPrepare(safetySnapshot.ID, provider.PrepareOptions{Deployment: newDeploymentOf(safetySnapshot)}, nil),
					inflight.On("Put", mock.Anything,
						isInflightMarkerOf(record.ID, model.InflightRecreating, safetySnapshot.ID)).Return(nil).Once(),
					stages.expectHeldTeardown(&stages.revert, tt.teardownErr),
					inflight.On("Put", mock.Anything,
						isInflightMarkerOf(record.ID, model.InflightInconsistent, safetySnapshot.ID)).Return(nil).Once(),
				)
			}
			if tt.wantRelease {
				calls = append(calls, stages.expectRelease(60*time.Second, nil))
			}
			mock.InOrder(calls...)
			svc := newStagedService(stages, savepoints, locker, inflight)

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.ErrorContains(t, err, tt.wantErr)
			if tt.wantKept {
				assert.Contains(t, logs.String(), safetySnapshot.ID.String(), "the snapshot it stopped short of using is named")
			}
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
			inflight.AssertExpectations(t)
		})
	}
}

// A safety snapshot whose plan cannot be built, or a target that cannot be prepared, stops the
// rollback before anything is held: the plan's two refusals keep their own words.
func TestService_Rollback_APrepareFailureHoldsNothing(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name       string
		planErr    error
		prepareErr error
		wantErr    string
	}{
		{
			name:    "a policy that cannot be resolved",
			planErr: &provider.CapturePolicyError{Err: errBoom},
			wantErr: "resolve the safety snapshot's capture policy: boom",
		},
		{name: "a plan that cannot be built", planErr: errBoom, wantErr: "plan the safety snapshot: boom"},
		{name: "a target that cannot be prepared", prepareErr: errBoom, wantErr: "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(false)
			calls := []*mock.Call{
				stages.expectInspect(nil),
				stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, tt.planErr),
			}
			if tt.planErr == nil {
				calls = append(calls, stages.expectPrepare(record.ID, demoPrepareOptions(record, false), tt.prepareErr))
			}
			mock.InOrder(calls...)
			svc := newStagedService(stages, savepoints, locker, &fakeInflight{})

			// Act
			_, err := svc.Rollback(zerolog.Nop().WithContext(context.Background()),
				model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: time.Second})

			// Assert
			require.EqualError(t, err, tt.wantErr)
			assert.ErrorIs(t, err, errBoom)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// Labels the safety snapshot's plan ignored are logged as the safety snapshot's.
func TestService_planSafetySnapshot(t *testing.T) {
	// Arrange
	record := demoRecord()
	stages := newMockStages(false)
	stages.plan.labelsIgnored = errors.New("unknown label arcave.captur")
	stages.expectPlanCapture(provider.CaptureOptions{Restoring: record.ID}, nil)
	svc := newStagedService(stages, &mockSavepoints{}, &mockLocker{}, &fakeInflight{})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	plan, err := svc.planSafetySnapshot(ctx, &stages.live, record)

	// Assert
	require.NoError(t, err)
	assert.Same(t, &stages.plan, plan)
	assert.Contains(t, logs.String(),
		`"safety_snapshot":true,"error":"unknown label arcave.captur","message":"the safety snapshot ignores the labels and follows the config"`)
	stages.AssertExpectations(t)
}

// A request skipping the safety snapshot, or a recovery, plans and holds nothing: it warns, writes
// its marker — restoring on a recovery — tears the live workload down and builds the target.
func TestService_Rollback_WithoutSafetySnapshot(t *testing.T) {
	recovered := newRecoveredMarker(demoRecord().ID, model.InflightRestoring, true)
	inconsistent := newRecoveredMarker(demoRecord().ID, model.InflightInconsistent, true)
	rerunRecreating := newRecoveredMarker(demoRecord().ID, model.InflightRecreating, false)
	rerunDone := newRecoveredMarker(demoRecord().ID, model.InflightRestoreDone, false)
	another := newRecoveredMarker("019e83ef-ee80-7000-8000-000000000000", model.InflightRestoring, false)
	tests := []struct {
		name       string
		standing   *model.InflightMarker
		skip       bool
		wantPhase  model.InflightPhase
		wantSafety model.SavepointID
	}{
		{name: "skipped by the request", skip: true, wantPhase: model.InflightRecreating},
		{
			name:       "a recovery to the safety snapshot",
			standing:   &recovered,
			wantPhase:  model.InflightRestoring,
			wantSafety: demoRecord().ID,
		},
		{
			name:       "a recovery to the safety snapshot past a failed revert",
			standing:   &inconsistent,
			wantPhase:  model.InflightRestoring,
			wantSafety: demoRecord().ID,
		},
		{name: "a recovery of the same rollback before a write", standing: &rerunRecreating, wantPhase: model.InflightRestoring},
		{name: "a recovery of the same rollback past the restore", standing: &rerunDone, wantPhase: model.InflightRestoring},
		// A savepoint that will not come up holds nothing hostage: another one recovers over a
		// running project too, taking no snapshot of its half-restored state.
		{name: "a recovery to another savepoint", standing: &another, wantPhase: model.InflightRestoring},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(false)
			stages.target.prepared.summary = demoSummary()
			recovering := tt.standing != nil
			inflight := &mockInflight{}
			getStanding := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
			if recovering {
				getStanding.Return(*tt.standing, true, nil)
			}
			mock.InOrder(
				getStanding,
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, recovering), nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, tt.wantPhase, tt.wantSafety)).Return(nil).Once(),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, tt.wantSafety)).Return(nil).Once(),
				stages.target.expectRestoreData(nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoreDone, tt.wantSafety)).Return(nil).Once(),
				stages.target.expectStart(60*time.Second, nil),
				stages.target.expectReap(),
				inflight.On("Clear", mock.Anything).Return(nil).Once(),
			)
			svc := newStagedService(stages, savepoints, locker, inflight)
			recorder := &warnRecorder{}
			ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, fixedNow))

			// Act
			res, err := svc.Rollback(ctx, model.RollbackRequest{
				ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: tt.skip,
			})

			// Assert
			require.NoError(t, err)
			assert.Empty(t, res.SafetySnapshot)
			assert.Equal(t, demoSummary().Volumes, res.Restored)
			assert.Equal(t, []string{noSafetySnapshotWarning}, recorder.inResult)
			inflight.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// Without a safety snapshot nothing is reverted: a failure past the marker leaves it standing,
// nothing reaped; a marker that cannot be written touches nothing; an interrupt during the prepare
// writes no marker.
func TestService_Rollback_WithoutSafetySnapshotFailures(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name        string
		interrupt   bool
		putErr      error
		teardownErr error
		wantErr     string
	}{
		{name: "a failure past the marker", teardownErr: errBoom, wantErr: "rollback failed: boom"},
		{
			name:    "a marker that cannot be written",
			putErr:  errors.New("disk full"),
			wantErr: "recording the inflight marker, nothing changed: disk full",
		},
		{
			name:      "an interrupt during the prepare",
			interrupt: true,
			wantErr:   "rollback interrupted, nothing changed: context canceled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
			defer cancel()
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(false)
			inflight := &mockInflight{}
			prepare := stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil)
			calls := []*mock.Call{
				inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
				stages.expectInspect(nil),
				prepare,
			}
			if tt.interrupt {
				prepare.Run(func(mock.Arguments) { cancel() })
			} else {
				calls = append(calls, inflight.On("Put", mock.Anything,
					isInflightMarker(record.ID, model.InflightRecreating)).Return(tt.putErr).Once())
			}
			if tt.teardownErr != nil {
				calls = append(calls, stages.expectLiveTeardown(tt.teardownErr))
			}
			mock.InOrder(calls...)
			svc := newStagedService(stages, savepoints, locker, inflight)

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{
				ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
			})

			// Assert
			require.EqualError(t, err, tt.wantErr)
			inflight.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// A compose project with no containers is built from the savepoint: no hold, no safety snapshot,
// the marker naming none. What the prepared savepoint overwrites is the result's leftover volumes
// and a warning the result repeats; a recovery's are only logged.
func TestService_Rollback_ColdStartBuildsFromTheSavepoint(t *testing.T) {
	recovered := newRecoveredMarker(demoRecord().ID, model.InflightRestoring, false)
	tests := []struct {
		name         string
		standing     *model.InflightMarker
		wantLeftover []string
		wantPhase    model.InflightPhase
	}{
		{name: "a leftover volume overwritten", wantLeftover: []string{"demo_db"}, wantPhase: model.InflightRecreating},
		{name: "a recovery names none", standing: &recovered, wantPhase: model.InflightRestoring},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(true)
			stages.target.prepared.summary = provider.PreparedSummary{
				Volumes:     []model.Volume{demoVolume()},
				Overwritten: []string{"demo_db"},
			}
			recovering := tt.standing != nil
			inflight := &mockInflight{}
			getStanding := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
			if recovering {
				getStanding.Return(*tt.standing, true, nil)
			}
			mock.InOrder(
				getStanding,
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, recovering), nil),
				inflight.On("Put", mock.Anything, isInflightMarker(record.ID, tt.wantPhase)).Return(nil).Once(),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(nil),
				inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoring)).Return(nil).Once(),
				stages.target.expectRestoreData(nil),
				inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoreDone)).Return(nil).Once(),
				stages.target.expectStart(60*time.Second, nil),
				stages.target.expectReap(),
				inflight.On("Clear", mock.Anything).Return(nil).Once(),
			)
			svc := newStagedService(stages, savepoints, locker, inflight)
			recorder := &warnRecorder{}
			logs := &syncBuffer{}
			ctx := progress.WithTracker(zerolog.New(logs).WithContext(context.Background()), progress.New(recorder, fixedNow))

			// Act
			res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.NoError(t, err)
			assert.Empty(t, res.SafetySnapshot)
			assert.Equal(t, tt.wantLeftover, res.LeftoverVolumes)
			if recovering {
				assert.Empty(t, recorder.inResult)
				assert.Contains(t, logs.String(), "the compose project is not running but this volume survives")
			} else {
				assert.Equal(t, []string{model.NewLeftoverVolumeNotice("demo_db").Message}, recorder.inResult)
			}
			inflight.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// A cold start the prepare refuses — a leftover volume under the refusing policy among the
// reasons — writes no marker and builds nothing.
func TestService_Rollback_ColdStartRefusedByThePrepare(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(true)
	stages.expectInspect(nil)
	stages.expectPrepare(record.ID, demoPrepareOptions(record, false), model.ErrLeftoverVolumes)
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	svc := newStagedService(stages, savepoints, locker, inflight)

	// Act
	_, err := svc.Rollback(zerolog.Nop().WithContext(context.Background()),
		model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrLeftoverVolumes)
	inflight.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A cold start that fails discards what it built; the marker is cleared while no volume was
// written, and kept at restoring once one may have been — restore-done going back, since what the
// restore wrote into has just gone.
func TestService_Rollback_ColdStartFailure(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name          string
		buildErr      error
		restoreErr    error
		startErr      error
		discardErr    error
		wantErr       string
		wantMarker    bool
		wantMarkPhase model.InflightPhase
		wantLog       string
	}{
		{name: "a failed build clears the marker", buildErr: errBoom, wantErr: "rollback failed: boom"},
		{
			name:          "a failed restore keeps the marker",
			restoreErr:    errBoom,
			wantErr:       "rollback failed: boom",
			wantMarker:    true,
			wantMarkPhase: model.InflightRestoring,
		},
		{
			name:          "a failed start keeps the marker, not at restore-done",
			startErr:      errBoom,
			wantErr:       "rollback failed: services did not become healthy after restore: boom",
			wantMarker:    true,
			wantMarkPhase: model.InflightRestoring,
		},
		{
			name:       "a failed discard is logged",
			buildErr:   errBoom,
			discardErr: errors.New("down boom"),
			wantErr:    "rollback failed: boom",
			wantLog:    "failed to remove what the failed rollback created",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(true)
			calls := []*mock.Call{
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(tt.buildErr),
			}
			if tt.buildErr == nil {
				calls = append(calls, stages.target.expectRestoreData(tt.restoreErr))
			}
			if tt.startErr != nil {
				calls = append(calls, stages.target.expectStart(60*time.Second, tt.startErr))
			}
			calls = append(calls, stages.target.expectDiscard(tt.discardErr))
			mock.InOrder(calls...)
			inflight := &fakeInflight{}
			svc := newStagedService(stages, savepoints, locker, inflight)
			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.EqualError(t, err, tt.wantErr)
			marker, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			assert.Equal(t, tt.wantMarker, marked)
			if tt.wantMarker {
				assert.Equal(t, tt.wantMarkPhase, marker.Phase)
				assert.Equal(t, record.ID, marker.Target)
				assert.Empty(t, marker.Safety)
			}
			assert.Contains(t, logs.String(), tt.wantLog)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// With no safety snapshot to go back to, the hint names the same rollback again.
func TestRecoveryHint_NoSafetySnapshot(t *testing.T) {
	// Arrange
	marker := model.InflightMarker{
		Phase:          model.InflightRestoring,
		Session:        model.DefaultSession,
		ComposeProject: "demo",
		Target:         demoRecord().ID,
	}

	// Act
	hint := recoveryHint(marker)

	// Assert
	assert.Contains(t, hint, "a rollback that took no safety snapshot was interrupted; volumes may be half-restored")
	assert.Contains(t, hint, "-p demo "+demoRecord().ID.String())
}

// What the hint says of a rollback that took no safety snapshot follows how far it got.
func TestInterruptedState_NoSafetySnapshot(t *testing.T) {
	tests := []struct {
		phase model.InflightPhase
		want  string
	}{
		{phase: model.InflightRecreating, want: "before it wrote any volume"},
		{phase: model.InflightRestoring, want: "volumes may be half-restored"},
		// A failed cold start stands here too, what it restored into removed.
		{phase: model.InflightRestoreDone, want: "volumes may be half-restored"},
	}
	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			assert.Contains(t, interruptedState(model.InflightMarker{Phase: tt.phase}), tt.want)
		})
	}
}

// Recovering from an interrupted rollback with no containers left prepares as a recovery, warns of
// no leftover volume, and its marker never goes back to "nothing written": a failure keeps it.
func TestService_Rollback_ColdStartRerun(t *testing.T) {
	tests := []struct {
		name     string
		phase    model.InflightPhase
		toSafety bool
	}{
		{name: "past a write", phase: model.InflightRestoring},
		{name: "before a write: the live project it took down is still gone", phase: model.InflightRecreating},
		{name: "past a write, to its safety snapshot", phase: model.InflightRestoring, toSafety: true},
		{name: "a failed revert, to its safety snapshot", phase: model.InflightInconsistent, toSafety: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(true)
			stages.target.prepared.summary = provider.PreparedSummary{
				Volumes:     []model.Volume{demoVolume()},
				Overwritten: []string{"demo_db"},
			}
			mock.InOrder(
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, true), nil),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(errors.New("create boom")),
				stages.target.expectDiscard(nil),
			)
			inflight := &fakeInflight{}
			require.NoError(t, inflight.Put(context.Background(), newRecoveredMarker(record.ID, tt.phase, tt.toSafety)))
			svc := newStagedService(stages, savepoints, locker, inflight)
			recorder := &warnRecorder{}
			ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, fixedNow))

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert: the interrupted rollback's partial writes are not warned of.
			require.ErrorContains(t, err, "create boom")
			assert.Empty(t, recorder.inResult)
			marker, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			require.True(t, marked)
			assert.Equal(t, model.InflightRestoring, marker.Phase)
			assert.Equal(t, record.ID, marker.RecoveryID())
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// A failed recovery to the safety snapshot keeps naming it, and frees the gate: a snapshot that
// cannot come up must hold nothing hostage, and the hint must not deny that it exists.
func TestService_Rollback_FailedRecoveryKeepsTheSafetySnapshot(t *testing.T) {
	// Arrange
	record := demoRecord()
	standing := newRecoveredMarker(record.ID, model.InflightInconsistent, true)
	inflight := &fakeInflight{}
	require.NoError(t, inflight.Put(context.Background(), standing))
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, true), nil),
		stages.expectLiveTeardown(errors.New("down failed")),
	)
	svc := newStagedService(stages, savepoints, locker, inflight)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorContains(t, err, "down failed")
	assert.False(t, svc.isRecoveryRollback(standing, demoSafetySnapshot().ID), "before it, only the snapshot recovered")
	marker, found, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	require.True(t, found)
	assert.Equal(t, record.ID, marker.Safety)
	assert.Equal(t, model.InflightRestoring, marker.Phase)
	assert.True(t, svc.isRecoveryRollback(marker, demoSafetySnapshot().ID), "another savepoint may recover now")
	hint := recoveryHint(marker)
	assert.Contains(t, hint, "the recovery to the safety snapshot failed")
	assert.Contains(t, hint, record.ID.String())
	assert.Contains(t, hint, "or name another savepoint")
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// Once the start has passed the rollback has succeeded: an interrupt that lands then still reaps on
// a live context and clears the marker.
func TestService_Rollback_NoSafetySnapshotCleansUpPastAnInterrupt(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	live := mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil })
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
		stages.expectLiveTeardown(nil),
		stages.target.expectBuild(nil),
		stages.target.expectRestoreData(nil),
		stages.target.expectStart(60*time.Second, nil).Run(func(mock.Arguments) { cancel() }),
		stages.target.restored.On("Reap", live).Once(),
	)
	inflight := &fakeInflight{}
	svc := newStagedService(stages, savepoints, locker, inflight)

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
	})

	// Assert
	require.NoError(t, err)
	_, marked, getErr := inflight.Get(context.Background())
	require.NoError(t, getErr)
	assert.False(t, marked)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}
