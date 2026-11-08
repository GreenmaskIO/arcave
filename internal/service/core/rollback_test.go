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
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	savepointrepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestService_Rollback_RestoresAndKeepsTheSafetySnapshot(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	// The expectations are declared in workflow order and the InOrder below pins
	// it. The safety snapshot's calls all follow the Snapshot that fills safetySnapshotID; the
	// errgroup barrier between groups gives each matcher's read a happens-before.
	var safetySnapshotID model.SavepointID
	registry := &mockRegistry{}
	savepointBackend := &mockBackend{volume: vol}
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
	// The teardown takes the live compose project down; the rebuild puts back the recorded one.
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, rolledBackTo(demoTargetSpec, record), demoRestoredVolumes).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	up := runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once()

	// The whole workflow: stop → back up the live state, write its spec and commit
	// its record → tear the compose project down → rebuild it from the savepoint's spec →
	// restore → start. Every neighbouring pair
	// compiles when swapped, so the chain is the pin.
	mock.InOrder(stop, snapshotSafety, specSafety, commitSafety, down, create,
		restoreSp, up)

	// Twice over the one store: one backend per volume per set, and no third build.
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
		ArcaveProject:    "app",
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, record, res.RolledBackTo)

	// The safety snapshot was taken under the id the run created, committed under it
	// and stays; no expectation above lets it be deleted or the savepoint's own record rewritten.
	require.NoError(t, safetySnapshotID.Validate())
	assert.Equal(t, safetySnapshotID, res.SafetySnapshot)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// A failed restore reverts through the very backend instances that took the
// backup: the .Once builds above pin one safety instance, so the revert's
// Restore can only land on the one whose Snapshot wrote the backup.
func TestService_Rollback_RevertsOnFailedRestore(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	// Declared in workflow order; the InOrder below pins it. Stop is two .Once()
	// expectations because it fires twice — before the backup and again as the
	// revert's own stop — and each has its own place in the chain.
	var safetySnapshotID model.SavepointID
	registry := &mockRegistry{}
	savepointBackend := &mockBackend{volume: vol}
	runner := &mockComposeRunner{}
	stopBeforeBackup := runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).
		Return(errors.New("restore boom")).Once()
	// The revert rebuilds the compose project the backup was taken of and replays it under the
	// id its capture reported; a successful revert drops the backup too.
	downForRevert := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	// Labelled as the revert of the rollback to record, not as a rollback to the safety snapshot.
	createForRevert := runner.On("Create", mock.Anything, revertedFrom(demoSpec, record), demoRestoredVolumes).Return(nil).Once()
	restoreSafety := safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	up := runner.On("Start", mock.Anything, revertedFrom(demoSpec, record), 60*time.Second).Return(nil).Once()

	// Stop → backup, spec and commit → tear down and rebuild → the failed restore,
	// then the revert: tear down and rebuild again → replay → restart.
	mock.InOrder(stopBeforeBackup, snapshotSafety, specSafety, commitSafety, down, create, restoreSp,
		downForRevert, createForRevert, restoreSafety, up)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
		ArcaveProject:    "app",
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed: restore volume-named-demo_db: restore capture: restore boom")
	assert.Contains(t, logs.String(), "reverted to the pre-rollback state")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// Services that never come up healthy revert too: an unhealthy compose project after a
// clean restore is still a failed rollback.
func TestService_Rollback_RevertsOnUnhealthy(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	// The backup commits its record, which stays once the revert has returned the environment.
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	// The rollback rebuilds the savepoint's compose project; the revert rebuilds the live one.
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	runner.On("Create", mock.Anything, deployed(demoSpec), demoRestoredVolumes).Return(nil).Once()
	// The first start is the failed health check; the second is the revert's restart.
	runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(errors.New("unhealthy")).Once()
	runner.On("Start", mock.Anything, deployed(demoSpec), 60*time.Second).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed: services did not become healthy after restore: unhealthy")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// A failed revert keeps the safety snapshot: it is now the last copy of the
// pre-rollback state.
func TestService_Rollback_FailedRevertRetainsTheSafetySnapshot(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	// The record is committed and never uncommitted: repo carries no Delete
	// expectation, so an uncommit would fail the run where it happened.
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	runner.On("Create", mock.Anything, deployed(demoSpec), demoRestoredVolumes).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).
		Return(errors.New("restore boom")).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed: restore volume-named-demo_db: restore capture: restore boom")
	// The backup is retained, captures and record alike: repo carries no Delete expectation,
	// so one would fail the run.
	assert.Contains(t, logs.String(), "safety snapshot retained")
	assert.Contains(t, logs.String(), safetySnapshotID.String())
	assert.Contains(t, logs.String(), "--include-safety-snapshots",
		"the log names the way back to a backup that is now the last copy of the pre-rollback state")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// A failed safety snapshot changes nothing: the services are restarted, live data
// is never touched, and what the partial capture wrote is left for a gc.
func TestService_Rollback_FailedSafetySnapshotAbortsAndRestarts(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Return(model.SnapshotStat{}, errors.New("backup boom")).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"pre-rollback safety snapshot failed, nothing changed: snapshot volume-named-demo_db: capture volume: backup boom")
	// Nothing may be restored over live data, and the partial capture is not deleted:
	// no Restore on savepointBackend, no Delete on repo — either would fail the run.
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// The backup a failed revert retains is left as a savepoint: its record is in
// the store, listable by id and hidden from everything else by its label. That
// is the whole of what a killed rollback leaves a human — a manual recovery
// path, not a rollback arcave will finish on its own.
//
// Driven through the real repository over a real store, since a mocked one could
// only report that Save was called.
func TestService_Rollback_RetainedSafetySnapshotIsAListableSavepoint(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	store := newDirStore(t)
	repo := savepointrepo.New(store)
	require.NoError(t, repo.SaveSpec(context.Background(), record.ID, demoTargetSpecBytes))
	require.NoError(t, repo.SaveSavepoint(context.Background(), record))

	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	runner.On("Create", mock.Anything, deployed(demoSpec), demoRestoredVolumes).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).
		Return(errors.New("restore boom")).Once()

	var safetySnapshotID model.SavepointID
	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()
	// The revert fails, which is the one path that keeps the backup.
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, logs.String(), "safety snapshot retained")
	assert.Contains(t, logs.String(), safetySnapshotID.String())

	// The record is in the store, beside the savepoint's own.
	committed, err := svc.ListSavepoints(ctx, model.ListRequest{IncludeSafetySnapshots: true})
	require.NoError(t, err)
	require.Len(t, committed, 2)
	backup, found := byID(committed, safetySnapshotID)
	require.True(t, found, "the retained safety snapshot lists with the flag")
	assert.True(t, backup.IsSafetySnapshot())
	assert.Equal(t, "safety-snapshot-019e843e-1650-7000-8000-000000000000", backup.Name,
		"the name says what the killed rollback was undoing")
	assert.Equal(t, map[string]string{
		model.SavepointLabelSafetySnapshot:    model.SavepointLabelSafetySnapshotValue,
		model.SavepointLabelSafetySnapshotFor: record.ID.String(),
	}, backup.Labels, "and the label names the savepoint the rollback was restoring")
	assert.Equal(t, []model.Volume{vol}, volumesOf(backup),
		"and carries the stats a manual rollback to it would restore through")

	// And the label is what hides it: without the flag only the savepoint shows.
	visible, err := svc.ListSavepoints(ctx, model.ListRequest{})
	require.NoError(t, err)
	assert.Equal(t, []model.Savepoint{record}, visible)

	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// The revert must restart with the caller's context: an errgroup cancels its
