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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// demoMarker is what an interrupted rollback of demoRecord left behind; the way back
// is demoSafetySnapshot.
func demoMarker(phase model.InflightPhase) model.InflightMarker {
	return model.InflightMarker{
		Phase:          phase,
		Session:        model.DefaultSession,
		ComposeProject: demoProject,
		Target:         demoRecord().ID,
		Safety:         demoSafetySnapshot().ID,
		At:             time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}
}

// demoSession is a session other than the default, for the tests about the marker's session.
var demoSession = model.SessionID(uuid.MustParse("0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"))

// standingMarker is a fakeInflight preloaded with demoMarker.
func standingMarker(t *testing.T, phase model.InflightPhase) *fakeInflight {
	t.Helper()
	inflight := &fakeInflight{}
	require.NoError(t, inflight.Put(context.Background(), demoMarker(phase)))
	return inflight
}

// Every workflow takes the project lock, then the session's lock under its own kind, and frees
// them in reverse.
func TestService_locked(t *testing.T) {
	tests := []struct {
		name      string
		operation model.SessionLockOperation
		run       func(svc *Service) error
	}{
		{
			name:      "savepoint create",
			operation: model.SessionLockOperationSavepointCreate,
			run: func(svc *Service) error {
				_, err := svc.CreateSavepoint(newLockTestContext(), model.CreateSavepointRequest{Name: "sp"})
				return err
			},
		},
		{
			name:      "savepoint remove",
			operation: model.SessionLockOperationSavepointRemove,
			run: func(svc *Service) error {
				_, err := svc.RemoveSavepoints(newLockTestContext(),
					model.RemoveRequest{Refs: []string{"019e843e-1650-7000-8000-000000000001"}})
				return err
			},
		},
		{
			name:      "rollback",
			operation: model.SessionLockOperationRollback,
			run: func(svc *Service) error {
				_, err := svc.Rollback(newLockTestContext(), model.RollbackRequest{Latest: utils.New(true)})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: the first read under the locks fails, so the workflow stops there.
			locker := &mockLocker{}
			admission := &mockWriteAdmission{}
			sessionLocker := &mockSessionLocker{}
			repo := &mockSavepoints{}
			grantedLock := &mockGrantedSessionLock{}
			calls := []*mock.Call{
				locker.On("Acquire", mock.Anything).Return(nil).Once(),
				admission.On("AdmitWrite", mock.Anything).Return(nil).Once(),
				sessionLocker.On("Lock", mock.Anything, tt.operation).Return(grantedLock, nil).Once(),
			}
			calls = append(calls,
				repo.On("List", mock.Anything).Return(nil, errListStore).Once(),
				grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
				locker.On("Release").Return(nil).Once(),
			)
			mock.InOrder(calls...)
			svc := New(Deps{
				ArcaveProject:  demoArcaveProject,
				ProjectLocker:  locker,
				WriteAdmission: admission,
				SessionLocker:  sessionLocker,
				Savepoints:     repo,
				Inflight:       &fakeInflight{},
			})

			// Act
			err := tt.run(svc)

			// Assert
			require.ErrorIs(t, err, errListStore)
			locker.AssertExpectations(t)
			admission.AssertExpectations(t)
			sessionLocker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

func TestService_locked_AWriteRefused(t *testing.T) {
	// Arrange: the admission refuses under the project lock, so the session is never locked.
	locker := &mockLocker{}
	admission := &mockWriteAdmission{}
	sessionLocker := &mockSessionLocker{}
	mock.InOrder(
		locker.On("Acquire", mock.Anything).Return(nil).Once(),
		admission.On("AdmitWrite", mock.Anything).Return(model.ErrStoreHoldsPlainData).Once(),
		locker.On("Release").Return(nil).Once(),
	)
	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		WriteAdmission: admission,
		SessionLocker:  sessionLocker,
		Savepoints:     &mockSavepoints{},
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.CreateSavepoint(newLockTestContext(), model.CreateSavepointRequest{Name: "sp"})

	// Assert
	require.ErrorIs(t, err, model.ErrStoreHoldsPlainData)
	locker.AssertExpectations(t)
	admission.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
}

// A workflow that cannot claim the project must abort before it touches
// anything: it must not stop the services a concurrent run is depending on, and
// it must not even read the savepoint store. The lock is taken first, so every
// collaborator below stays untouched — the mocks carry no expectations, and a
// stray call panics on the missing one.
func TestService_MutatingWorkflows_AbortWhenTheProjectIsLocked(t *testing.T) {
	tests := []struct {
		name string
		call func(svc *Service, ctx context.Context) error
	}{
		{
			name: "CreateSavepoint",
			call: func(svc *Service, ctx context.Context) error {
				_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})
				return err
			},
		},
		{
			name: "Rollback",
			call: func(svc *Service, ctx context.Context) error {
				_, err := svc.Rollback(ctx, model.RollbackRequest{Latest: ptrBool(true)})
				return err
			},
		},
		{
			name: "RemoveSavepoints",
			call: func(svc *Service, ctx context.Context) error {
				_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{"1"}})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			locker.On("Acquire", mock.Anything).Return(model.ErrProjectLocked)
			savepoints := &mockSavepoints{}
			stages := newMockStages(false)
			svc := newStagedService(t, stages, savepoints, locker, &fakeInflight{})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.call(svc, ctx)

			// Assert
			require.ErrorIs(t, err, model.ErrProjectLocked)
			// A lock that was never taken must not be released, and the locker carries
			// no Release expectation — a release would fail the run as an unexpected call.
			locker.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// TestService_requireNoInflight checks the gate matches by session and id: the same id
// in another session passes nothing.
func TestService_requireNoInflight(t *testing.T) {
	marker := demoMarker(model.InflightRestoring)
	marker.Session = demoSession
	coldMarker := marker
	coldMarker.Safety = ""
	otherCompose := marker
	otherCompose.ComposeProject = "other"
	tests := []struct {
		name      string
		marker    model.InflightMarker
		session   model.SessionID
		restoring model.SavepointID
		wantPass  bool
	}{
		{name: "safety snapshot in the marker's session", marker: marker, session: demoSession,
			restoring: marker.Safety, wantPass: true},
		{name: "safety snapshot id in another session", marker: marker, session: model.DefaultSession,
			restoring: marker.Safety},
		{name: "cold-start target in the marker's session", marker: coldMarker, session: demoSession,
			restoring: coldMarker.Target, wantPass: true},
		{name: "cold-start target id in another session", marker: coldMarker, session: model.DefaultSession,
			restoring: coldMarker.Target},
		{name: "any savepoint under a marker naming no safety snapshot", marker: coldMarker, session: demoSession,
			restoring: marker.Safety, wantPass: true},
		{name: "any savepoint in another session", marker: coldMarker, session: model.DefaultSession,
			restoring: marker.Safety},
		{name: "any other workflow under a marker naming no safety snapshot", marker: coldMarker, session: demoSession},
		{name: "any other workflow", marker: marker, session: demoSession},
		{name: "the target of a rollback that took a safety snapshot", marker: marker, session: demoSession,
			restoring: marker.Target},
		{name: "safety snapshot id in another compose project", marker: otherCompose, session: demoSession,
			restoring: marker.Safety},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			inflight := &fakeInflight{}
			require.NoError(t, inflight.Put(context.Background(), tt.marker))
			svc := New(Deps{SessionLocker: fakeSessionLocker{}, Inflight: inflight, Session: tt.session, ComposeProject: demoProject})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			recovery, err := svc.requireNoInflight(ctx, tt.restoring)

			// Assert
			assert.Equal(t, tt.wantPass, recovery.recovering)
			if tt.wantPass {
				require.NoError(t, err)
				assert.Equal(t, tt.marker.Safety, recovery.safety)
				return
			}
			var interrupted *model.InterruptedRollbackError
			require.ErrorAs(t, err, &interrupted)
			assert.Equal(t, tt.marker, interrupted.Marker)
		})
	}
}

// The gate runs right after the lock: a refused create reads nothing and stops
// nothing, so every other mock closes the test empty.
func TestService_CreateSavepoint_RefusedWhileInflight(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	savepoints := &mockSavepoints{}
	stages := newMockStages(false)
	svc := New(Deps{
		Store:          newDirStore(t),
		ArcaveProject:  demoArcaveProject,
		SessionLocker:  fakeSessionLocker{},
		WriteAdmission: fakeWriteAdmission{},
		Savepoints:     savepoints,
		ProjectLocker:  locker,
		Workload:       &stages.workload,
		Inflight:       standingMarker(t, model.InflightRestoring),
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	var interrupted *model.InterruptedRollbackError
	require.ErrorAs(t, err, &interrupted)
	assert.Equal(t, demoMarker(model.InflightRestoring), interrupted.Marker)
	locker.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A rollback to anything but the marker's own safety snapshot is refused, and the
// resolve's listing is the one call made before the refusal.
func TestService_Rollback_RefusedWhileInflight(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints := &mockSavepoints{}
	savepoints.On("List", mock.Anything).Return([]model.Savepoint{record}, nil).Twice()
	locker := &mockLocker{}
	expectLock(locker)
	stages := newMockStages(false)
	svc := newStagedService(t, stages, savepoints, locker, standingMarker(t, model.InflightRestoring))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act: a blind retry of the interrupted rollback, still refused — its undo would
	// capture a mix of epochs.
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID)})

	// Assert
	var interrupted *model.InterruptedRollbackError
	require.ErrorAs(t, err, &interrupted)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A rollback to the marker's safety snapshot passes the gate and clears the marker on success. It
// takes no safety snapshot of its own: that would capture the half-restored volumes as an undo.
func TestService_Rollback_SafetySnapshotPassesTheGateAndClearsTheMarker(t *testing.T) {
	// Arrange: the record being restored IS the safety snapshot the marker names.
	record := demoSafetySnapshot()
	savepoints, locker := demoResolution(record)
	inflight := standingMarker(t, model.InflightRestoring)
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, true), nil),
		stages.expectLiveTeardown(nil),
		stages.target.expectBuild(nil),
		stages.target.expectRestoreData(nil),
		stages.target.expectStart(60*time.Second, nil),
		stages.target.expectReap(),
	)
	svc := newStagedService(t, stages, savepoints, locker, inflight)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	result, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID(record.ID),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, result.SafetySnapshot)
	_, found, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	assert.False(t, found, "a successful recovery clears the marker")
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A marker naming no safety snapshot protects no earlier state: a rollback to its own target or to
// another savepoint passes the gate as a recovery and clears it.
func TestService_Rollback_MarkerWithoutSafetySnapshot(t *testing.T) {
	tests := []struct {
		name   string
		target model.SavepointID
		absent bool
	}{
		{name: "its own target", target: demoRecord().ID, absent: true},
		{name: "another target", target: "019e83ef-ee80-7000-8000-000000000000", absent: true},
		// A savepoint that will not come up holds nothing hostage: another one recovers over a
		// running project too, taking no snapshot of its half-restored state.
		{name: "another target over a running project", target: "019e83ef-ee80-7000-8000-000000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			inflight := &fakeInflight{}
			require.NoError(t, inflight.Put(context.Background(), model.InflightMarker{
				Phase: model.InflightRestoring, Session: model.DefaultSession, ComposeProject: demoProject, Target: tt.target,
				At: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC),
			}))
			// A recovery takes no safety snapshot: nothing is planned or held.
			stages := newMockStages(tt.absent)
			mock.InOrder(
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, true), nil),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(nil),
				stages.target.expectRestoreData(nil),
				stages.target.expectStart(60*time.Second, nil),
				stages.target.expectReap(),
			)
			svc := newStagedService(t, stages, savepoints, locker, inflight)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.NoError(t, err)
			_, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			assert.False(t, marked)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

