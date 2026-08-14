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
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/fullcopy"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
	savepointrepo "github.com/greenmaskio/arcave/internal/provider/docker/internal/repository/savepoint"
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
	stop := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) {
			safetySnapshotID = args.Get(1).(dockermodel.BackendSnapshotRequest).SnapshotID
		}).
		Return(dockermodel.SnapshotStat{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	commitSafety := expectSafetySnapshotCommit(repo, "1780335122")
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		dockermodel.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	up := runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// The whole workflow: stop → back up the live state and commit its record →
	// restore → start → uncommit the backup and drop it. Every neighbouring pair
	// compiles when swapped, so the chain is the pin — the commit after the capture
	// is what makes the record a commit marker, and the uncommit before the drop is
	// what keeps a half-gone backup from listing.
	mock.InOrder(stop, snapshotSafety, commitSafety, restoreSp, up, uncommitSafety, removeSafety)

	// Twice over the one store: one backend per volume per set, and no third build.
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []dockermodel.Volume{vol}, res.Restored)
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
	stopBeforeBackup := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	safetySnapshotBackend := &mockBackend{volume: vol}
	snapshotSafety := safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) {
			safetySnapshotID = args.Get(1).(dockermodel.BackendSnapshotRequest).SnapshotID
		}).
		Return(dockermodel.SnapshotStat{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	commitSafety := expectSafetySnapshotCommit(repo, "1780335122")
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		dockermodel.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()
	// The revert replays the backup under the id its capture reported, and a
	// successful revert already returned the environment, so the backup goes too.
	stopForRevert := runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	restoreSafety := safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	up := runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	// Stop → backup and commit, then the failed restore and the revert:
	// stop again → replay → restart → uncommit and drop.
	mock.InOrder(stopBeforeBackup, snapshotSafety, commitSafety, restoreSp,
		stopForRevert, restoreSafety, up, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
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
	savepointBackend.On("Restore", mock.Anything,
		dockermodel.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) {
			safetySnapshotID = args.Get(1).(dockermodel.BackendSnapshotRequest).SnapshotID
		}).
		Return(dockermodel.SnapshotStat{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
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
	savepointBackend.On("Restore", mock.Anything,
		dockermodel.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) {
			safetySnapshotID = args.Get(1).(dockermodel.BackendSnapshotRequest).SnapshotID
		}).
		Return(dockermodel.SnapshotStat{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
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

// A failed safety snapshot changes nothing: the services are restarted, live data
// is never touched, and what the partial capture wrote is left for a gc.
func TestService_Rollback_FailedSafetySnapshotAbortsAndRestarts(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Return(dockermodel.SnapshotStat{}, errors.New("backup boom")).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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
	require.NoError(t, repo.Save(context.Background(), record))

	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		dockermodel.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("restore boom")).Once()

	var safetySnapshotID model.SavepointID
	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) {
			safetySnapshotID = args.Get(1).(dockermodel.BackendSnapshotRequest).SnapshotID
		}).
		Return(dockermodel.SnapshotStat{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	// The revert fails, which is the one path that keeps the backup.
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(errors.New("revert boom")).Once()

	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, logs.String(), "safety snapshot retained")
	assert.Contains(t, logs.String(), safetySnapshotID.String())

	// The record is in the store, beside the savepoint's own.
	committed, err := svc.ListSavepoints(ctx, dockermodel.ListRequest{IncludeSafetySnapshots: true})
	require.NoError(t, err)
	require.Len(t, committed, 2)
	backup, found := byID(committed, safetySnapshotID)
	require.True(t, found, "the retained safety snapshot lists with the flag")
	assert.True(t, backup.IsSafetySnapshot())
	assert.Equal(t, "safety-snapshot-1780335122", backup.Name,
		"the name says what the killed rollback was undoing")
	assert.Equal(t, map[string]string{model.SafetySnapshotLabel: model.SafetySnapshotLabelValue}, backup.Labels)
	assert.Equal(t, []dockermodel.Volume{vol}, volumesOf(backup),
		"and carries the stats a manual rollback to it would restore through")

	// And the label is what hides it: without the flag only the savepoint shows.
	visible, err := svc.ListSavepoints(ctx, dockermodel.ListRequest{})
	require.NoError(t, err)
	assert.Equal(t, []dockermodel.Savepoint{record}, visible)

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
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	captureCtx := func(args mock.Arguments) { upErrs = append(upErrs, args.Get(0).(context.Context).Err()) }
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	// The first Up fails the health check, which drives Rollback into revert; the
	// second Up is revert's own restart, the one under test.
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Run(captureCtx).
		Return(errors.New("unhealthy")).Once()
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Run(captureCtx).Return(nil).Once()

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		dockermodel.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) {
			safetySnapshotID = args.Get(1).(dockermodel.BackendSnapshotRequest).SnapshotID
		}).
		Return(dockermodel.SnapshotStat{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Restore", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRestoreRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()
	safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
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
	expectSafetySnapshotCommit(repo, "1780335122")
	expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, 60*time.Second, demoComposeFiles()).Return(nil)

	savepointBackend := &mockBackend{volume: vol}
	savepointBackend.On("Restore", mock.Anything,
		dockermodel.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()

	safetySnapshotBackend := &mockBackend{volume: vol}
	safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) {
			safetySnapshotID = args.Get(1).(dockermodel.BackendSnapshotRequest).SnapshotID
		}).
		Return(dockermodel.SnapshotStat{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	safetySnapshotBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req dockermodel.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Payload == "safety-payload"
	})).Return(nil).Once()

	registry := &mockRegistry{}
	store := newDirStore(t)
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
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

// The id gate stands ahead of the whole workflow: an id that could name a
// parent directory builds no backend and stops nothing.
func TestService_Rollback_RejectsAnUnsafeIDBeforeAnyBackend(t *testing.T) {
	// Arrange
	record := demoRecord()
	record.ID = "../../etc"
	repo := &mockSavepointRepo{}
	repo.On("List", mock.Anything).Return([]dockermodel.Savepoint{record}, nil)
	locker := &mockLocker{}
	expectLock(locker)

	// Nothing past the gate carries an expectation: an introspection, a build, a
	// stop or a dispatch fails the test loudly.
	inspector := &mockInspector{}
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}
	store := newDirStore(t)

	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
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
}