// derived one as soon as Wait returns.
func TestService_Rollback_RevertRestartsWithLiveContext(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	// Err is read at call time: the revert's own budget context is cancelled when it
	// returns, so a post-hoc read would see that cancel and not the live restart.
	var safetySnapshotID model.SavepointID
	var upErrs []error
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
	captureCtx := func(args mock.Arguments) { upErrs = append(upErrs, args.Get(0).(context.Context).Err()) }
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	runner.On("Create", mock.Anything, deployed(demoSpec), demoRestoredVolumes).Return(nil).Once()
	// The first start fails the health check, which drives Rollback into revert; the
	// second is revert's own restart, the one under test.
	runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Run(captureCtx).
		Return(errors.New("unhealthy")).Once()
	runner.On("Start", mock.Anything, deployed(demoSpec), 60*time.Second).Run(captureCtx).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	require.Len(t, upErrs, 2)
	assert.NoError(t, upErrs[1], "revert must restart services with a live context, not the cancelled errgroup one")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// Every line a volume's operation logs is attributed from its instance.
func TestService_Rollback_LogsAreAttributedPerVolume(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil)
	runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(), `"volume":"volume-named-demo_db"`)
	assert.Contains(t, logs.String(), `"backend":"fullcopy"`)
	assert.Contains(t, logs.String(), `"snapshot_id":"019e843e-1650-7000-8000-000000000000"`)
	assert.Contains(t, logs.String(), `"op":"restore"`)
	assert.Contains(t, logs.String(), `"snapshot_id":"`+safetySnapshotID.String()+`"`)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// The id gate stands ahead of the whole workflow: an id that could name a
// parent directory builds no backend and stops nothing.
func TestService_Rollback_RejectsAnUnsafeIDBeforeAnyBackend(t *testing.T) {
	// Arrange
	record := demoRecord()
	record.ID = "../../etc"
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	locker := &mockLocker{}
	expectLock(locker)

	// Nothing past the gate carries an expectation: an introspection, a build, a
	// stop or a dispatch fails the test loudly.
	inspector := &mockInspector{}
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}
	store := newDirStore(t)

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("../../etc"),
		WaitTimeout: time.Second,
	})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSavepointID)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// A rollback over real copy captures, in the one store both now write into: the
// savepoint tree survives untouched, and the backup's own tree — record and
// archive alike — is gone by the end.
func TestService_Rollback_OverRealStore(t *testing.T) {
	// Arrange
	// The full literal stays inline: real fullcopy addressing needs the compose
	// project and the mount, not the trimmed mock-test form.
	vol := model.Volume{
		ObjectID:       "volume-named-demo_db",
		ComposeProject: "demo",
		Type:           model.VolumeTypeNamed,
		Backend:        model.SnapshotBackendFullCopy,
		Named: &model.VolumePropertiesNamed{VolumeName: "demo_db",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}},
	}
	record := model.Savepoint{
		ID:        "019e843e-1650-7000-8000-000000000000",
		Name:      "sp",
		CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC),
		Stats:     []model.SnapshotStat{{Volume: vol, Type: model.SnapshotBackendFullCopy, SizeBytes: demoVolumeSize, Payload: map[string]any{"compression": "none"}}},
		Spec:      demoSpecRef(),
	}
	store := newDirStore(t)
	repo := savepointrepo.New(store)
	require.NoError(t, repo.SaveSpec(context.Background(), record.ID, demoTargetSpecBytes))
	require.NoError(t, repo.SaveSavepoint(context.Background(), record))
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil)
	runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil)

	docker := &fakeVolumeStreamer{payload: "live bytes"}
	registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(docker, fullcopy.CompressionNone))
	require.NoError(t, err)
	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})

	// The capture the restore reads, at the key the backend renders.
	archive := "019e843e-1650-7000-8000-000000000000/volume-named-demo_db/data.tar"
	require.NoError(t, store.PutObject(context.Background(), archive, strings.NewReader("savepoint bytes")))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, "savepoint bytes", docker.written.String(), "the archive's stream is what reaches the mount")
	safetyTree := res.SafetySnapshot.String() + "/"
	assert.ElementsMatch(t, []string{
		archive, "019e843e-1650-7000-8000-000000000000/savepoint.json", "019e843e-1650-7000-8000-000000000000/project.yaml",
		safetyTree + "heartbeat.json", safetyTree + "project.yaml", safetyTree + "savepoint.json",
		safetyTree + "volume-named-demo_db/data.tar",
	}, objectNames(t, store), "the savepoint is read, never rewritten, and the backup's tree stays beside it")
	committed, err := repo.ListSavepoints(ctx)
	require.NoError(t, err)
	require.Len(t, committed, 2, "the backup stays committed beside the savepoint")
	got, found := byID(committed, record.ID)
	require.True(t, found)
	assert.Equal(t, record, got)
	safetySnapshot, found := byID(committed, res.SafetySnapshot)
	require.True(t, found, "the result names the safety snapshot it left")
	assert.True(t, safetySnapshot.IsSafetySnapshot())
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
}

// byID picks one record out of a listing whose order the store decides.
func byID(records []model.Savepoint, id model.SavepointID) (model.Savepoint, bool) {
	for _, r := range records {
		if r.ID == id {
			return r, true
		}
	}
	return model.Savepoint{}, false
}

