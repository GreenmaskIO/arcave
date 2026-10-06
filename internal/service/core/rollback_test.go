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

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	savepointrepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
)

func TestService_Rollback_RestoresAndDropsTheSafetySnapshot(t *testing.T) {
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
	stop := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, "1780335122")
	// The teardown takes the live compose project down; the rebuild puts back the recorded one.
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	up := runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// The whole workflow: stop → back up the live state, write its spec and commit
	// its record → tear the compose project down → rebuild it from the savepoint's spec →
	// restore → start → uncommit the backup and drop it. Every neighbouring pair
	// compiles when swapped, so the chain is the pin.
	mock.InOrder(stop, snapshotSafety, specSafety, commitSafety, down, create,
		restoreSp, up, uncommitSafety, removeSafety)

	// Twice over the one store: one backend per volume per set, and no third build.
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, record, res.Savepoint)

	// The safety snapshot was taken under the id the run created, committed under it
	// and dropped under it. Nothing reverts, and the savepoint's own record is
	// never rewritten — neither carries an expectation above, so either would have
	// failed the run where it happened.
	require.NoError(t, safetySnapshotID.Validate())
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
	stopBeforeBackup := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, "1780335122")
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()
	// The revert rebuilds the compose project the backup was taken of and replays it under the
	// id its capture reported; a successful revert drops the backup too.
	downForRevert := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	createForRevert := runner.On("Create", mock.Anything, demoSpec).Return(nil).Once()
	restoreSafety := safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	up := runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// Stop → backup, spec and commit → tear down and rebuild → the failed restore,
	// then the revert: tear down and rebuild again → replay → restart → uncommit and drop.
	mock.InOrder(stopBeforeBackup, snapshotSafety, specSafety, commitSafety, down, create, restoreSp,
		downForRevert, createForRevert, restoreSafety, up, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
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
	// The backup commits its record and, once the revert has returned the
	// environment, uncommits it again.
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	// The rollback rebuilds the savepoint's compose project; the revert rebuilds the live one.
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	runner.On("Create", mock.Anything, demoSpec).Return(nil).Once()
	// The first start is the failed health check; the second is the revert's restart.
	runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(errors.New("unhealthy")).Once()
	runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
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
	expectSafetySnapshotCommit(repo, "1780335122")
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	runner.On("Create", mock.Anything, demoSpec).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed: restore volume-named-demo_db: restore capture: restore boom")
	// The backup is retained, captures and record alike: safetySnapshotBackend carries no
	// Remove expectation and repo no Delete, so either would fail the run.
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
	runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Return(model.SnapshotStat{}, errors.New("backup boom")).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"pre-rollback safety snapshot failed, nothing changed: snapshot volume-named-demo_db: capture volume: backup boom")
	// Nothing may be restored over live data, and the partial capture is not deleted:
	// no Restore on savepointBackend, no Remove on safetySnapshotBackend, no Delete on
	// repo — each would fail the run as an unexpected call.
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
	runner.On("Stop", mock.Anything, demoSpec).Return(nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	runner.On("Create", mock.Anything, demoSpec).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	var safetySnapshotID model.SavepointID
	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	// The revert fails, which is the one path that keeps the backup.
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
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
	assert.Equal(t, "safety-snapshot-1780335122", backup.Name,
		"the name says what the killed rollback was undoing")
	assert.Equal(t, map[string]string{model.SafetySnapshotLabel: model.SafetySnapshotLabelValue}, backup.Labels)
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
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	captureCtx := func(args mock.Arguments) { upErrs = append(upErrs, args.Get(0).(context.Context).Err()) }
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	runner.On("Create", mock.Anything, demoSpec).Return(nil).Once()
	// The first start fails the health check, which drives Rollback into revert; the
	// second is revert's own restart, the one under test.
	runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Run(captureCtx).
		Return(errors.New("unhealthy")).Once()
	runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Run(captureCtx).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
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
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil)
	runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(), `"volume":"volume-named-demo_db"`)
	assert.Contains(t, logs.String(), `"backend":"copy"`)
	assert.Contains(t, logs.String(), `"snapshot_id":"1780335122"`)
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
		DefaultBackend:   model.SnapshotBackendCopy,
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
		Backend:        model.SnapshotBackendCopy,
		Named: &model.VolumePropertiesNamed{VolumeName: "demo_db",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}},
	}
	record := model.Savepoint{
		ID:        "1780335122",
		Name:      "sp",
		CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC),
		Stats:     []model.SnapshotStat{{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "sp-payload"}},
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
	runner.On("Stop", mock.Anything, demoSpec).Return(nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil)
	runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(nil)

	docker := &fakeVolumeStreamer{payload: "live bytes"}
	registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(docker))
	require.NoError(t, err)
	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})

	// The capture the restore reads, at the key the backend renders.
	archive := "1780335122/volume-named-demo_db/data.tar"
	require.NoError(t, store.PutObject(context.Background(), archive, strings.NewReader("savepoint bytes")))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID("1780335122"),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, "savepoint bytes", docker.written.String(), "the archive's stream is what reaches the mount")
	assert.ElementsMatch(t, []string{archive, "1780335122/savepoint.json", "1780335122/project.yaml"}, objectNames(t, store),
		"the savepoint is read, never rewritten, and the backup's own tree is swept whole")
	committed, err := repo.ListSavepoints(ctx)
	require.NoError(t, err)
	assert.Equal(t, []model.Savepoint{record}, committed,
		"the backup is uncommitted on success, so nothing of it lists — not even with the flag")
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

