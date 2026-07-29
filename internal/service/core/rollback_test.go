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
	"github.com/greenmaskio/arcave/internal/service/backend/tarcopy"
)

func TestService_Rollback_RestoresAndDropsTheSafetyBackup(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	// The expectations are declared in workflow order and the InOrder below pins
	// it. The safety tier's calls all follow the Snapshot that fills safetyID; the
	// errgroup barrier between groups gives each matcher's read a happens-before.
	var safetyID model.SavepointID
	savepointBackend := &mockBackend{volume: vol}
	existsSp := savepointBackend.On("Exists", mock.Anything,
		model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	safetyBackend := &mockBackend{volume: vol}
	snapshotSafety := safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetyRepo := &mockSavepointRepo{}
	saveSafety := safetyRepo.On("Save", mock.Anything, matchSafetyRecord()).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	up := runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()
	deleteSafety := safetyRepo.On("Delete", mock.Anything,
		mock.MatchedBy(func(id model.SavepointID) bool { return id == safetyID })).Return(nil).Once()
	removeSafety := safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// The whole workflow: pre-flight → stop → backup (capture, then commit) →
	// restore → start → drop the backup uncommit-first. Every neighbouring pair
	// compiles when swapped, so the chain is the pin.
	mock.InOrder(existsSp, stop, snapshotSafety, saveSafety, restoreSp, up, deleteSafety, removeSafety)

	// Once per tier: one backend per volume per tier, and no third build.
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, record, res.Savepoint)

	// The safety backup was taken under the id the run created and removed under
	// the same one; nothing reverts, and the savepoint tier is never written to.
	require.NoError(t, safetyID.Validate())
	safetyBackend.AssertNotCalled(t, "Restore", mock.Anything, mock.Anything)
	savepointBackend.AssertNotCalled(t, "Snapshot", mock.Anything, mock.Anything)
	savepointBackend.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything,
		"a rollback never removes the savepoint it restored")
	assert.Equal(t, 1, locker.released, "the lock is released on the way out")
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
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
	var safetyID model.SavepointID
	savepointBackend := &mockBackend{volume: vol}
	existsSp := savepointBackend.On("Exists", mock.Anything,
		model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	runner := &mockComposeRunner{}
	stopBeforeBackup := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	safetyBackend := &mockBackend{volume: vol}
	snapshotSafety := safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetyRepo := &mockSavepointRepo{}
	saveSafety := safetyRepo.On("Save", mock.Anything, matchSafetyRecord()).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()
	// The revert replays the backup under the id its capture reported, and a
	// successful revert already returned the environment, so the backup goes too.
	stopForRevert := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	existsSafety := safetyBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetyID
	})).Return(true, nil).Once()
	restoreSafety := safetyBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	up := runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()
	deleteSafety := safetyRepo.On("Delete", mock.Anything,
		mock.MatchedBy(func(id model.SavepointID) bool { return id == safetyID })).Return(nil).Once()
	removeSafety := safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// Pre-flight → stop → backup → failed restore, then the revert: stop again →
	// its own pre-flight → replay → restart → drop the backup uncommit-first.
	mock.InOrder(existsSp, stopBeforeBackup, snapshotSafety, saveSafety, restoreSp,
		stopForRevert, existsSafety, restoreSafety, up, deleteSafety, removeSafety)

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed: restore volume/demo_db: restore capture: restore boom")
	assert.Contains(t, logs.String(), "reverted to the pre-rollback state")
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// Services that never come up healthy revert too: an unhealthy stack after a
// clean restore is still a failed rollback.
func TestService_Rollback_RevertsOnUnhealthy(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetyID model.SavepointID
	safetyRepo := &mockSavepointRepo{}
	safetyRepo.On("Save", mock.Anything, matchSafetyRecord()).Return(nil).Once()
	safetyRepo.On("Delete", mock.Anything,
		mock.MatchedBy(func(id model.SavepointID) bool { return id == safetyID })).Return(nil).Once()
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	// The first Up is the failed health check; the second is the revert's restart.
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(errors.New("unhealthy")).Once()
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetyBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetyID
	})).Return(true, nil).Once()
	safetyBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed: services did not become healthy after restore: unhealthy")
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// A failed revert keeps the safety backup: it is now the last copy of the
// pre-rollback state.
func TestService_Rollback_FailedRevertRetainsTheSafetyBackup(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetyID model.SavepointID
	safetyRepo := &mockSavepointRepo{}
	safetyRepo.On("Save", mock.Anything, matchSafetyRecord()).Return(nil).Once()
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetyBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetyID
	})).Return(true, nil).Once()
	safetyBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed: restore volume/demo_db: restore capture: restore boom")
	safetyBackend.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything,
		"the backup is retained, captures and all")
	safetyRepo.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
	assert.Contains(t, logs.String(), "safety backup retained")
	assert.Contains(t, logs.String(), safetyID.String())
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// A failed safety backup changes nothing: the partial backup is dropped and the
// services restarted, with live data never touched.
func TestService_Rollback_FailedSafetyBackupAbortsAndRestarts(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetyID model.SavepointID
	safetyRepo := &mockSavepointRepo{}
	safetyRepo.On("Delete", mock.Anything,
		mock.MatchedBy(func(id model.SavepointID) bool { return id == safetyID })).Return(nil).Once()
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()

	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{}, errors.New("backup boom")).Once()
	// The partial backup is dropped, carrying the nil payload the failed
	// capture reported.
	safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == nil
	})).Return(nil).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"pre-rollback safety backup failed, nothing changed: snapshot volume/demo_db: capture volume: backup boom")
	savepointBackend.AssertNotCalled(t, "Restore", mock.Anything, mock.Anything,
		"nothing may be restored over live data")
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// A missing capture aborts before anything is touched: nothing stopped, backed
// up or restored.
func TestService_Rollback_MissingCapture(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)
	safetyRepo := &mockSavepointRepo{}
	runner := &mockComposeRunner{}

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(false, nil).Once()
	safetyBackend := &mockBackend{volume: vol}

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.ErrorIs(t, err, model.ErrMissingVolumeInSnapshot)
	assert.Contains(t, logs.String(), "no capture exists for these volumes")
	assert.Contains(t, logs.String(), `"object_id":"volume/demo_db"`)
	runner.AssertNotCalled(t, "Stop", mock.Anything, mock.Anything)
	safetyBackend.AssertNotCalled(t, "Snapshot", mock.Anything, mock.Anything)
	savepointBackend.AssertNotCalled(t, "Restore", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// A half-revert would leave a mix of savepoint and live data, so a missing
// capture reverts nothing and keeps the backup.
func TestService_Rollback_RevertGatedOnACompleteBackup(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetyID model.SavepointID
	safetyRepo := &mockSavepointRepo{}
	safetyRepo.On("Save", mock.Anything, matchSafetyRecord()).Return(nil).Once()
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	// The revert's own pre-flight finds the backup incomplete.
	safetyBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetyID
	})).Return(false, nil).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	safetyBackend.AssertNotCalled(t, "Restore", mock.Anything, mock.Anything,
		"a partial revert is worse than none")
	safetyBackend.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything,
		"the backup is kept for a manual recovery")
	assert.Contains(t, logs.String(), "safety backup incomplete, nothing reverted")
	assert.Contains(t, logs.String(), "no capture exists for these volumes")
	assert.Contains(t, logs.String(), "safety backup retained")
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// A retained safety backup is one a human has to be able to find. Its id lives
// in this process's memory only until the process is gone, so the record —
// committed through the real repository into the real safety store — is what
// keeps the last copy of the pre-rollback state addressable.
func TestService_Rollback_RetainedBackupStaysAddressable(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)
	safetyStore := newDirStore(t)
	safetyRepo := savepointrepo.New(safetyStore)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	var safetyID model.SavepointID
	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetyBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetyID
	})).Return(true, nil).Once()
	safetyBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), safetyStore)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	backups, err := safetyRepo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, backups, 1)
	assert.Equal(t, safetyID, backups[0].ID, "the record names the id its captures were taken under")
	assert.Equal(t, safetyBackupName, backups[0].Name)
	require.Len(t, backups[0].Stats, 1)
	assert.Equal(t, "volume/demo_db", backups[0].Stats[0].Volume.ObjectID)
	assert.Contains(t, objectNames(t, safetyStore), safetyID.String()+"/savepoint.json",
		"the record is committed into the safety tree, beside the captures it names")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// The revert must restart with the caller's context: an errgroup cancels its