// A rollback lets compose recreate only a named volume it restores: a bind or an anonymous
// volume has no config to drift, and a named volume the savepoint left out has no copy.
func TestNewRestoredVolumeNames(t *testing.T) {
	named := model.Volume{ObjectID: "volume-named-demo_db", Named: &model.VolumePropertiesNamed{VolumeName: "demo_db"}}
	bind := model.Volume{ObjectID: "volume-service-app-/src", Bind: &model.VolumePropertiesBind{Service: "app", Target: "/src"}}
	anon := model.Volume{ObjectID: "volume-anon-app-1-/scratch", Anon: &model.VolumePropertiesAnon{Replica: 1}}
	tests := []struct {
		name  string
		stats []model.SnapshotStat
		want  []string
	}{
		{
			name:  "only the named volume, by its docker name",
			stats: []model.SnapshotStat{{Volume: bind}, {Volume: named}, {Volume: anon}},
			want:  []string{"demo_db"},
		},
		{
			name:  "no named volume restored",
			stats: []model.SnapshotStat{{Volume: bind}, {Volume: anon}},
		},
		{
			name: "nothing restored",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newRestoredVolumeNames(model.Savepoint{Stats: tt.stats})

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestVolumesOf pins the projection, and that it is never nil — restored must
// marshal as [], not null.
func TestVolumesOf(t *testing.T) {
	vol := model.Volume{ObjectID: "volume-named-demo_db", Backend: model.SnapshotBackendFullCopy}

	tests := []struct {
		name   string
		record model.Savepoint
		want   []model.Volume
	}{
		{
			name:   "a stat per volume yields a volume per stat",
			record: model.Savepoint{ID: "100", Stats: []model.SnapshotStat{{Volume: vol, Type: model.SnapshotBackendFullCopy}}},
			want:   []model.Volume{vol},
		},
		{
			name:   "no stats yields an empty slice, never nil",
			record: model.Savepoint{ID: "100"},
			want:   []model.Volume{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := volumesOf(tt.record)

			// Assert
			assert.Equal(t, tt.want, got)
			assert.NotNil(t, got)
		})
	}
}

// A savepoint with no spec is refused before the stop, so the project keeps running.
func TestService_Rollback_RefusesASavepointWithNoSpec(t *testing.T) {
	// Arrange
	record := demoRecord()
	record.Spec = nil
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	registry := &mockRegistry{}
	// Both sets are built: prepare runs first, then the savepoint's own.
	registry.On("New", mock.Anything).Return(&mockBackend{volume: demoVolume()}, nil).Twice()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
	runner := &mockComposeRunner{}
	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            nil,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID)})

	// Assert: nothing was stopped — the runner closes empty.
	require.ErrorIs(t, err, model.ErrSavepointNoSpec)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
	inspector.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// The teardown orphans the anonymous volumes; only those a capture was restored
// into are reaped, anything else is its own last copy.
func TestService_Rollback_ReapsTheOrphanedAnonymousVolumes(t *testing.T) {
	// Arrange
	anonVol := model.Volume{ObjectID: "volume-anon-app-1-scratch", Backend: model.SnapshotBackendFullCopy}
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: "app",
			Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/scratch", VolumeName: "9f8e7d", Replica: 1},
			},
			Containers: []model.Container{{Name: "demo-app-1", Number: 1}},
		}},
	}}
	record := demoRecord()
	record.Stats = []model.SnapshotStat{{Volume: anonVol, Type: model.SnapshotBackendFullCopy, SizeBytes: demoVolumeSize, Payload: "sp-payload"}}

	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil)
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, record.ID)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil)
	locker := &mockLocker{}
	expectLock(locker)

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), []string(nil)).Return(nil)
	runner.On("Start", mock.Anything, deployed(demoTargetSpec), time.Duration(0)).Return(nil)

	savepointBackend := &mockBackend{volume: anonVol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: anonVol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Return(model.SnapshotStat{Volume: anonVol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, anonVol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, anonVol.ObjectID)).Return(savepointBackend, nil).Once()

	// The docker name the mount had before the teardown: the only record of what to reap.
	remover := &mockVolumeRemover{}
	remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(nil).Once()

	// The safety snapshot commits the service's layer like any other savepoint.
	capturer := &mockImageBackend{}
	capturer.On("CaptureWritableLayer", mock.Anything, mock.Anything).Return(model.WritableLayer{Service: "app"}, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    remover,
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		ImageBackend:     capturer,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID)})

	// Assert
	require.NoError(t, err)
	remover.AssertExpectations(t)
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
	capturer.AssertExpectations(t)
}

// demoColdRecord is demoRecordWithImages addressing its volume by docker name, as a
// capture records it: what a surviving volume is matched against.
func demoColdRecord() model.Savepoint {
	record := demoRecordWithImages()
	record.Stats[0].Volume.Named = &model.VolumePropertiesNamed{VolumeName: demoVolumeName}
	return record
}

// coldStartDeps wires a Service over a compose project taken down: the records hold
// record, the inspector reports demoAbsentIntrospection, the daemon is Docker 27.
func coldStartDeps(t *testing.T, record model.Savepoint) (Deps, *mockSavepointRepo, *mockInspector) {
	t.Helper()
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	introspection := demoAbsentIntrospection()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	codec := &mockCodec{}
	codec.On("Parse", mock.Anything, demoTargetSpecBytes).Return(demoTargetSpec, nil).Maybe()
	return Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: &mockAssembler{},
		SpecCodec:        codec,
		ComposeRunner:    &mockComposeRunner{},
		BackendRegistry:  &mockRegistry{},
		ImageBackend:     &mockImageBackend{},
		Inflight:         &fakeInflight{},
		Store:            newDirStore(t),
		CaptureDefaults:  demoCaptureDefaults,
		DaemonVersion:    "27.3.1",
		MinDaemonMajor:   25,
	}, repo, inspector
}