// TestVolumesOf pins the projection, and that it is never nil — restored must
// marshal as [], not null.
func TestVolumesOf(t *testing.T) {
	vol := model.Volume{ObjectID: "volume-named-demo_db", Backend: model.SnapshotBackendCopy}

	tests := []struct {
		name   string
		record model.Savepoint
		want   []model.Volume
	}{
		{
			name:   "a stat per volume yields a volume per stat",
			record: model.Savepoint{ID: "100", Stats: []model.SnapshotStat{{Volume: vol, Type: model.SnapshotBackendCopy}}},
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
	registry.On("New", mock.Anything).Return(&mockBackend{volume: demoVolume()}, nil).Once()
	inspector := &mockInspector{}
	runner := &mockComposeRunner{}
	svc := New(Deps{
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID)})

	// Assert: nothing was introspected and nothing was stopped — both mocks close empty.
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
	anonVol := model.Volume{ObjectID: "volume-service-app-scratch", Backend: model.SnapshotBackendCopy}
	res := model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: "app",
			Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/scratch", VolumeName: "9f8e7d"},
			},
			Containers: []model.Container{{Name: "demo-app-1"}},
		}},
	}}
	record := demoRecord()
	record.Stats = []model.SnapshotStat{{Volume: anonVol, Type: model.SnapshotBackendCopy, Payload: "sp-payload"}}

	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil)
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, record.ID)
	var safetySnapshotID model.SavepointID
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(res, nil)
	locker := &mockLocker{}
	expectLock(locker)

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil)
	runner.On("Down", mock.Anything, demoSpec).Return(nil)
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil)
	runner.On("Start", mock.Anything, demoTargetSpec, time.Duration(0)).Return(nil)

	savepointBackend := &mockBackend{volume: anonVol}
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: anonVol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: anonVol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Remove", mock.Anything, mock.Anything).Return(nil).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, anonVol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, anonVol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	// The docker name the mount had before the teardown: the only record of what to reap.
	remover := &mockVolumeRemover{}
	remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(nil).Once()

	// The safety snapshot commits the service's layer like any other savepoint.
	capturer := &mockImageBackend{}
	capturer.On("Capture", mock.Anything, mock.Anything).Return(model.ImageCapture{Service: "app"}, nil).Once()

	svc := New(Deps{
		VolumeRemover:    remover,
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(res),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		ImageBackend:     capturer,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
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
