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
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
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
	capturer := &mockImageBackend{}
	// The id comes off the clock, so only the container half can be named.
	capture := capturer.On("Capture", mock.Anything, mock.MatchedBy(func(req model.ImageCaptureRequest) bool {
		return req.SnapshotID.Validate() == nil && req.Container == demoContainerRef()
	})).Return(demoImageCapture(), nil).Once()
	upstream := capturer.On("CaptureUpstream", mock.Anything, demoUpstreamCaptureRequest()).
		Return(demoUpstreamImage(), nil).Once()
	spec := expectSpecCommit(repo)
	commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return len(r.Images) == 1 && r.Images[0] == demoImageCapture() &&
			assert.ObjectsAreEqual([]model.UpstreamImage{demoUpstreamImage()}, r.UpstreamImages)
	})).Return(nil).Once()
	start := runner.On("Start", mock.Anything, demoSpec, 60*time.Second).Return(nil).Once()

	// The layer is committed while the services are down, after the volumes; the
	// upstream follows it, then the spec, and the record stays last.
	mock.InOrder(stop, snapshot, capture, upstream, spec, commit, start)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
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
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.ImageCapture{demoImageCapture()}, savepoint.Images)
	assert.Equal(t, []model.UpstreamImage{demoUpstreamImage()}, savepoint.UpstreamImages)
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
	capturer := &mockImageBackend{}
	capturer.On("Capture", mock.Anything, mock.Anything).Return(model.ImageCapture{}, commitErr).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
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
		DefaultBackend:   model.SnapshotBackendCopy,
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
	repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil)
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
	capturer := &mockImageBackend{}
	// The id is read at match time, filled by the volume capture that runs first.
	captureSafety := capturer.On("Capture", mock.Anything, mock.MatchedBy(func(req model.ImageCaptureRequest) bool {
		return req.SnapshotID == safetySnapshotID && req.Container == demoContainerRef()
	})).Return(demoImageCapture(), nil).Once()
	upstreamSafety := capturer.On("CaptureUpstream", mock.Anything, demoUpstreamCaptureRequest()).
		Return(demoUpstreamImage(), nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.IsSafetySnapshot() && len(r.Images) == 1 && r.Images[0] == demoImageCapture() &&
			assert.ObjectsAreEqual([]model.UpstreamImage{demoUpstreamImage()}, r.UpstreamImages)
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
	mock.InOrder(stop, snapshotSafety, captureSafety, upstreamSafety, specSafety, commitSafety, down, create,
		restore, start, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
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
		DefaultBackend:   model.SnapshotBackendCopy,
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

func TestService_Rollback_RestoresTheWritableLayers(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecordWithImages()
	repo, inspector, locker := demoResolution(record)

	var safetySnapshotID model.SavepointID
	images := &mockImageBackend{}
	// The load runs on the live compose project: it is this workflow's pre-flight.
	restoreImage := images.On("Restore", mock.Anything, model.ImageRestoreRequest{
		SnapshotID: record.ID, Capture: demoImageCapture(), ImageRef: demoRestoredRef(1),
	}).Return(demoImageRestore(), nil).Once()
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	snapshotSafety := safetyBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Run(func(args mock.Arguments) { safetySnapshotID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	specSafety := expectSpecCommit(repo)
	commitSafety := expectSafetySnapshotCommit(repo, record.ID)
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	// The compose project is rebuilt on the restored layer, and never off a registry.
	target := matchRetargetedImage(demoService, demoRestoredRef(1))
	create := runner.On("Create", mock.Anything, target).Return(nil).Once()
	restoreVolume := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	start := runner.On("Start", mock.Anything, target, 60*time.Second).Return(nil).Once()
	uncommitSafety := expectSafetySnapshotUncommit(repo, &safetySnapshotID)
	removeSafety := safetyBackend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID == safetySnapshotID
	})).Return(nil).Once()

	mock.InOrder(restoreImage, stop, snapshotSafety, specSafety, commitSafety, down, create,
		restoreVolume, start, uncommitSafety, removeSafety)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		ImageBackend:     images,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.ImageRestore{demoImageRestore()}, res.Images)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	images.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_Rollback_ScaledServiceRebuildsDivergentReplicas(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoScaledRecord()
	repo, _, locker := demoResolution(record)
	// Two reads: the live compose project the workflow prepares against, then the fixup's own
	// read of what compose has just created.
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
	inspector.On("Introspect", mock.Anything).Return(demoScaledIntrospection(), nil).Once()

	images := &mockImageBackend{}
	images.On("Restore", mock.Anything, model.ImageRestoreRequest{
		SnapshotID: record.ID, Capture: demoImageCapture(), ImageRef: demoRestoredRef(1),
	}).Return(demoImageRestore(), nil).Once()
	images.On("Restore", mock.Anything, model.ImageRestoreRequest{
		SnapshotID: record.ID, Capture: demoSecondImageCapture(), ImageRef: demoRestoredRef(2),
	}).Return(model.ImageRestore{
		Service: demoService, Replica: 2, ContainerName: demoContainerName(2),
		ImageID: demoSecondImageID, ImageRef: demoRestoredRef(2),
	}, nil).Once()

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, record.ID)
	runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	// The spec carries the first replica's image; only the second is rebuilt.
	target := matchRetargetedImage(demoService, demoRestoredRef(1))
	create := runner.On("Create", mock.Anything, target).Return(nil).Once()
	rebuilder := &mockContainerRebuilder{}
	rebuild := rebuilder.On("RecreateWithImage", mock.Anything, demoSecondContainerID,
		model.ReplicaIdentity{Name: demoContainerName(2), Number: 2}, demoRestoredRef(2)).
		Return("rebuilt", nil).Once()
	restoreVolume := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	runner.On("Start", mock.Anything, target, 60*time.Second).Return(nil).Once()
	repo.On("DeleteSavepoint", mock.Anything, mock.Anything).Return(nil).Once()
	safetyBackend.On("Remove", mock.Anything, mock.Anything).Return(nil).Once()

	// The container set has to be final before a volume is restored into it.
	mock.InOrder(create, rebuild, restoreVolume)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover:      &mockVolumeRemover{},
		SavepointRepo:      repo,
		ProjectLocker:      locker,
		ComposeInspector:   inspector,
		ProjectAssembler:   demoAssembler(),
		SpecCodec:          demoCodec(),
		ComposeRunner:      runner,
		BackendRegistry:    registry,
		ImageBackend:       images,
		ContainerRebuilder: rebuilder,
		Inflight:           &fakeInflight{},
		Store:              store,
		DefaultBackend:     model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Len(t, res.Images, 2)
	inspector.AssertExpectations(t)
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	images.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_Rollback_SavepointWithoutImagesRestoresNothing(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)

	// Neither seam carries an expectation: a record predating image capture must
	// reach neither the daemon nor the store.
	images := &mockImageBackend{}
	rebuilder := &mockContainerRebuilder{}

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	safetyBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "safety-payload"}, nil).Once()
	expectSpecCommit(repo)
	expectSafetySnapshotCommit(repo, record.ID)
	runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	// The spec goes to the rebuild untouched: nothing was retargeted.
	runner.On("Create", mock.Anything, demoTargetSpec).Return(nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload"}).Return(nil).Once()
	runner.On("Start", mock.Anything, demoTargetSpec, 60*time.Second).Return(nil).Once()
	repo.On("DeleteSavepoint", mock.Anything, mock.Anything).Return(nil).Once()
	safetyBackend.On("Remove", mock.Anything, mock.Anything).Return(nil).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()

	svc := New(Deps{
		VolumeRemover:      &mockVolumeRemover{},
		SavepointRepo:      repo,
		ProjectLocker:      locker,
		ComposeInspector:   inspector,
		ProjectAssembler:   demoAssembler(),
		SpecCodec:          demoCodec(),
		ComposeRunner:      runner,
		BackendRegistry:    registry,
		ImageBackend:       images,
		ContainerRebuilder: rebuilder,
		Inflight:           &fakeInflight{},
		Store:              store,
		DefaultBackend:     model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, res.Images)
	images.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_Rollback_FailedImageLoadStopsNothing(t *testing.T) {
	// Arrange
	record := demoRecordWithImages()
	repo, inspector, locker := demoResolution(record)

	images := &mockImageBackend{}
	images.On("Restore", mock.Anything, mock.Anything).
		Return(model.ImageRestore{}, model.ErrSavepointImageMissing).Once()

	// The runner carries no expectation at all: a load that fails is a pre-flight
	// failure, and the project is still running when it is reported.
	runner := &mockComposeRunner{}
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, demoVolume().ObjectID)).Return(&mockBackend{volume: demoVolume()}, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		ImageBackend:     images,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrSavepointImageMissing)
	assert.Equal(t, model.RollbackResult{}, res)
	runner.AssertExpectations(t)
	images.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestRestoredImageRef(t *testing.T) {
	const id = model.SavepointID("1780335122")
	// A repository path may hold at most 255 characters, and a path component only
	// lower-case alphanumerics joined by separators — neither is arcave's to widen.
	longImage := strings.Repeat("x", 300)

	tests := []struct {
		name    string
		project string
		capture model.ImageCapture
		want    string
	}{
		{
			name:    "project, service, replica and upstream image",
			project: "demo",
			capture: model.ImageCapture{Service: "db", Replica: 1, UpstreamImageRef: "postgres:16"},
			want:    "arcave/demo-db-1-postgres-16:sp-1780335122",
		},
		{
			name:    "a registry-qualified upstream image",
			project: "demo",
			capture: model.ImageCapture{Service: "web", Replica: 2, UpstreamImageRef: "ghcr.io/Org/App:1.2.3"},
			want:    "arcave/demo-web-2-ghcr-io-org-app-1-2-3:sp-1780335122",
		},
		{
			name:    "an upper-case service, which a repository path refuses",
			project: "Demo_App",
			capture: model.ImageCapture{Service: "DB", Replica: 1, UpstreamImageRef: "busybox"},
			want:    "arcave/demo-app-db-1-busybox:sp-1780335122",
		},
		{
			name:    "a capture whose upstream could not be traced is still addressable",
			project: "demo",
			capture: model.ImageCapture{Service: "db", Replica: 1},
			want:    "arcave/demo-db-1:sp-1780335122",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := restoredImageRef(tt.project, id, tt.capture)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("an upstream image too long for the cap is cut, never the identity", func(t *testing.T) {
		// Act
		got := restoredImageRef("demo", id, model.ImageCapture{
			Service: "db", Replica: 1, UpstreamImageRef: longImage,
		})

		// Assert
		repository, tag, _ := strings.Cut(got, ":")
		assert.Len(t, repository, 255)
		assert.True(t, strings.HasPrefix(repository, "arcave/demo-db-1-"), repository)
		assert.Equal(t, "sp-1780335122", tag)
	})
}

// TestRestoredImageRef_IsAlwaysNameable pins the ref against docker's own path
// grammar: lower-case alphanumeric runs joined by single dashes is a subset of it,
// so no service or image name can produce something the daemon refuses to tag.
func TestRestoredImageRef_IsAlwaysNameable(t *testing.T) {
	nameable := regexp.MustCompile(`^arcave/[a-z0-9]+(-[a-z0-9]+)*:sp-[0-9]+$`)

	names := []string{"", "___", "..", "-lead", "trail-", "UPPER", "a b/c:d@e", "юникод"}
	bases := append(append([]string{}, names...), strings.Repeat("x", 300))
	for _, project := range names {
		for _, service := range names {
			for _, base := range bases {
				// Act
				got := restoredImageRef(project, "1780335122", model.ImageCapture{
					Service: service, Replica: 1, UpstreamImageRef: base,
				})

				// Assert
				assert.Regexp(t, nameable, got, "project %q, service %q, base image %q", project, service, base)
				repository, _, _ := strings.Cut(got, ":")
				assert.LessOrEqual(t, len(repository), 255)
			}
		}
	}
}

func TestPairReplicas(t *testing.T) {
	tests := []struct {
		name     string
		recorded []int
		created  []int
		want     []replicaPair
		wantErr  string
	}{
		{
			name: "no gap pairs each replica with itself", recorded: []int{1, 2}, created: []int{1, 2},
			want: []replicaPair{{recorded: 1, created: 1}, {recorded: 2, created: 2}},
		},
		{
			name: "a gap in the middle", recorded: []int{1, 3}, created: []int{1, 2},
			want: []replicaPair{{recorded: 1, created: 1}, {recorded: 3, created: 2}},
		},
		{
			name: "the first replica gone", recorded: []int{2, 3}, created: []int{1, 2},
			want: []replicaPair{{recorded: 2, created: 1}, {recorded: 3, created: 2}},
		},
		{
			name: "a gap at the top, input unsorted", recorded: []int{5, 1, 2}, created: []int{3, 2, 1},
			want: []replicaPair{{recorded: 1, created: 1}, {recorded: 2, created: 2}, {recorded: 5, created: 3}},
		},
		{
			name: "fewer created than recorded", recorded: []int{1, 3}, created: []int{1},
			wantErr: "holds 1 replicas to restore 2 recorded ones into",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := pairReplicas(tt.recorded, tt.created)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The service's one image is its lowest recorded replica's, whether or not that is 1.
func TestRetargetImages_LowestRecordedReplica(t *testing.T) {
	// Arrange
	project := &types.Project{Name: demoProject, Services: types.Services{
		demoService: {Name: demoService, Image: demoBaseImage},
	}}
	restored := []model.ImageRestore{
		{Service: demoService, Replica: 3, ImageRef: demoRestoredRef(3)},
		{Service: demoService, Replica: 2, ImageRef: demoRestoredRef(2)},
	}

	// Act
	got := retargetImages(project, restored)

	// Assert
	assert.Equal(t, demoRestoredRef(2), got.Services[demoService].Image)
	assert.Equal(t, types.PullPolicyNever, got.Services[demoService].PullPolicy)
	assert.Equal(t, demoBaseImage, project.Services[demoService].Image, "the input project is left alone")
}

func TestService_rebuildDivergentReplicas(t *testing.T) {
	type rebuild struct {
		created  int
		recorded int
	}
	tests := []struct {
		name     string
		recorded []int
		created  []int
		// sameImage restores every replica on one layer, so only a renumbering
		// makes a rebuild.
		sameImage bool
		want      []rebuild
		wantErr   string
	}{
		{
			name: "no gap rebuilds every replica past the first", recorded: []int{1, 2, 3}, created: []int{1, 2, 3},
			want: []rebuild{{created: 3, recorded: 3}, {created: 2, recorded: 2}},
		},
		{
			name: "a gap in the middle", recorded: []int{1, 3}, created: []int{1, 2},
			want: []rebuild{{created: 2, recorded: 3}},
		},
		{
			name: "the first replica gone renumbers both, from the top down", recorded: []int{2, 3}, created: []int{1, 2},
			want: []rebuild{{created: 2, recorded: 3}, {created: 1, recorded: 2}},
		},
		{
			name: "a gap at the top", recorded: []int{1, 2, 5}, created: []int{1, 2, 3},
			want: []rebuild{{created: 3, recorded: 5}, {created: 2, recorded: 2}},
		},
		{
			name: "one image and a gap still renumbers", recorded: []int{1, 3}, created: []int{1, 2}, sameImage: true,
			want: []rebuild{{created: 2, recorded: 3}},
		},
		{
			name: "fewer created than recorded", recorded: []int{1, 3}, created: []int{1},
			wantErr: "service db: the rebuilt compose project holds 1 replicas to restore 2 recorded ones into",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			restored := make([]model.ImageRestore, 0, len(tt.recorded))
			for _, n := range tt.recorded {
				ref := demoRestoredRef(n)
				if tt.sameImage {
					ref = demoRestoredRef(tt.recorded[0])
				}
				restored = append(restored, model.ImageRestore{
					Service: demoService, Replica: n, ContainerName: demoContainerName(n), ImageRef: ref,
				})
			}
			built := demoIntrospection()
			svc := model.Service{Name: demoService}
			for _, n := range tt.created {
				svc.Containers = append(svc.Containers, model.Container{ID: createdID(n), Number: n})
			}
			built.Project.Services = []model.Service{svc}
			inspector := &mockInspector{}
			inspector.On("Introspect", mock.Anything).Return(built, nil).Once()
			rebuilder := &mockContainerRebuilder{}
			calls := make([]*mock.Call, 0, len(tt.want))
			for _, w := range tt.want {
				calls = append(calls, rebuilder.On("RecreateWithImage", mock.Anything, createdID(w.created),
					model.ReplicaIdentity{Name: demoContainerName(w.recorded), Number: w.recorded},
					refOf(restored, w.recorded)).
					Return("rebuilt", nil).Once())
			}
			// A name is claimed only once the container holding it has been rebuilt away.
			mock.InOrder(calls...)
			s := New(Deps{ComposeInspector: inspector, ContainerRebuilder: rebuilder})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := s.rebuildDivergentReplicas(ctx, restored)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			inspector.AssertExpectations(t)
			rebuilder.AssertExpectations(t)
		})
	}
}

// A compose project compose numbers as recorded, on one image, needs no rebuild and
// not even a look at what was created.
func TestService_rebuildDivergentReplicas_CreatedAsRecorded(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	rebuilder := &mockContainerRebuilder{}
	s := New(Deps{ComposeInspector: inspector, ContainerRebuilder: rebuilder})
	restored := []model.ImageRestore{
		{Service: demoService, Replica: 2, ImageRef: demoRestoredRef(1)},
		{Service: demoService, Replica: 1, ImageRef: demoRestoredRef(1)},
	}

	// Act
	err := s.rebuildDivergentReplicas(zerolog.Nop().WithContext(context.Background()), restored)

	// Assert
	require.NoError(t, err)
	inspector.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
}

func TestService_rebuildDivergentReplicas_WarnsOnARenumberedReplica(t *testing.T) {
	// Arrange
	built := demoIntrospection()
	built.Project.Services = []model.Service{{Name: demoService, Containers: []model.Container{
		{ID: createdID(1), Number: 1}, {ID: createdID(2), Number: 2},
	}}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(built, nil).Once()
	rebuilder := &mockContainerRebuilder{}
	rebuilder.On("RecreateWithImage", mock.Anything, createdID(2),
		model.ReplicaIdentity{Name: demoContainerName(3), Number: 3}, demoRestoredRef(3)).Return("rebuilt", nil).Once()
	s := New(Deps{ComposeInspector: inspector, ContainerRebuilder: rebuilder})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	err := s.rebuildDivergentReplicas(ctx, []model.ImageRestore{
		{Service: demoService, Replica: 1, ContainerName: demoContainerName(1), ImageRef: demoRestoredRef(1)},
		{Service: demoService, Replica: 3, ContainerName: demoContainerName(3), ImageRef: demoRestoredRef(3)},
	})

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(),
		`"service":"db","replica":3,"created_replica":2,"message":"compose numbers replicas without gaps`)
	inspector.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
}

// createdID is the id of the container compose created as replica n.
func createdID(n int) string { return fmt.Sprintf("created-%d", n) }

// refOf is the ref the restore of replica n carries.
func refOf(restored []model.ImageRestore, n int) string {
	for _, image := range restored {
		if image.Replica == n {
			return image.ImageRef
		}
	}
	return ""
}