// A compose project with no container takes the short path: load, create, restore,
// start — no stop, no safety snapshot, no teardown and no reap.
func TestService_Rollback_ColdStartBuildsFromTheSavepoint(t *testing.T) {
	// Arrange
	record := demoColdRecord()
	vol := record.Stats[0].Volume
	deps, repo, inspector := coldStartDeps(t, record)
	loadSpec := repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()

	matchMarker := func(phase model.InflightPhase) any {
		return mock.MatchedBy(func(m model.InflightMarker) bool {
			return m.Phase == phase && m.Target == record.ID && m.Safety == ""
		})
	}
	inflight := &mockInflight{}
	gateGet := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	putRecreating := inflight.On("Put", mock.Anything, matchMarker(model.InflightRecreating)).Return(nil).Once()
	putRestoring := inflight.On("Put", mock.Anything, matchMarker(model.InflightRestoring)).Return(nil).Once()
	putDone := inflight.On("Put", mock.Anything, matchMarker(model.InflightRestoreDone)).Return(nil).Once()
	clear := inflight.On("Clear", mock.Anything).Return(nil).Once()

	images := &mockImageBackend{}
	restoreImage := images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
		SnapshotID: record.ID, Layer: demoWritableLayer(), Tag: demoRestoredTag(1),
	}).Return(demoWritableLayerRestoreResult(), nil).Once()
	runner := &mockComposeRunner{}
	target := matchRetargetedImage(demoService, demoRestoredTag(1))
	create := runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(nil).Once()
	backend := &mockBackend{volume: vol}
	restoreVolume := backend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	start := runner.On("Start", mock.Anything, target, 60*time.Second).Return(nil).Once()

	mock.InOrder(gateGet, loadSpec, restoreImage, putRecreating, create, putRestoring,
		restoreVolume, putDone, start, clear)

	registry := &mockRegistry{}
	registry.On("New", matchSpec(deps.Store, vol.ObjectID)).Return(backend, nil).Once()
	volumeRemover := &mockVolumeRemover{}
	deps.Inflight, deps.ImageBackend, deps.ComposeRunner = inflight, images, runner
	deps.BackendRegistry, deps.VolumeRemover = registry, volumeRemover
	svc := New(deps)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert: the volume `compose down` kept is overwritten under a warning naming it.
	require.NoError(t, err)
	assert.Equal(t, record, res.RolledBackTo)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, []model.WritableLayerRestoreResult{demoWritableLayerRestoreResult()}, res.WritableLayers)
	assert.Contains(t, logs.String(), `"volume":"`+demoVolumeName+`"`)
	assert.Contains(t, logs.String(), "no safety snapshot is taken")
	inflight.AssertExpectations(t)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	images.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
	volumeRemover.AssertExpectations(t)
}

// Refusing a surviving volume refuses before anything is loaded or created: the image
// backend, the registry and the runner carry no expectation.
func TestService_Rollback_ColdStartRefusesALeftoverVolume(t *testing.T) {
	// Arrange
	record := demoColdRecord()
	deps, repo, inspector := coldStartDeps(t, record)
	images := &mockImageBackend{}
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}
	inflight := &fakeInflight{}
	deps.ImageBackend, deps.ComposeRunner, deps.BackendRegistry, deps.Inflight = images, runner, registry, inflight
	svc := New(deps)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, LeftoverVolumes: model.LeftoverVolumesRefuse,
	})

	// Assert
	require.ErrorIs(t, err, model.ErrLeftoverVolumes)
	assert.Contains(t, err.Error(), demoVolumeName)
	_, marked, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	assert.False(t, marked)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	images.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// A failure takes down what was created; the marker is cleared while no volume was
// written and kept once one may have been.
func TestService_Rollback_ColdStartFailure(t *testing.T) {
	tests := []struct {
		name          string
		createErr     error
		restoreErr    error
		startErr      error
		wantErr       string
		wantMarker    bool
		wantMarkPhase model.InflightPhase
	}{
		{
			name:      "a failed create clears the marker",
			createErr: errors.New("create boom"),
			wantErr:   "rollback failed: creating the compose project: create boom",
		},
		{
			name:          "a failed restore keeps the marker",
			restoreErr:    errors.New("restore boom"),
			wantErr:       "rollback failed: restore volume-named-demo_db: restore capture: restore boom",
			wantMarker:    true,
			wantMarkPhase: model.InflightRestoring,
		},
		{
			// The failure removes what the restore wrote into, so restore-done would lie.
			name:          "a failed start keeps the marker, not at restore-done",
			startErr:      errors.New("start boom"),
			wantErr:       "rollback failed: services did not become healthy after restore: start boom",
			wantMarker:    true,
			wantMarkPhase: model.InflightRestoring,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoColdRecord()
			vol := record.Stats[0].Volume
			deps, repo, inspector := coldStartDeps(t, record)
			repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
			images := &mockImageBackend{}
			images.On("RestoreWritableLayer", mock.Anything, mock.Anything).Return(demoWritableLayerRestoreResult(), nil).Once()
			runner := &mockComposeRunner{}
			target := matchRetargetedImage(demoService, demoRestoredTag(1))
			create := runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(tt.createErr).Once()
			backend := &mockBackend{volume: vol}
			down := runner.On("Down", mock.Anything, target).Return(nil).Once()
			// demo_db outlived `compose down` and stays; demo_cache is the failed run's own.
			inspector.On("Introspect", mock.Anything).Return(demoAbsentIntrospectionWith("demo_cache"), nil).Once()
			volumeRemover := &mockVolumeRemover{}
			removeCreated := volumeRemover.On("RemoveVolume", mock.Anything, "demo_cache").Return(nil).Once()
			if tt.createErr == nil {
				restore := backend.On("Restore", mock.Anything, mock.Anything).Return(tt.restoreErr).Once()
				if tt.startErr != nil {
					start := runner.On("Start", mock.Anything, target, 60*time.Second).Return(tt.startErr).Once()
					mock.InOrder(create, restore, start, down, removeCreated)
				} else {
					mock.InOrder(create, restore, down, removeCreated)
				}
			} else {
				mock.InOrder(create, down, removeCreated)
			}
			deps.VolumeRemover = volumeRemover
			registry := &mockRegistry{}
			registry.On("New", matchSpec(deps.Store, vol.ObjectID)).Return(backend, nil).Once()
			inflight := &fakeInflight{}
			deps.ImageBackend, deps.ComposeRunner, deps.BackendRegistry, deps.Inflight = images, runner, registry, inflight
			svc := New(deps)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			marker, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			assert.Equal(t, tt.wantMarker, marked)
			if tt.wantMarker {
				assert.Equal(t, tt.wantMarkPhase, marker.Phase)
				assert.Equal(t, record.ID, marker.Target)
				assert.Empty(t, marker.Safety)
			}
			repo.AssertExpectations(t)
			inspector.AssertExpectations(t)
			images.AssertExpectations(t)
			runner.AssertExpectations(t)
			registry.AssertExpectations(t)
			backend.AssertExpectations(t)
			volumeRemover.AssertExpectations(t)
		})
	}
}

