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

func TestService_CreateSavepoint_CommitsTheWritableLayers(t *testing.T) {
	// Arrange
	introspection := demoIntrospectionWithContainers()
	vol := demoVolume()
	repo := &mockSavepointRepo{}
	locker := &mockLocker{}
	expectLock(locker)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()

	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	backend := &mockBackend{volume: vol}
	snapshot := backend.On("Snapshot", mock.Anything, mock.Anything).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy}, nil).Once()
	capturer := &mockImageCapturer{}
	// The id comes off the clock, so only the container half can be named.
	capture := capturer.On("Capture", mock.Anything, mock.MatchedBy(func(req model.ImageCaptureRequest) bool {
		return req.SnapshotID.Validate() == nil && req.Container == demoContainerRef()
	})).Return(demoImageCapture(), nil).Once()
	spec := expectSpecCommit(repo)
	commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return len(r.Images) == 1 && r.Images[0] == demoImageCapture()
	})).Return(nil).Once()
	start := runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()

	// The layer is committed while the services are down, after the volumes and
	// before the spec; the record stays last.
	mock.InOrder(stop, snapshot, capture, spec, commit, start)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection), ComposeRunner: runner, BackendRegistry: registry,
		ImageCapturer: capturer, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.ImageCapture{demoImageCapture()}, savepoint.Images)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
	inspector.AssertExpectations(t)
	runner.AssertExpectations(t)
	backend.AssertExpectations(t)
	capturer.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_CreateSavepoint_FailedImageCaptureCommitsNothing(t *testing.T) {
	// Arrange
	introspection := demoIntrospectionWithContainers()
	vol := demoVolume()
	commitErr := errors.New("daemon refused to commit")
	repo := &mockSavepointRepo{}
	locker := &mockLocker{}
	expectLock(locker)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	// The services come back even though the capture failed: the capture error is
	// what the caller gets, the restart on top of it is best-effort.
	runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()
	backend := &mockBackend{volume: vol}
	backend.On("Snapshot", mock.Anything, mock.Anything).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy}, nil).Once()
	capturer := &mockImageCapturer{}
	capturer.On("Capture", mock.Anything, mock.Anything).Return(model.ImageCapture{}, commitErr).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection), ComposeRunner: runner, BackendRegistry: registry,
		ImageCapturer: capturer, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, commitErr)
	assert.Equal(t, model.Savepoint{}, savepoint)
	// Neither the spec nor the record was written: the repo mock expects no call.
	repo.AssertExpectations(t)
	capturer.AssertExpectations(t)
	backend.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_Rollback_SafetySnapshotCommitsTheWritableLayers(t *testing.T) {
	// Arrange
	introspection := demoIntrospectionWithContainers()
	vol := demoVolume()
	record := demoRecord()
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("LoadSpec", mock.Anything, record.ID).Return(demoSpecBytes, nil)
	locker := &mockLocker{}
	expectLock(locker)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil)

	var safetySnapshotID model.SavepointID
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	snapshotSafety := safetyBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	capturer := &mockImageCapturer{}
	// The id is read at match time, filled by the volume capture that runs first.
	captureSafety := capturer.On("Capture", mock.Anything, mock.MatchedBy(func(req model.ImageCaptureRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Container == demoContainerRef()
	})).Return(demoImageCapture(), nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.IsSafetySnapshot() && len(r.Images) == 1 && r.Images[0] == demoImageCapture()
	})).Return(nil).Once()
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	restore := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	start := runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(nil).Once()

	// The containers it commits exist only between the stop and the teardown.
	mock.InOrder(stop, snapshotSafety, captureSafety, specSafety, commitSafety, down, create,
		restore, start, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection), ComposeRunner: runner, BackendRegistry: registry,
		ImageCapturer: capturer, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	capturer.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
	registry.AssertExpectations(t)
}
