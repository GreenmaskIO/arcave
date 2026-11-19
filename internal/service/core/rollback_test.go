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
	"encoding/json"
	"errors"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// newStagedService is a Service walking stages, its savepoints listed by savepoints, the
// session's lock always held, its heartbeats written to a tempdir.
func newStagedService(
	t *testing.T, stages *mockStages, savepoints *mockSavepoints, locker *mockLocker, inflight inflightMarkers,
) *Service {
	t.Helper()
	return New(Deps{
		Store:          newDirStore(t),
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
		Restored: model.RestoreSummary{
			Units:   []model.UnitTotal{{Kind: model.UnitKindVolumes, Count: 1}, {Kind: model.UnitKindLayers, Count: 1}},
			Notices: []model.Notice{{Message: "the containers run on the savepoint's writable layers"}},
		},
		Detail: json.RawMessage(`{"writable_layers":[{"tag":"arcave/demo-db-1:sp"}]}`),
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
	svc := newStagedService(t, stages, savepoints, locker, inflight)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.RollbackResult{
		RolledBackTo:   record,
		Restored:       summary.Restored,
		Detail:         summary.Detail,
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
	svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})
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
	stages.expectInspect(errUnsupportedDaemon)
	svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: time.Second})

	// Assert
	require.ErrorIs(t, err, errUnsupportedDaemon)
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
			svc := newStagedService(t, stages, savepoints, locker, inflight)
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
	svc := newStagedService(t, stages, savepoints, locker, inflight)
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
	svc := newStagedService(t, stages, savepoints, locker, inflight)
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
	inflight := &fakeInflight{}
	svc := newStagedService(t, stages, savepoints, locker, inflight)

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "rollback failed: interrupted")
	_, marked, getErr := inflight.Get(context.Background())
	require.NoError(t, getErr)
	assert.False(t, marked, "the cleanup after the revert clears the marker past the interrupt")
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
			svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})
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
// checked. A failure in any is the caller's to release and ends the heartbeat failed; a lost lock
// abandons the capture.
func TestService_takeSafetySnapshot(t *testing.T) {
	boom := errors.New("boom")
	unnamed := demoRecord()
	unnamed.Name = ""
	tests := []struct {
		name       string
		savepoint  model.Savepoint
		holdErr    error
		captureErr error
		verifyErr  error
		// wantErr spells the minted id as <id>.
		wantErr  string
		wantBeat heartbeat.Status
	}{
		{
			name:      "a failed hold captures nothing",
			savepoint: demoRecord(),
			holdErr:   boom,
			wantErr:   "stop services: boom",
			wantBeat:  heartbeat.StatusFailed,
		},
		{
			name:       "a failed capture",
			savepoint:  demoRecord(),
			captureErr: boom,
			wantErr:    "pre-rollback safety snapshot failed, nothing changed: boom",
			wantBeat:   heartbeat.StatusFailed,
		},
		{
			name:      "a lost lock commits nothing",
			savepoint: demoRecord(),
			verifyErr: model.ErrSessionLockLost,
			wantErr: "pre-rollback safety snapshot failed, nothing changed: commit savepoint <id>: " +
				model.ErrSessionLockLost.Error(),
			wantBeat: heartbeat.StatusFailed,
		},
		{name: "a named savepoint", savepoint: demoRecord(), wantBeat: heartbeat.StatusDone},
		{name: "an unnamed savepoint", savepoint: unnamed, wantBeat: heartbeat.StatusDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var (
				want  model.Savepoint
				id    model.SavepointID
				store = newDirStore(t)
			)
			svc := New(Deps{Store: store})
			stages := newMockStages(false)
			grantedLock := &mockGrantedSessionLock{}
			// The hold runs inside the heartbeat: its tree holds exactly one object then.
			hold := stages.expectHold(tt.holdErr).Run(func(mock.Arguments) {
				objects, err := store.List(context.Background(), "")
				require.NoError(t, err)
				require.Len(t, objects, 1)
				id = model.SavepointID(path.Dir(objects[0].Name))
			})
			calls := []*mock.Call{hold}
			if tt.holdErr == nil {
				calls = append(calls, stages.expectCaptureOf(provider.CaptureRequest{
					Labels:      safetySnapshotLabels(),
					TakenBefore: &model.SavepointRef{ID: tt.savepoint.ID, Name: tt.savepoint.Name},
				}, tt.captureErr))
			}
			if tt.holdErr == nil && tt.captureErr == nil {
				calls = append(calls, grantedLock.On("Verify", mock.Anything).Return(tt.verifyErr).Once())
			}
			if tt.holdErr == nil && tt.captureErr == nil && tt.verifyErr == nil {
				want = demoSafetySnapshot()
				calls = append(calls, stages.expectCommit(want, nil))
			}
			mock.InOrder(calls...)

			// Act
			held, got, err := svc.takeSafetySnapshot(context.Background(), grantedLock, tt.savepoint, &stages.plan)

			// Assert
			require.NoError(t, id.Validate(), "the heartbeat lands in a savepoint's tree")
			assert.Equal(t, tt.wantBeat, readSavepointHeartbeat(t, store, id).Status)
			if tt.holdErr == nil {
				assert.Equal(t, id, stages.capturedID, "the capture is of the tree the heartbeat frames")
			}
			if tt.wantErr != "" {
				require.EqualError(t, err, strings.ReplaceAll(tt.wantErr, "<id>", id.String()))
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
			svc := newStagedService(t, stages, savepoints, locker, inflight)

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
			svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})

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
	svc := newStagedService(t, stages, &mockSavepoints{}, &mockLocker{}, &fakeInflight{})
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

// A rollback aborted before its restore releases what its safety snapshot held on a revertContext:
// the release outlives the interrupt. No session lock is asked.
func TestAbortBeforeRestore(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(newLockTestContext())
	cancel()
	capturePlan := &mockCapturePlan{}
	revertCtx := mock.MatchedBy(func(ctx context.Context) bool {
		_, bounded := ctx.Deadline()
		return ctx.Err() == nil && bounded
	})
	capturePlan.On("Release", revertCtx, time.Minute).Return(nil).Once()

	// Act
	abortBeforeRestore(ctx, capturePlan, time.Minute)

	// Assert
	capturePlan.AssertExpectations(t)
}
