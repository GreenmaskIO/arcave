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