// demoAbsentIntrospectionWith is demoAbsentIntrospection owning the extra volumes too.
func demoAbsentIntrospectionWith(names ...string) model.IntrospectionResult {
	introspection := demoAbsentIntrospection()
	for _, name := range names {
		introspection.Project.Volumes = append(introspection.Project.Volumes, model.NamedVolume{Key: name, Name: name})
	}
	return introspection
}

// Recovering from an interrupted rollback with no containers left is not refused over the volumes
// it wrote, and its marker never goes back to "nothing written": a failure keeps it.
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
			record := demoColdRecord()
			vol := record.Stats[0].Volume
			deps, repo, inspector := coldStartDeps(t, record)
			repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
			inspector.On("Introspect", mock.Anything).Return(demoAbsentIntrospection(), nil).Once()
			inflight := &fakeInflight{}
			require.NoError(t, inflight.Put(context.Background(), newRecoveredMarker(record.ID, tt.phase, tt.toSafety)))
			images := &mockImageBackend{}
			images.On("RestoreWritableLayer", mock.Anything, mock.Anything).Return(demoWritableLayerRestoreResult(), nil).Once()
			runner := &mockComposeRunner{}
			target := matchRetargetedImage(demoService, demoRestoredTag(1))
			runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(errors.New("create boom")).Once()
			runner.On("Down", mock.Anything, target).Return(nil).Once()
			registry := &mockRegistry{}
			registry.On("New", matchSpec(deps.Store, vol.ObjectID)).Return(&mockBackend{volume: vol}, nil).Once()
			deps.Inflight, deps.ImageBackend, deps.ComposeRunner, deps.BackendRegistry = inflight, images, runner, registry
			svc := New(deps)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{
				ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, LeftoverVolumes: model.LeftoverVolumesRefuse,
			})

			// Assert
			require.Error(t, err)
			assert.NotErrorIs(t, err, model.ErrLeftoverVolumes)
			assert.Contains(t, err.Error(), "create boom")
			marker, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			require.True(t, marked)
			assert.Equal(t, model.InflightRestoring, marker.Phase)
			assert.Equal(t, record.ID, marker.RecoveryID())
			repo.AssertExpectations(t)
			inspector.AssertExpectations(t)
			images.AssertExpectations(t)
			runner.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}

// Loading layers needs Docker 25+ even when the compose project has no containers.
func TestService_Rollback_ColdStartUnsupportedDaemonLoadsNothing(t *testing.T) {
	// Arrange
	record := demoColdRecord()
	deps, repo, _ := coldStartDeps(t, record)
	deps.DaemonVersion = "24.0.7"
	introspection := demoAbsentIntrospection()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	images := &mockImageBackend{}
	runner := &mockComposeRunner{}
	deps.ComposeInspector, deps.ImageBackend, deps.ComposeRunner = inspector, images, runner
	svc := New(deps)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrUnsupportedDaemon)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	images.AssertExpectations(t)
	runner.AssertExpectations(t)
}

// An interrupt during the restore runs the revert, and the cleanup after it, on a
// live context: the Ctrl-C that aborted the rollback must not tear the way back.
func TestService_Rollback_InterruptedRestoreReverts(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	liveErr := func(errs *[]error) func(mock.Arguments) {
		return func(args mock.Arguments) { *errs = append(*errs, args.Get(0).(context.Context).Err()) }
	}

	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	var revertErrs []error
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	runner.On("Down", mock.Anything, demoSpec).Return(nil).Twice()
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	runner.On("Create", mock.Anything, deployed(demoSpec), demoRestoredVolumes).Run(liveErr(&revertErrs)).Return(nil).Once()
	runner.On("Start", mock.Anything, deployed(demoSpec), 60*time.Second).Run(liveErr(&revertErrs)).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).
		Run(func(mock.Arguments) { cancel() }).Return(context.Canceled).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Run(liveErr(&revertErrs)).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	inflight := &fakeInflight{}
	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         inflight,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, revertErrs, 3, "create, restore and start of the revert")
	for _, err := range revertErrs {
		assert.NoError(t, err, "the revert and its cleanup must run on a live context")
	}
	_, found, _ := inflight.Get(context.Background())
	assert.False(t, found, "a reverted rollback clears its marker")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// An interrupt during the stop or the safety snapshot changes nothing: the services
// come back on a live context, and nothing is torn down or restored.
func TestService_Rollback_InterruptBeforeTheTeardownRestarts(t *testing.T) {
	tests := []struct {
		name string
		// stopErr fails the stop itself; with none, the interrupt lands during the
		// safety snapshot, which still commits.
		stopErr      error
		wantErr      string
		wantSnapshot bool
	}{
		{
			name:    "stop fails partway",
			stopErr: context.Canceled,
			wantErr: "stop services: interrupted: context canceled",
		},
		{
			name:         "interrupt during the safety snapshot",
			wantErr:      "rollback interrupted, nothing changed: context canceled",
			wantSnapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &syncBuffer{}
			ctx, cancel := context.WithCancel(zerolog.New(logs).WithContext(context.Background()))
			defer cancel()

			vol := demoVolume()
			record := demoRecord()
			repo, inspector, locker := demoResolution(record)

			var startErr error
			runner := &mockComposeRunner{}
			stop := runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), tt.stopErr).Once()
			if tt.stopErr != nil {
				stop.Run(func(mock.Arguments) { cancel() })
			}
			runner.On("Start", mock.Anything, demoSpec, 60*time.Second).
				Run(func(args mock.Arguments) { startErr = args.Get(0).(context.Context).Err() }).
				Return(nil).Once()

			savepointBackend := &mockBackend{volume: vol}
			safetySnapshotBackend := &mockBackend{volume: vol}
			if tt.wantSnapshot {
				expectSpecCommit(repo)
				expectSafetySnapshotCommit(repo, "019e843e-1650-7000-8000-000000000000")
				safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
					Run(func(mock.Arguments) { cancel() }).
					Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy, Payload: "safety-payload"}, nil).
					Once()
			}

			registry := &mockRegistry{}
			store := newDirStore(t)
			registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
			registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

			inflight := &fakeInflight{}
			svc := New(Deps{
				SessionLocker:    fakeSessionLocker{},
				VolumeRemover:    &mockVolumeRemover{},
				SavepointRepo:    repo,
				ProjectLocker:    locker,
				ComposeInspector: inspector,
				ProjectAssembler: demoAssembler(),
				SpecCodec:        demoCodec(),
				ComposeRunner:    runner,
				BackendRegistry:  registry,
				Inflight:         inflight,
				Store:            store,
				CaptureDefaults:  demoCaptureDefaults,
			})

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{
				ID:          ptrID("019e843e-1650-7000-8000-000000000000"),
				WaitTimeout: 60 * time.Second,
			})

			// Assert
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NoError(t, startErr, "the restart must run on a live context")
			_, found, _ := inflight.Get(context.Background())
			assert.False(t, found, "nothing was torn down, so no marker is written")
			if tt.wantSnapshot {
				assert.Contains(t, logs.String(), "safety_snapshot_id", "the snapshot it stopped short of using is named")
			}
			// No Down, Create or Restore is declared: any would fail the run.
			repo.AssertExpectations(t)
			inspector.AssertExpectations(t)
			locker.AssertExpectations(t)
			runner.AssertExpectations(t)
			registry.AssertExpectations(t)
			savepointBackend.AssertExpectations(t)
			safetySnapshotBackend.AssertExpectations(t)
		})
	}
}