func TestService_CreateSavepoint_RefusesADeletedSession(t *testing.T) {
	// Arrange: --session resolved, then the session was deleted before the lock was taken.
	locker := &mockLocker{}
	expectLock(locker)
	sessions := &mockSessionLookup{}
	sessions.On("LoadSession", mock.Anything, demoSession).Return(model.Session{}, false, nil).Once()
	svc := New(Deps{SessionLocker: fakeSessionLocker{}, WriteAdmission: fakeWriteAdmission{}, ProjectLocker: locker, Session: demoSession, Sessions: sessions, Inflight: &fakeInflight{}})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionNotFound)
	sessions.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestService_CreateSavepoint_RefusedWhileSessionLocked(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	sessionLocker := &mockSessionLocker{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).
		Return(nil, model.ErrSessionLocked).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, lock, release)
	stages := newMockStages(false)
	svc := New(Deps{
		Store:          newDirStore(t),
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		SessionLocker:  sessionLocker,
		WriteAdmission: fakeWriteAdmission{},
		Workload:       &stages.workload,
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.CreateSavepoint(newLockTestContext(), model.CreateSavepointRequest{Name: "sp"})

	// Assert: another host's operation holds the session; nothing is read or stopped.
	require.ErrorIs(t, err, model.ErrSessionLocked)
	locker.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A removal checks the session's lock before it uncommits each savepoint: a lost lock removes
// nothing.
func TestService_RemoveSavepoints_LostLockRemovesNothing(t *testing.T) {
	// Arrange
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepoints{}
	list := repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	sessionLocker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSavepointRemove).Return(grantedLock, nil).Once()
	verify := grantedLock.On("Verify", mock.Anything).Return(model.ErrSessionLockLost).Once()
	unlock := grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	mock.InOrder(lock, list, verify, unlock)
	svc := New(Deps{
		WriteAdmission: fakeWriteAdmission{},
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		SessionLocker:  sessionLocker,
		Savepoints:     repo,
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.RemoveSavepoints(newLockTestContext(), model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	repo.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSavepointStoreService_ListSavepoints_AdmitsNoWrite(t *testing.T) {
	// Arrange: an admission with no expectation fails on any call.
	admission := &mockWriteAdmission{}
	repo := &mockSavepoints{}
	repo.On("List", mock.Anything).Return([]model.Savepoint{}, nil).Once()
	svc := NewSavepointStoreService(SavepointStoreDeps{Savepoints: repo, WriteAdmission: admission})

	// Act
	_, err := svc.ListSavepoints(newLockTestContext(), model.ListRequest{Sort: model.SortDesc})

	// Assert
	require.NoError(t, err)
	admission.AssertExpectations(t)
	repo.AssertExpectations(t)
}
