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
)

// demoMarker is what an interrupted rollback of demoRecord left behind; its remedy
// is demoSafetySnapshot.
func demoMarker(phase model.InflightPhase) model.InflightMarker {
	return model.InflightMarker{
		Phase:  phase,
		Target: demoRecord().ID,
		Safety: demoSafetySnapshot().ID,
		At:     time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}
}

// standingMarker is a fakeInflight preloaded with demoMarker.
func standingMarker(t *testing.T, phase model.InflightPhase) *fakeInflight {
	t.Helper()
	inflight := &fakeInflight{}
	require.NoError(t, inflight.Put(context.Background(), demoMarker(phase)))
	return inflight
}

// The gate runs right after the lock: a refused create reads nothing and stops
// nothing, so every other mock closes the test empty.
func TestService_CreateSavepoint_RefusedWhileInflight(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	inspector := &mockInspector{}
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}
	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(), ComposeRunner: runner, BackendRegistry: registry, Inflight: standingMarker(t, model.InflightRestoring),
		Store: nil, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	var interrupted *model.InterruptedRollbackError
	require.ErrorAs(t, err, &interrupted)
	assert.Equal(t, demoMarker(model.InflightRestoring), interrupted.Marker)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_RemoveSavepoints_RefusedWhileInflight(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: nil,
		ComposeRunner: nil, BackendRegistry: nil, Inflight: standingMarker(t, model.InflightInconsistent),
		Store: nil, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act: an id selector, which could otherwise reach the safety snapshot itself.
	_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []model.SavepointID{demoSafetySnapshot().ID}})

	// Assert: refused before the listing — the remedy stays whole.
	var interrupted *model.InterruptedRollbackError
	require.ErrorAs(t, err, &interrupted)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A rollback to anything but the marker's own safety snapshot is refused, and the
// resolve's listing is the one call made before the refusal.
func TestService_Rollback_RefusedWhileInflight(t *testing.T) {
	// Arrange
	record := demoRecord()
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	inspector := &mockInspector{}
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}
	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(), ComposeRunner: runner, BackendRegistry: registry, Inflight: standingMarker(t, model.InflightRestoring),
		Store: nil, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act: a blind retry of the interrupted rollback, still refused — its undo would
	// capture a mix of epochs.
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID)})

	// Assert
	var interrupted *model.InterruptedRollbackError
	require.ErrorAs(t, err, &interrupted)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
	inspector.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// The remedy the marker names passes the gate, runs as an ordinary rollback, and
// clears the marker on success.
func TestService_Rollback_RemedyPassesTheGateAndClearsTheMarker(t *testing.T) {
	// Arrange: the record being restored IS the safety snapshot the marker names.
	vol := demoVolume()
	record := demoSafetySnapshot()
	repo, inspector, locker := demoResolution(record)
	inflight := standingMarker(t, model.InflightRestoring)

	var newSafetyID model.SavepointID
	registry := &mockRegistry{}
	savepointBackend := &mockBackend{volume: vol}
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { newSafetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "undo-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, record.ID)
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "safety-payload"}).Return(nil).Once()
	up := runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &newSafetyID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == newSafetyID && req.Payload == "undo-payload"
	})).Return(nil).Once()
	mock.InOrder(stop, snapshotSafety, specSafety, commitSafety, down, create,
		restoreSp, up, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(), ComposeRunner: runner, BackendRegistry: registry, Inflight: inflight,
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID(record.ID),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	_, found, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	assert.False(t, found, "a successful remedy clears the marker")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// The marker's lifecycle across a clean rollback: restoring before the first
// destructive write, restore-done before the restart, cleared before the drop.
// Every neighbouring pair compiles when swapped, so the chain is the pin.
func TestService_Rollback_WritesAndClearsTheMarker(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	matchMarker := func(phase model.InflightPhase) any {
		return mock.MatchedBy(func(m model.InflightMarker) bool {
			return m.Phase == phase && m.Target == record.ID && m.Safety == safetySnapshotID
		})
	}
	inflight := &mockInflight{}
	gateGet := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	putRecreating := inflight.On("Put", mock.Anything, matchMarker(model.InflightRecreating)).Return(nil).Once()
	putRestoring := inflight.On("Put", mock.Anything, matchMarker(model.InflightRestoring)).Return(nil).Once()
	putDone := inflight.On("Put", mock.Anything, matchMarker(model.InflightRestoreDone)).Return(nil).Once()
	clear := inflight.On("Clear", mock.Anything).Return(nil).Once()

	registry := &mockRegistry{}
	savepointBackend := &mockBackend{volume: vol}
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, record.ID)
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	up := runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// The marker steps in workflow order: written before the teardown, phased to
	// restoring before a byte moves and forward before the restart, then cleared.
	mock.InOrder(gateGet, stop, snapshotSafety, specSafety, commitSafety,
		putRecreating, down, create, putRestoring, restoreSp, putDone, up,
		clear, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(), ComposeRunner: runner, BackendRegistry: registry, Inflight: inflight,
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID(record.ID),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	inflight.AssertExpectations(t)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// The write-ahead bit is strict: a marker that cannot be written aborts before the
// first destructive write — the snapshot is dropped and the services come back.
func TestService_Rollback_MarkerWriteFailureAbortsBeforeRestore(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	inflight := &mockInflight{}
	gateGet := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	putRecreating := inflight.On("Put", mock.Anything, mock.MatchedBy(func(m model.InflightMarker) bool {
		return m.Phase == model.InflightRecreating && m.Target == record.ID
	})).Return(errors.New("disk full")).Once()

	registry := &mockRegistry{}
	savepointBackend := &mockBackend{volume: vol}
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, record.ID)
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	up := runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()
	mock.InOrder(gateGet, stop, snapshotSafety, specSafety, commitSafety,
		putRecreating, uncommitSafety, removeSafety, up)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(), ComposeRunner: runner, BackendRegistry: registry, Inflight: inflight,
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID(record.ID),
		WaitTimeout: 60 * time.Second,
	})

	// Assert: nothing changed, and the caller is told why.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recording the inflight marker")
	assert.Contains(t, err.Error(), "nothing changed")
	inflight.AssertExpectations(t)
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// A failed revert leaves the marker standing at inconsistent, naming the safety
// snapshot that is now the last copy of the pre-rollback state.
func TestService_Rollback_RevertFailureMarksInconsistent(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)
	inflight := &fakeInflight{}

	var safetySnapshotID model.SavepointID
	registry := &mockRegistry{}
	savepointBackend := &mockBackend{volume: vol}
	runner := &mockComposeRunner{}
	stopBeforeBackup := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, record.ID)
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()
	// The revert rebuilds the live stack and fails replaying its own capture: no
	// start follows.
	downForRevert := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	createForRevert := runner.On("Create", mock.Anything, demoSpec).Return(nil).Once()
	restoreSafety := safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()
	mock.InOrder(stopBeforeBackup, snapshotSafety, specSafety, commitSafety, down, create,
		restoreSp, downForRevert, createForRevert, restoreSafety)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(), ComposeRunner: runner, BackendRegistry: registry, Inflight: inflight,
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID(record.ID),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed")
	marker, found, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	require.True(t, found, "the marker stays with the retained safety snapshot")
	assert.Equal(t, model.InflightInconsistent, marker.Phase)
	assert.Equal(t, record.ID, marker.Target)
	assert.Equal(t, safetySnapshotID, marker.Safety)
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}