// noSnapshotDoubles are the doubles a rollback with no safety snapshot runs on.
type noSnapshotDoubles struct {
	store         storages.Storager
	repo          *mockSavepointRepo
	locker        *mockLocker
	inspector     *mockInspector
	runner        *mockComposeRunner
	registry      *mockRegistry
	backend       *mockBackend
	volumeRemover *mockVolumeRemover
}

// assert closes every double.
func (d noSnapshotDoubles) assert(t *testing.T) {
	t.Helper()
	d.repo.AssertExpectations(t)
	d.locker.AssertExpectations(t)
	d.inspector.AssertExpectations(t)
	d.runner.AssertExpectations(t)
	d.registry.AssertExpectations(t)
	d.backend.AssertExpectations(t)
	d.volumeRemover.AssertExpectations(t)
}

// newNoSnapshotService is a service over a running compose project and a record it may restore.
// Only the savepoint's backend is built: a SaveSavepoint or a DeleteSavepoint on the repository has
// no expectation and fails the run.
func newNoSnapshotService(t *testing.T, record model.Savepoint, inflight inflightMarkers) (*Service, noSnapshotDoubles) {
	t.Helper()
	vol := record.Stats[0].Volume
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	store := newDirStore(t)
	backend := &mockBackend{volume: vol}
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()
	doubles := noSnapshotDoubles{
		store:         store,
		repo:          repo,
		locker:        locker,
		inspector:     &mockInspector{},
		runner:        &mockComposeRunner{},
		registry:      registry,
		backend:       backend,
		volumeRemover: &mockVolumeRemover{},
	}
	return New(Deps{
		SessionLocker:    fakeSessionLocker{},
		VolumeRemover:    doubles.volumeRemover,
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: doubles.inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    doubles.runner,
		BackendRegistry:  registry,
		Inflight:         inflight,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	}), doubles
}

// renderBoth hands every event to two renderers.
type renderBoth [2]progress.Renderer

func (r renderBoth) Render(evt progress.Event) {
	r[0].Render(evt)
	r[1].Render(evt)
}

// newRestoreOf is the request a restore of record's one volume sends.
func newRestoreOf(record model.Savepoint) model.BackendRestoreRequest {
	return model.BackendRestoreRequest{
		SnapshotID: record.ID,
		Payload:    record.Stats[0].Payload,
		SizeBytes:  record.Stats[0].SizeBytes,
	}
}

// isInflightMarker matches a marker of phase that names no safety snapshot.
func isInflightMarker(target model.SavepointID, phase model.InflightPhase) any {
	return isInflightMarkerOf(target, phase, "")
}

// isInflightMarkerOf matches a marker naming safety as its safety snapshot.
func isInflightMarkerOf(target model.SavepointID, phase model.InflightPhase, safety model.SavepointID) any {
	return mock.MatchedBy(func(marker model.InflightMarker) bool {
		return marker.Target == target && marker.Phase == phase && marker.Safety == safety
	})
}

// A request skipping the safety snapshot warns before anything is written and needs nothing of the
// live project: it goes straight to the marker and the teardown, by the recorded spec, and opens no
// step of a snapshot. The marker names none, and neither does the result. A marker that cannot be
// cleared does not fail it.
func TestService_Rollback_SkipsTheSafetySnapshot(t *testing.T) {
	tests := []struct {
		name     string
		clearErr error
	}{
		{name: "the marker is cleared"},
		{name: "a marker that cannot be cleared does not fail it", clearErr: errors.New("disk full")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			inflight := &mockInflight{}
			svc, doubles := newNoSnapshotService(t, record, inflight)
			doubles.inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
			recorder := &progressRecorder{paths: map[progress.StepID]string{}}
			warned := &warnedRecorder{}
			now := func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) }
			ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()),
				progress.New(renderBoth{recorder, warned}, now))
			var warnedBeforeMarker int
			gate := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
			putRecreating := inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRecreating)).
				Run(func(mock.Arguments) { warnedBeforeMarker = len(warned.msgs) }).Return(nil).Once()
			down := doubles.runner.On("Down", mock.Anything, demoTargetSpec).Return(nil).Once()
			create := doubles.runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
			putRestoring := inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoring)).Return(nil).Once()
			restore := doubles.backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
			putDone := inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoreDone)).Return(nil).Once()
			start := doubles.runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once()
			clearMarker := inflight.On("Clear", mock.Anything).Return(tt.clearErr).Once()
			mock.InOrder(gate, putRecreating, down, create, putRestoring, restore, putDone, start, clearMarker)

			// Act
			res, err := svc.Rollback(ctx, model.RollbackRequest{
				ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
			})

			// Assert
			require.NoError(t, err)
			assert.Empty(t, res.SafetySnapshot)
			assert.Equal(t, []model.Volume{record.Stats[0].Volume}, res.Restored)
			for _, line := range recorder.ended {
				assert.NotContains(t, line, string(kindRollbackSafetySnapshot))
			}
			assert.Equal(t, 1, warnedBeforeMarker, "the warning comes before anything is written")
			assert.Equal(t, []string{noSafetySnapshotWarning}, warned.msgs)
			assert.Equal(t, []bool{true}, warned.inResult, "the result says so too: it names no safety snapshot")
			inflight.AssertExpectations(t)
			doubles.assert(t)
		})
	}
}

