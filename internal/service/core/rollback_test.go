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
	existsSp := savepointBackend.On("Exists", mock.Anything,
		model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	commitSafety := expectSafetySnapshotCommit(repo, "1780335122")
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	up := runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// The whole workflow: pre-flight → stop → back up the live state and commit its
	// record → restore → start → uncommit the backup and drop it. Every neighbouring
	// pair compiles when swapped, so the chain is the pin — the pre-flight ahead of
	// the stop is what leaves the project running when a capture is missing, the
	// commit after the capture is what makes the record a commit marker, and the
	// uncommit before the drop is what keeps a half-gone backup from listing.
	mock.InOrder(existsSp, stop, snapshotSafety, commitSafety, restoreSp, up, uncommitSafety, removeSafety)

	// Twice over the one store: one backend per volume per set, and no third build.
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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
	existsSp := savepointBackend.On("Exists", mock.Anything,
		model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	runner := &mockComposeRunner{}
	stopBeforeBackup := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	commitSafety := expectSafetySnapshotCommit(repo, "1780335122")
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()
	// The revert replays the backup under the id its capture reported, and a
	// successful revert already returned the environment, so the backup goes too.
	stopForRevert := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	existsSafety := safetySnapshotBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(true, nil).Once()
	restoreSafety := safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	up := runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// Pre-flight → stop → backup and commit, then the failed restore and the revert:
	// stop again → its own pre-flight → replay → restart → uncommit and drop.
	mock.InOrder(existsSp, stopBeforeBackup, snapshotSafety, commitSafety, restoreSp,
		stopForRevert, existsSafety, restoreSafety, up, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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

// Services that never come up healthy revert too: an unhealthy stack after a
// clean restore is still a failed rollback.
func TestService_Rollback_RevertsOnUnhealthy(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	// The backup commits its record and, once the revert has returned the
	// environment, uncommits it again.
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	// The first Up is the failed health check; the second is the revert's restart.
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(errors.New("unhealthy")).Once()
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(true, nil).Once()
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

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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
	expectSafetySnapshotCommit(repo, "1780335122")
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(true, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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

// A failed safety snapshot changes nothing: the partial backup is dropped and the
// services restarted, with live data never touched.
func TestService_Rollback_FailedSafetySnapshotAbortsAndRestarts(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	// The capture never got to commit, so repo carries no Save — only the uncommit
	// the drop opens with, which is a no-op against a record that was never written.
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{}, errors.New("backup boom")).Once()
	// The partial backup is dropped, carrying the nil payload the failed
	// capture reported.
	safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == nil
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"pre-rollback safety snapshot failed, nothing changed: snapshot volume-named-demo_db: capture volume: backup boom")
	// Nothing may be restored over live data: savepointBackend carries no Restore
	// expectation, so a restore would fail the run where it happened.
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// A missing capture aborts before anything is touched: nothing stopped, backed
// up or restored.
func TestService_Rollback_MissingCapture(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)
	runner := &mockComposeRunner{}

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(false, nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.ErrorIs(t, err, model.ErrMissingVolumeInSnapshot)
	assert.Contains(t, logs.String(), "no capture exists for these volumes")
	assert.Contains(t, logs.String(), `"object_id":"volume-named-demo_db"`)
	// The bracket is closed even on the abort path: its Release expectation is Once().
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
}

// A half-revert would leave a mix of savepoint and live data, so a missing
// capture reverts nothing and keeps the backup.
func TestService_Rollback_RevertGatedOnACompleteBackup(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	expectSafetySnapshotCommit(repo, "1780335122")
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	// The revert's own pre-flight finds the backup incomplete.
	safetySnapshotBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(false, nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	// A partial revert is worse than none, and the backup is kept for a manual
	// recovery — record and all: safetySnapshotBackend carries neither a Restore nor a
	// Remove expectation, and repo no Delete.
	require.Error(t, err)
	assert.Contains(t, logs.String(), "safety snapshot incomplete, nothing reverted")
	assert.Contains(t, logs.String(), "no capture exists for these volumes")
	assert.Contains(t, logs.String(), "safety snapshot retained")
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
	require.NoError(t, repo.Save(context.Background(), record))

	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	var safetySnapshotID model.SavepointID
	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(true, nil).Once()
	// The revert fails, which is the one path that keeps the backup.
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req model.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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

	var safetySnapshotID model.SavepointID
	var upCtxs []context.Context
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
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

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Exists", mock.Anything, mock.MatchedBy(func(req model.BackendExistsRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(true, nil).Once()
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

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	require.Len(t, upCtxs, 2)
	assert.NoError(t, upCtxs[1].Err(), "revert must restart services with a live context, not the cancelled errgroup one")
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
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Exists", mock.Anything, model.BackendExistsRequest{SnapshotID: record.ID}).Return(true, nil).Once()
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

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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

// The id gate stands ahead of the dispatch: an id that could name a parent
// directory reaches no backend.
func TestService_Rollback_RejectsAnUnsafeIDBeforeAnyBackend(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	record.ID = "../../etc"
	repo, inspector, locker := demoResolution(record)
	runner := &mockComposeRunner{}

	// Neither backend carries an expectation: any dispatch fails the test loudly.
	savepointBackend := &mockBackend{volume: vol}
	safetySnapshotBackend := &mockBackend{volume: vol}
	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("../../etc"),
		WaitTimeout:  time.Second,
	})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSavepointID)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
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
	}
	store := newDirStore(t)
	repo := savepointrepo.New(store)
	require.NoError(t, repo.Save(context.Background(), record))
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil)

	docker := &fakeVolumeStreamer{payload: "live bytes"}
	registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(docker))
	require.NoError(t, err)
	svc := New(repo, locker, inspector, runner, registry, store, model.SnapshotBackendCopy)

	// The capture the pre-flight looks for, at the key the backend renders.
	archive := "1780335122/volume-named-demo_db/data.tar"
	require.NoError(t, store.PutObject(context.Background(), archive, strings.NewReader("savepoint bytes")))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Equal(t, "savepoint bytes", docker.written.String(), "the archive's stream is what reaches the mount")
	assert.ElementsMatch(t, []string{archive, "1780335122/savepoint.json"}, objectNames(t, store),
		"the savepoint is read, never rewritten, and the backup's own tree is swept whole")
	committed, err := repo.List(ctx)
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