// A rollback over real copy captures, in the one store both now write into: the
// savepoint tree survives untouched, and the backup's own tree — record and
// archive alike — is gone by the end.
func TestService_Rollback_OverRealStore(t *testing.T) {
	// Arrange
	// The full literal stays inline: real fullcopy addressing needs the compose
	// project and the mount, not the trimmed mock-test form.
	vol := dockermodel.Volume{
		ObjectID:       "volume-named-demo_db",
		ComposeProject: "demo",
		Type:           dockermodel.VolumeTypeNamed,
		Backend:        dockermodel.SnapshotBackendCopy,
		Named: &dockermodel.VolumePropertiesNamed{VolumeName: "demo_db",
			UsedBy: []dockermodel.ServiceReference{{Service: "db", Target: "/data"}}},
	}
	record := dockermodel.Savepoint{
		ID:        "1780335122",
		Name:      "sp",
		CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC),
		Stats:     []dockermodel.SnapshotStat{{Volume: vol, Type: dockermodel.SnapshotBackendCopy, Payload: "sp-payload"}},
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
	svc := New(Deps{
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: dockermodel.SnapshotBackendCopy,
	})

	// The capture the restore reads, at the key the backend renders.
	archive := "1780335122/volume-named-demo_db/data.tar"
	require.NoError(t, store.PutObject(context.Background(), archive, strings.NewReader("savepoint bytes")))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, dockermodel.RollbackRequest{
		ComposeFiles: demoComposeFiles(),
		ID:           ptrID("1780335122"),
		WaitTimeout:  60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []dockermodel.Volume{vol}, res.Restored)
	assert.Equal(t, "savepoint bytes", docker.written.String(), "the archive's stream is what reaches the mount")
	assert.ElementsMatch(t, []string{archive, "1780335122/savepoint.json"}, objectNames(t, store),
		"the savepoint is read, never rewritten, and the backup's own tree is swept whole")
	committed, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []dockermodel.Savepoint{record}, committed,
		"the backup is uncommitted on success, so nothing of it lists — not even with the flag")
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
}

// byID picks one record out of a listing whose order the store decides.
func byID(records []dockermodel.Savepoint, id model.SavepointID) (dockermodel.Savepoint, bool) {
	for _, r := range records {
		if r.ID == id {
			return r, true
		}
	}
	return dockermodel.Savepoint{}, false
}

// TestVolumesOf pins the projection, and that it is never nil — restored must
// marshal as [], not null.
func TestVolumesOf(t *testing.T) {
	vol := dockermodel.Volume{ObjectID: "volume-named-demo_db", Backend: dockermodel.SnapshotBackendCopy}

	tests := []struct {
		name   string
		record dockermodel.Savepoint
		want   []dockermodel.Volume
	}{
		{
			name:   "a stat per volume yields a volume per stat",
			record: dockermodel.Savepoint{ID: "100", Stats: []dockermodel.SnapshotStat{{Volume: vol, Type: dockermodel.SnapshotBackendCopy}}},
			want:   []dockermodel.Volume{vol},
		},
		{
			name:   "no stats yields an empty slice, never nil",
			record: dockermodel.Savepoint{ID: "100"},
			want:   []dockermodel.Volume{},
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