// derived one as soon as Wait returns.
func TestService_Rollback_RevertRestartsWithLiveContext(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetyID model.SavepointID
	safetyRepo := &mockSavepointRepo{}
	safetyRepo.On("Save", mock.Anything, matchSafetyRecord()).Return(nil)
	safetyRepo.On("Delete", mock.Anything,
		mock.MatchedBy(func(id model.SavepointID) bool { return id == safetyID })).Return(nil)

	var upCtxs []context.Context
	captureCtx := func(args mock.Arguments) { upCtxs = append(upCtxs, args.Get(0).(context.Context)) }
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	// The first Up fails the health check, which drives Rollback into revert; the
	// second Up is revert's own restart, the one under test.
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Run(captureCtx).
		Return(errors.New("unhealthy")).Once()
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Run(captureCtx).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetyBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetyID
	})).Return(true, nil).Once()
	safetyBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	require.Len(t, upCtxs, 2)
	assert.NoError(t, upCtxs[1].Err(), "revert must restart services with a live context, not the cancelled errgroup one")
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// Every line a volume's operation logs is attributed from its instance.
func TestService_Rollback_LogsAreAttributedPerVolume(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetyID model.SavepointID
	safetyRepo := &mockSavepointRepo{}
	safetyRepo.On("Save", mock.Anything, matchSafetyRecord()).Return(nil)
	safetyRepo.On("Delete", mock.Anything,
		mock.MatchedBy(func(id model.SavepointID) bool { return id == safetyID })).Return(nil)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { safetyID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetyID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(), `"volume":"volume/demo_db"`)
	assert.Contains(t, logs.String(), `"backend":"copy"`)
	assert.Contains(t, logs.String(), `"snapshot_id":"1780335122"`)
	assert.Contains(t, logs.String(), `"op":"restore"`)
	assert.Contains(t, logs.String(), `"snapshot_id":"`+safetyID.String()+`"`)
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// The id gate stands ahead of the dispatch: an id that could name a parent
// directory reaches no backend.
func TestService_Rollback_RejectsAnUnsafeIDBeforeAnyBackend(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	record.ID = "../../etc"
	repo, inspector, locker := demoResolution(record)
	safetyRepo := &mockSavepointRepo{}
	runner := &mockComposeRunner{}

	// Neither backend carries an expectation: any dispatch fails the test loudly.
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(model.BackendTierSafety, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(repo, safetyRepo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "../../etc",
		WaitTimeout:  time.Second,
	})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSavepointID)
	savepointBackend.AssertNotCalled(t, "Exists", mock.Anything, mock.Anything,
		"no backend may be dispatched on an id the gate refuses")
	runner.AssertNotCalled(t, "Stop", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
	safetyRepo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
}

// A rollback over real copy captures: the savepoint tree survives untouched,
// the safety tree is gone.
func TestService_Rollback_OverRealStores(t *testing.T) {
	// Arrange
	// The full literal stays inline: real tarcopy addressing needs the compose
	// project and the mount, not the trimmed mock-test form.
	vol := model.Volume{
		ObjectID:       "volume/demo_db",
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
	}
	repo, inspector, locker := demoResolution(record)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil)

	session, safety := newDirStore(t), newDirStore(t)
	docker := &fakeMountStreamer{payload: "live bytes"}
	registry, err := NewSnapshotBackendRegistry(tarcopy.NewFactory(docker))
	require.NoError(t, err)
	svc := New(repo, savepointrepo.New(safety), locker, inspector, runner, registry, session, safety)

	// The capture the pre-flight looks for, at the key the backend renders.
	archive := "1780335122/volume/demo_db/data.tar"
	require.NoError(t, session.PutObject(context.Background(), archive, strings.NewReader("savepoint bytes")))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		Ref:          "1780335122",
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, "savepoint bytes", docker.written.String(), "the archive's stream is what reaches the mount")
	assert.Equal(t, []string{archive}, objectNames(t, session), "the savepoint is read, never rewritten")
	assert.Empty(t, objectNames(t, safety),
		"the safety backup is dropped on success, its own directory swept with it")
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
}

// TestVolumesOf pins the projection, and that it is never nil — restored must
// marshal as [], not null.
func TestVolumesOf(t *testing.T) {
	vol := model.Volume{ObjectID: "volume/demo_db", Backend: model.SnapshotBackendCopy}

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