// With nothing to replay, any failure past the marker, an interrupt included, cleans nothing up and
// starts nothing: the marker stands in its phase, and the same rollback again recovers.
func TestService_Rollback_NoSafetySnapshotFailureKeepsTheMarker(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name                                     string
		downErr, createErr, restoreErr, startErr error
		// interrupt cancels the run during the restore.
		interrupt bool
		// cause is what the interrupt cancels with.
		cause     error
		wantErr   error
		wantPhase model.InflightPhase
	}{
		{name: "a failed down", downErr: boom, wantErr: boom, wantPhase: model.InflightRecreating},
		{name: "a failed create", createErr: boom, wantErr: boom, wantPhase: model.InflightRecreating},
		{name: "a failed restore", restoreErr: boom, wantErr: boom, wantPhase: model.InflightRestoring},
		{name: "a failed start", startErr: boom, wantErr: boom, wantPhase: model.InflightRestoreDone},
		{
			name:       "an interrupted restore",
			restoreErr: context.Canceled,
			interrupt:  true,
			wantErr:    context.Canceled,
			wantPhase:  model.InflightRestoring,
		},
		{
			name:       "a session lock lost during the restore",
			restoreErr: context.Canceled,
			interrupt:  true,
			cause:      model.ErrSessionLockLost,
			wantErr:    model.ErrSessionLockLost,
			wantPhase:  model.InflightRestoring,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			inflight := &fakeInflight{}
			svc, doubles := newNoSnapshotService(t, record, inflight)
			ctx, cancel := context.WithCancelCause(zerolog.Nop().WithContext(context.Background()))
			defer cancel(nil)
			calls := []*mock.Call{
				doubles.inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once(),
				doubles.runner.On("Down", mock.Anything, demoTargetSpec).Return(tt.downErr).Once(),
			}
			if tt.downErr == nil {
				calls = append(calls, doubles.runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(tt.createErr).Once())
			}
			if tt.downErr == nil && tt.createErr == nil {
				restore := doubles.backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(tt.restoreErr).Once()
				if tt.interrupt {
					restore.Run(func(mock.Arguments) { cancel(tt.cause) })
				}
				calls = append(calls, restore)
			}
			if tt.downErr == nil && tt.createErr == nil && tt.restoreErr == nil {
				calls = append(calls, doubles.runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(tt.startErr).Once())
			}
			mock.InOrder(calls...)

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{
				ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
			})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			marker, marked, getErr := inflight.Get(context.Background())
			require.NoError(t, getErr)
			require.True(t, marked)
			assert.Equal(t, tt.wantPhase, marker.Phase)
			assert.Equal(t, record.ID, marker.RecoveryID(), "running the same rollback again recovers")
			doubles.assert(t)
		})
	}
}

// With no snapshot too, the volumes the teardown orphaned are reaped and the marker cleared once
// the start has passed, whatever interrupt lands then: the rollback has succeeded.
func TestService_Rollback_NoSafetySnapshotCleansUpPastAnInterrupt(t *testing.T) {
	// Arrange
	anonVol := model.Volume{ObjectID: "volume-anon-app-1-scratch", Backend: model.SnapshotBackendFullCopy}
	record := demoRecord()
	record.Stats = []model.SnapshotStat{{Volume: anonVol, Type: model.SnapshotBackendFullCopy, SizeBytes: demoVolumeSize, Payload: "sp-payload"}}
	inflight := &fakeInflight{}
	svc, doubles := newNoSnapshotService(t, record, inflight)
	doubles.inspector.On("Introspect", mock.Anything).Return(model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: "app",
			Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/scratch", VolumeName: "9f8e7d", Replica: 1},
			},
			Containers: []model.Container{{Name: "demo-app-1", Number: 1}},
		}},
	}}, nil).Once()
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	notCancelled := mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil })
	doubles.runner.On("Down", mock.Anything, demoTargetSpec).Return(nil).Once()
	doubles.runner.On("Create", mock.Anything, deployed(demoTargetSpec), []string(nil)).Return(nil).Once()
	doubles.backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
	start := doubles.runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).
		Run(func(mock.Arguments) { cancel() }).Return(nil).Once()
	remove := doubles.volumeRemover.On("RemoveVolume", notCancelled, "9f8e7d").Return(nil).Once()
	mock.InOrder(start, remove)

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
	})

	// Assert
	require.NoError(t, err)
	_, marked, getErr := inflight.Get(context.Background())
	require.NoError(t, getErr)
	assert.False(t, marked, "an interrupt past the start strands no marker")
	doubles.assert(t)
}

// A rollback with no snapshot that fails past the teardown leaves no containers: the same rollback
// again takes the cold start, is not refused over the volumes, and clears the marker.
func TestService_Rollback_NoSafetySnapshotFailureRecoversAsAColdStart(t *testing.T) {
	// Arrange
	record := demoRecord()
	inflight := &fakeInflight{}
	svc, doubles := newNoSnapshotService(t, record, inflight)
	doubles.repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	doubles.repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
	expectLock(doubles.locker)
	doubles.registry.On("New", matchSpec(doubles.store, record.Stats[0].Volume.ObjectID)).Return(doubles.backend, nil).Once()
	mock.InOrder(
		doubles.inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once(),
		doubles.runner.On("Down", mock.Anything, demoTargetSpec).Return(nil).Once(),
		doubles.runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(errors.New("create boom")).Once(),
		doubles.inspector.On("Introspect", mock.Anything).Return(demoAbsentIntrospection(), nil).Once(),
		doubles.runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once(),
		doubles.backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once(),
		doubles.runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once(),
	)
	ctx := zerolog.Nop().WithContext(context.Background())
	req := model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true, LeftoverVolumes: model.LeftoverVolumesRefuse,
	}

	// Act
	_, failed := svc.Rollback(ctx, req)
	interrupted, markedAfterFailure, _ := inflight.Get(ctx)
	_, err := svc.Rollback(ctx, req)

	// Assert
	require.ErrorContains(t, failed, "create boom")
	require.True(t, markedAfterFailure)
	assert.Equal(t, model.InflightRecreating, interrupted.Phase)
	require.NoError(t, err)
	_, marked, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	assert.False(t, marked)
	doubles.assert(t)
}

// A marker that cannot be written stops the rollback before it touches the project.
func TestService_Rollback_NoSafetySnapshotMarkerFailureTouchesNothing(t *testing.T) {
	// Arrange
	record := demoRecord()
	inflight := &mockInflight{}
	svc, doubles := newNoSnapshotService(t, record, inflight)
	doubles.inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRecreating)).Return(errors.New("disk full")).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
	})

	// Assert
	require.ErrorContains(t, err, "recording the inflight marker, nothing changed: disk full")
	inflight.AssertExpectations(t)
	doubles.assert(t)
}

// A rollback killed partway left its containers behind: the recovery, to its own target when it took
// no snapshot and to the snapshot when it took one, takes none of the half-restored state, whatever
// the request says, and opens by saying volumes may have been written.
func TestService_Rollback_RecoveringLiveTakesNoSafetySnapshot(t *testing.T) {
	tests := []struct {
		name        string
		interrupted model.InflightPhase
		// toSafety recovers to the safety snapshot the marker names, not to its target.
		toSafety bool
	}{
		{name: "killed before a write", interrupted: model.InflightRecreating},
		{name: "killed mid-restore", interrupted: model.InflightRestoring},
		{name: "killed after the restore", interrupted: model.InflightRestoreDone},
		{name: "killed mid-restore, to its safety snapshot", interrupted: model.InflightRestoring, toSafety: true},
		{name: "a failed revert, to its safety snapshot", interrupted: model.InflightInconsistent, toSafety: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			inflight := &mockInflight{}
			svc, doubles := newNoSnapshotService(t, record, inflight)
			doubles.inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
			standing := newRecoveredMarker(record.ID, tt.interrupted, tt.toSafety)
			gate := inflight.On("Get", mock.Anything).Return(standing, true, nil).Once()
			opening := inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, standing.Safety)).Return(nil).Once()
			down := doubles.runner.On("Down", mock.Anything, demoTargetSpec).Return(nil).Once()
			create := doubles.runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
			restoring := inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, standing.Safety)).Return(nil).Once()
			restore := doubles.backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
			done := inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoreDone, standing.Safety)).Return(nil).Once()
			start := doubles.runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once()
			clearMarker := inflight.On("Clear", mock.Anything).Return(nil).Once()
			mock.InOrder(gate, opening, down, create, restoring, restore, done, start, clearMarker)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.NoError(t, err)
			assert.Empty(t, res.SafetySnapshot)
			inflight.AssertExpectations(t)
			doubles.assert(t)
		})
	}
}

// A marker naming no safety snapshot protects no earlier state, so a savepoint that will not come up
// holds nothing hostage: a rollback to another one recovers, takes no snapshot of the half-restored
// state, and clears the marker.
func TestService_Rollback_NoSnapshotMarkerRecoversToAnotherSavepoint(t *testing.T) {
	// Arrange
	record := demoRecord()
	inflight := &fakeInflight{}
	require.NoError(t, inflight.Put(context.Background(), model.InflightMarker{
		Phase:   model.InflightRestoring,
		Session: model.DefaultSession,
		Target:  demoSafetySnapshot().ID,
	}))
	svc, doubles := newNoSnapshotService(t, record, inflight)
	var opening model.InflightMarker
	doubles.inspector.On("Introspect", mock.Anything).
		Run(func(mock.Arguments) { opening, _, _ = inflight.Get(context.Background()) }).
		Return(demoIntrospection(), nil).Once()
	var marked model.InflightMarker
	down := doubles.runner.On("Down", mock.Anything, demoTargetSpec).
		Run(func(mock.Arguments) { marked, _, _ = inflight.Get(context.Background()) }).Return(nil).Once()
	create := doubles.runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	restore := doubles.backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
	start := doubles.runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once()
	mock.InOrder(down, create, restore, start)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, res.SafetySnapshot)
	assert.Equal(t, demoSafetySnapshot().ID, opening.Target)
	assert.Equal(t, record.ID, marked.Target, "a failure from here recovers through this savepoint")
	assert.Equal(t, model.InflightRestoring, marked.Phase)
	_, standing, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	assert.False(t, standing)
	doubles.assert(t)
}

// A failed recovery to the safety snapshot keeps naming it, and frees the gate: a snapshot that
// cannot come up must hold nothing hostage, and the hint must not deny that it exists.
func TestService_Rollback_FailedRecoveryKeepsTheSafetySnapshot(t *testing.T) {
	// Arrange
	record := demoRecord()
	standing := newRecoveredMarker(record.ID, model.InflightInconsistent, true)
	inflight := &fakeInflight{}
	require.NoError(t, inflight.Put(context.Background(), standing))
	svc, doubles := newNoSnapshotService(t, record, inflight)
	require.False(t, svc.isRecoveryRollback(standing, demoSafetySnapshot().ID), "before it, only the snapshot recovers")
	doubles.inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
	doubles.runner.On("Down", mock.Anything, demoTargetSpec).Return(errors.New("down failed")).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorContains(t, err, "down failed")
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
	doubles.assert(t)
}

// An interrupt that lands during the prepare writes no marker and stops nothing: the empty runner
// fails on a Down.
func TestService_Rollback_NoSafetySnapshotInterruptBeforeTheMarker(t *testing.T) {
	// Arrange
	record := demoRecord()
	inflight := &fakeInflight{}
	svc, doubles := newNoSnapshotService(t, record, inflight)
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	doubles.inspector.On("Introspect", mock.Anything).Run(func(mock.Arguments) { cancel() }).
		Return(demoIntrospection(), nil).Once()

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
	})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "nothing changed")
	_, found, getErr := inflight.Get(context.Background())
	require.NoError(t, getErr)
	assert.False(t, found)
	doubles.assert(t)
}

// newRecoveredMarker is the marker a rollback to id recovers from: naming id as its safety snapshot
// under toSafety, as its target otherwise.
func newRecoveredMarker(id model.SavepointID, phase model.InflightPhase, toSafety bool) model.InflightMarker {
	marker := model.InflightMarker{
		Phase:   phase,
		Session: model.DefaultSession,
		Target:  id,
		At:      time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
	}
	if toSafety {
		marker.Target = demoSafetySnapshot().ID
		marker.Safety = id
	}
	return marker
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
