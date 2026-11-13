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
	"github.com/greenmaskio/arcave/internal/service/capture"
)

// The capture is handed the live compose project's upstream and its writable layer; what it
// commits is the result.
func TestService_CreateSavepoint_PlansTheImagesAndWritableLayers(t *testing.T) {
	// Arrange
	introspection := demoIntrospectionWithContainers()
	vol := demoVolume()
	repo := emptySavepointRepo()
	locker := &mockLocker{}
	expectLock(locker)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	committed := demoRecordWithImages()
	committed.Images = []model.Image{demoImage()}
	captured := &mockCapture{}
	expectCapture(captured, matchLayerPlan(), capture.Request{}, committed)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(&mockBackend{volume: vol}, nil).Once()

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    &mockComposeRunner{},
		Capture:          captured,
		BackendRegistry:  registry,
		ImageBackend:     &mockImageBackend{},
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
	inspector.AssertExpectations(t)
	captured.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// The safety snapshot is handed the live layers, and the containers its hold killed.
func TestService_Rollback_SafetySnapshotPlansTheLiveLayers(t *testing.T) {
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

	captured := &mockCapture{}
	hold := captured.On("Hold", mock.Anything, matchLayerPlan()).Return([]string{demoContainerID}, nil).Once()
	safety := captured.On("Capture", mock.Anything, fakeGrantedSessionLock{}, matchLayerPlan(), []string{demoContainerID},
		newSafetySnapshotRequest(record)).Return(demoSafetySnapshot(), nil).Once()
	runner := &mockComposeRunner{}
	savepointBackend := &mockBackend{volume: vol}
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	restore := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	start := runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once()

	// The containers it commits exist only between the hold and the teardown.
	mock.InOrder(hold, safety, down, create, restore, start)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(&mockBackend{volume: vol}, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		Capture:          captured,
		BackendRegistry:  registry,
		ImageBackend:     &mockImageBackend{},
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, demoSafetySnapshot().ID, res.SafetySnapshot)
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	captured.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	registry.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// The revert puts the safety snapshot's layers back: the parity the capture side has.
func TestService_Rollback_RevertRestoresTheSafetySnapshotLayers(t *testing.T) {
	// Arrange
	introspection := demoIntrospectionWithContainers()
	vol := demoVolume()
	record := demoRecord()
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Twice()
	repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	inspector := &mockInspector{}
	introspect := inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()

	safetySnapshot := demoSafetySnapshot()
	safetySnapshot.WritableLayers = []model.WritableLayer{demoWritableLayer()}
	revertTag := fmt.Sprintf("arcave/%s-%s-1-postgres-16:sp-%s", demoProject, demoService, safetySnapshot.ID)
	captured := &mockCapture{}
	hold, safety := expectSafetySnapshot(captured, record, safetySnapshot)
	runner := &mockComposeRunner{}
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	images := &mockImageBackend{}
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	create := runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	restoreSp := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).
		Return(errors.New("restore boom")).Once()
	// demoSpec names no service, so the retargeted live spec is matched by name alone.
	liveSpec := mock.MatchedBy(func(project *types.Project) bool {
		return project.Name == demoSpec.Name && len(project.Services) == 0
	})
	restoreLayer := images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
		SnapshotID: safetySnapshot.ID, Layer: demoWritableLayer(), Tag: revertTag,
	}).Return(model.WritableLayerRestoreResult{
		Service: demoService, Replica: 1, ContainerName: demoContainerName(1),
		ImageID: demoWritableLayer().CommittedImageID, Tag: "revert-tag",
	}, nil).Once()
	downForRevert := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	createForRevert := runner.On("Create", mock.Anything, liveSpec, demoRestoredVolumes).Return(nil).Once()
	restoreSafety := safetyBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: safetySnapshot.ID, Payload: "safety-payload"}).Return(nil).Once()
	up := runner.On("Start", mock.Anything, liveSpec, 60*time.Second).Return(nil).Once()
	mock.InOrder(introspect, hold, safety, down, create,
		restoreSp, restoreLayer, downForRevert, createForRevert, restoreSafety, up)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		Capture:          captured,
		BackendRegistry:  registry,
		ImageBackend:     images,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorContains(t, err, "restore boom")
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	captured.AssertExpectations(t)
	images.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
	registry.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// A rollback taking no safety snapshot captures nothing of the live compose project, neither volumes
// nor writable layers nor images, and stops nothing by itself; the savepoint's own layers still go
// back. A capture or a stop has no expectation here, and fails the run.
func TestService_Rollback_SkippedSafetySnapshotCapturesNoLayer(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecordWithImages()
	repo, inspector, locker := demoResolution(record)
	captured := &mockCapture{}
	images := &mockImageBackend{}
	restoreLayer := images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
		SnapshotID: record.ID, Layer: demoWritableLayer(), Tag: demoRestoredTag(1),
	}).Return(demoWritableLayerRestoreResult(), nil).Once()
	target := matchRetargetedImage(demoService, demoRestoredTag(1))
	runner := &mockComposeRunner{}
	down := runner.On("Down", mock.Anything, target).Return(nil).Once()
	create := runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(nil).Once()
	backend := &mockBackend{volume: vol}
	restoreVolume := backend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	start := runner.On("Start", mock.Anything, target, 60*time.Second).Return(nil).Once()
	mock.InOrder(restoreLayer, down, create, restoreVolume, start)
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()
	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		Capture:          captured,
		BackendRegistry:  registry,
		ImageBackend:     images,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
	})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, res.SafetySnapshot)
	assert.Equal(t, []model.WritableLayerRestoreResult{demoWritableLayerRestoreResult()}, res.WritableLayers)
	captured.AssertExpectations(t)
	images.AssertExpectations(t)
	runner.AssertExpectations(t)
	backend.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_Rollback_RestoresTheWritableLayers(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecordWithImages()
	repo, inspector, locker := demoResolution(record)

	images := &mockImageBackend{}
	// The load runs on the live compose project: it is this workflow's pre-flight.
	restoreImage := images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
		SnapshotID: record.ID, Layer: demoWritableLayer(), Tag: demoRestoredTag(1),
	}).Return(demoWritableLayerRestoreResult(), nil).Once()
	captured := &mockCapture{}
	hold, safety := expectSafetySnapshot(captured, record, demoSafetySnapshot())
	runner := &mockComposeRunner{}
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	down := runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	// The compose project is rebuilt on the restored layer, and never off a registry.
	target := matchRetargetedImage(demoService, demoRestoredTag(1))
	create := runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(nil).Once()
	restoreVolume := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	start := runner.On("Start", mock.Anything, target, 60*time.Second).Return(nil).Once()

	mock.InOrder(restoreImage, hold, safety, down, create, restoreVolume, start)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		Capture:          captured,
		BackendRegistry:  registry,
		ImageBackend:     images,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(), "`docker compose up` from your compose file recreates them", "the user is told the layers are the containers'")
	assert.Equal(t, []model.WritableLayerRestoreResult{demoWritableLayerRestoreResult()}, res.WritableLayers)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	captured.AssertExpectations(t)
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
	images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
		SnapshotID: record.ID, Layer: demoWritableLayer(), Tag: demoRestoredTag(1),
	}).Return(demoWritableLayerRestoreResult(), nil).Once()
	images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
		SnapshotID: record.ID, Layer: demoSecondWritableLayer(), Tag: demoRestoredTag(2),
	}).Return(model.WritableLayerRestoreResult{
		Service: demoService, Replica: 2, ContainerName: demoContainerName(2),
		ImageID: demoSecondImageID, Tag: demoRestoredTag(2),
	}, nil).Once()

	captured := &mockCapture{}
	expectSafetySnapshot(captured, record, demoSafetySnapshot())
	runner := &mockComposeRunner{}
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	// The spec carries the first replica's image; only the second is rebuilt.
	target := matchRetargetedImage(demoService, demoRestoredTag(1))
	create := runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(nil).Once()
	rebuilder := &mockContainerRebuilder{}
	rebuild := rebuilder.On("RecreateWithImage", mock.Anything, demoSecondContainerID,
		model.ReplicaIdentity{Name: demoContainerName(2), Number: 2}, demoRestoredTag(2)).
		Return("rebuilt", nil).Once()
	restoreVolume := savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	runner.On("Start", mock.Anything, target, 60*time.Second).Return(nil).Once()

	// The container set has to be final before a volume is restored into it.
	mock.InOrder(create, rebuild, restoreVolume)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		ArcaveProject:      demoArcaveProject,
		SessionLocker:      fakeSessionLocker{},
		WriteAdmission:     fakeWriteAdmission{},
		VolumeRemover:      &mockVolumeRemover{},
		SavepointRepo:      repo,
		ProjectLocker:      locker,
		ComposeInspector:   inspector,
		ProjectAssembler:   demoAssembler(),
		SpecCodec:          demoCodec(),
		ComposeRunner:      runner,
		Capture:            captured,
		BackendRegistry:    registry,
		ImageBackend:       images,
		ContainerRebuilder: rebuilder,
		Inflight:           &fakeInflight{},
		Store:              store,
		CaptureDefaults:    demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Len(t, res.WritableLayers, 2)
	inspector.AssertExpectations(t)
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	captured.AssertExpectations(t)
	images.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
	registry.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
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

	captured := &mockCapture{}
	expectSafetySnapshot(captured, record, demoSafetySnapshot())
	runner := &mockComposeRunner{}
	savepointBackend := &mockBackend{volume: vol}
	safetyBackend := &mockBackend{volume: vol}
	runner.On("Down", mock.Anything, demoSpec).Return(nil).Once()
	// The spec goes to the rebuild untouched: nothing was retargeted.
	runner.On("Create", mock.Anything, deployed(demoTargetSpec), demoRestoredVolumes).Return(nil).Once()
	savepointBackend.On("Restore", mock.Anything,
		model.BackendRestoreRequest{SnapshotID: record.ID, Payload: "sp-payload", SizeBytes: demoVolumeSize}).Return(nil).Once()
	runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetyBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()

	svc := New(Deps{
		ArcaveProject:      demoArcaveProject,
		SessionLocker:      fakeSessionLocker{},
		WriteAdmission:     fakeWriteAdmission{},
		VolumeRemover:      &mockVolumeRemover{},
		SavepointRepo:      repo,
		ProjectLocker:      locker,
		ComposeInspector:   inspector,
		ProjectAssembler:   demoAssembler(),
		SpecCodec:          demoCodec(),
		ComposeRunner:      runner,
		Capture:            captured,
		BackendRegistry:    registry,
		ImageBackend:       images,
		ContainerRebuilder: rebuilder,
		Inflight:           &fakeInflight{},
		Store:              store,
		CaptureDefaults:    demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.NoError(t, err)
	assert.Empty(t, res.WritableLayers)
	images.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
	runner.AssertExpectations(t)
	captured.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	safetyBackend.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
}

func TestService_Rollback_FailedImageLoadStopsNothing(t *testing.T) {
	// Arrange
	record := demoRecordWithImages()
	repo, inspector, locker := demoResolution(record)

	images := &mockImageBackend{}
	images.On("RestoreWritableLayer", mock.Anything, mock.Anything).
		Return(model.WritableLayerRestoreResult{}, model.ErrSavepointImageMissing).Once()

	// Neither the capture nor the runner carries an expectation: a load that fails is a
	// pre-flight failure, and the project is still running when it is reported.
	captured := &mockCapture{}
	runner := &mockComposeRunner{}
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, demoVolume().ObjectID)).Return(&mockBackend{volume: demoVolume()}, nil).Twice()

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		Capture:          captured,
		BackendRegistry:  registry,
		ImageBackend:     images,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrSavepointImageMissing)
	assert.Equal(t, model.RollbackResult{}, res)
	captured.AssertExpectations(t)
	runner.AssertExpectations(t)
	images.AssertExpectations(t)
	locker.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestService_CreateSavepoint_UnsupportedDaemonStopsNothing(t *testing.T) {
	// Arrange
	introspection := demoIntrospectionWithContainers()
	locker := &mockLocker{}
	expectLock(locker)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()

	// No expectation on the assembler, the capture, the repository or the image
	// backend: the refusal comes before any of them.
	captured := &mockCapture{}
	images := &mockImageBackend{}
	assembler := &mockAssembler{}
	runner := &mockComposeRunner{}
	repo := emptySavepointRepo()
	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        &mockCodec{},
		ComposeRunner:    runner,
		Capture:          captured,
		BackendRegistry:  &mockRegistry{},
		ImageBackend:     images,
		Inflight:         &fakeInflight{},
		Store:            newDirStore(t),
		CaptureDefaults:  demoCaptureDefaults,
		DaemonVersion:    "24.0.7",
		MinDaemonMajor:   25,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrUnsupportedDaemon)
	assert.Equal(t, model.Savepoint{}, savepoint)
	inspector.AssertExpectations(t)
	images.AssertExpectations(t)
	assembler.AssertExpectations(t)
	captured.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestService_Rollback_UnsupportedDaemonLoadsNothing(t *testing.T) {
	// Arrange
	record := demoRecordWithImages()
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Twice()
	locker := &mockLocker{}
	expectLock(locker)
	introspection := demoIntrospectionWithContainers()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	// Restore carries no expectation: the daemon is refused before any layer loads.
	images := &mockImageBackend{}

	captured := &mockCapture{}
	runner := &mockComposeRunner{}
	inflight := &fakeInflight{}
	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: &mockAssembler{},
		SpecCodec:        &mockCodec{},
		ComposeRunner:    runner,
		Capture:          captured,
		BackendRegistry:  &mockRegistry{},
		ImageBackend:     images,
		Inflight:         inflight,
		Store:            newDirStore(t),
		CaptureDefaults:  demoCaptureDefaults,
		DaemonVersion:    "24.0.7",
		MinDaemonMajor:   25,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrUnsupportedDaemon)
	assert.Equal(t, model.RollbackResult{}, res)
	_, marked, err := inflight.Get(ctx)
	require.NoError(t, err)
	assert.False(t, marked)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	images.AssertExpectations(t)
	captured.AssertExpectations(t)
	runner.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestRestoredLayerTag(t *testing.T) {
	const id = model.SavepointID("019e843e-1650-7000-8000-000000000000")
	// A repository path may hold at most 255 characters, and a path component only
	// lower-case alphanumerics joined by separators — neither is arcave's to widen.
	longImage := strings.Repeat("x", 300)

	tests := []struct {
		name    string
		project string
		capture model.WritableLayer
		want    string
	}{
		{
			name:    "project, service, replica and upstream image",
			project: "demo",
			capture: model.WritableLayer{Service: "db", Replica: 1, UpstreamImageRef: "postgres:16"},
			want:    "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000",
		},
		{
			name:    "a registry-qualified upstream image",
			project: "demo",
			capture: model.WritableLayer{Service: "web", Replica: 2, UpstreamImageRef: "ghcr.io/Org/App:1.2.3"},
			want:    "arcave/demo-web-2-ghcr-io-org-app-1-2-3:sp-019e843e-1650-7000-8000-000000000000",
		},
		{
			name:    "an upper-case service, which a repository path refuses",
			project: "Demo_App",
			capture: model.WritableLayer{Service: "DB", Replica: 1, UpstreamImageRef: "busybox"},
			want:    "arcave/demo-app-db-1-busybox:sp-019e843e-1650-7000-8000-000000000000",
		},
		{
			name:    "a capture whose upstream could not be traced is still addressable",
			project: "demo",
			capture: model.WritableLayer{Service: "db", Replica: 1},
			want:    "arcave/demo-db-1:sp-019e843e-1650-7000-8000-000000000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := restoredLayerTag(tt.project, id, tt.capture)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("an upstream image too long for the cap is cut, never the identity", func(t *testing.T) {
		// Act
		got := restoredLayerTag("demo", id, model.WritableLayer{
			Service: "db", Replica: 1, UpstreamImageRef: longImage,
		})

		// Assert
		repository, tag, _ := strings.Cut(got, ":")
		assert.Len(t, repository, 255)
		assert.True(t, strings.HasPrefix(repository, "arcave/demo-db-1-"), repository)
		assert.Equal(t, "sp-019e843e-1650-7000-8000-000000000000", tag)
	})
}

// TestRestoredLayerTag_IsAlwaysNameable pins the ref against docker's own path
// grammar: lower-case alphanumeric runs joined by single dashes is a subset of it,
// so no service or image name can produce something the daemon refuses to tag.
func TestRestoredLayerTag_IsAlwaysNameable(t *testing.T) {
	nameable := regexp.MustCompile(`^arcave/[a-z0-9]+(-[a-z0-9]+)*:sp-[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

	names := []string{"", "___", "..", "-lead", "trail-", "UPPER", "a b/c:d@e", "юникод"}
	bases := append(append([]string{}, names...), strings.Repeat("x", 300))
	for _, project := range names {
		for _, service := range names {
			for _, base := range bases {
				// Act
				got := restoredLayerTag(project, "019e843e-1650-7000-8000-000000000000", model.WritableLayer{
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

func TestNewReplicaPairs(t *testing.T) {
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
			got, err := newReplicaPairs(tt.recorded, tt.created)

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
	restored := []model.WritableLayerRestoreResult{
		{Service: demoService, Replica: 3, Tag: demoRestoredTag(3)},
		{Service: demoService, Replica: 2, Tag: demoRestoredTag(2)},
	}

	// Act
	got := retargetImages(project, restored)

	// Assert
	assert.Equal(t, demoRestoredTag(2), got.Services[demoService].Image)
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
			replicas := make([]recordedReplica, 0, len(tt.recorded))
			for _, n := range tt.recorded {
				ref := demoRestoredTag(n)
				if tt.sameImage {
					ref = demoRestoredTag(tt.recorded[0])
				}
				replicas = append(replicas, recordedReplica{
					service: demoService, number: n, containerName: demoContainerName(n), image: ref,
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
					imageOf(replicas, w.recorded)).
					Return("rebuilt", nil).Once())
			}
			// A name is claimed only once the container holding it has been rebuilt away.
			mock.InOrder(calls...)
			s := New(Deps{SessionLocker: fakeSessionLocker{}, ComposeInspector: inspector, ContainerRebuilder: rebuilder})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := s.rebuildDivergentReplicas(ctx, replicas)

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
	s := New(Deps{SessionLocker: fakeSessionLocker{}, ComposeInspector: inspector, ContainerRebuilder: rebuilder})
	replicas := []recordedReplica{
		{service: demoService, number: 2, image: demoRestoredTag(1)},
		{service: demoService, number: 1, image: demoRestoredTag(1)},
	}

	// Act
	err := s.rebuildDivergentReplicas(zerolog.Nop().WithContext(context.Background()), replicas)

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
		model.ReplicaIdentity{Name: demoContainerName(3), Number: 3}, demoRestoredTag(3)).Return("rebuilt", nil).Once()
	s := New(Deps{SessionLocker: fakeSessionLocker{}, ComposeInspector: inspector, ContainerRebuilder: rebuilder})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	err := s.rebuildDivergentReplicas(ctx, []recordedReplica{
		{service: demoService, number: 1, containerName: demoContainerName(1), image: demoRestoredTag(1)},
		{service: demoService, number: 3, containerName: demoContainerName(3), image: demoRestoredTag(3)},
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

// imageOf is the image recorded replica n is built on.
func imageOf(replicas []recordedReplica, n int) string {
	for _, replica := range replicas {
		if replica.number == n {
			return replica.image
		}
	}
	return ""
}

func TestService_requireImages(t *testing.T) {
	tests := []struct {
		name     string
		policy   string
		restored []model.WritableLayerRestoreResult
		present  *bool
		wantErr  bool
	}{
		{name: "an image compose may pull is not looked up", policy: types.PullPolicyMissing},
		{name: "a service on a restored layer is not looked up", policy: types.PullPolicyNever,
			restored: []model.WritableLayerRestoreResult{{Service: "db"}}},
		{name: "a local image the daemon holds passes", policy: types.PullPolicyNever, present: boolPtr(true)},
		{name: "a local image the daemon lacks is refused", policy: types.PullPolicyNever, present: boolPtr(false), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := &types.Project{Services: types.Services{"db": {Name: "db", Image: "local:1", PullPolicy: tt.policy}}}
			lookup := &mockImageLookup{}
			if tt.present != nil {
				lookup.On("ImageRefExists", mock.Anything, "local:1").Return(*tt.present, nil).Once()
			}
			svc := New(Deps{SessionLocker: fakeSessionLocker{}, ImageLookup: lookup})

			// Act
			err := svc.requireImages(context.Background(), project, tt.restored)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, errImageUnavailable)
			} else {
				require.NoError(t, err)
			}
			lookup.AssertExpectations(t)
		})
	}
}

func boolPtr(v bool) *bool { return &v }

// matchLayerPlan matches a plan taking demoIntrospectionWithContainers' one upstream and its one
// writable layer.
func matchLayerPlan() any {
	layers := []model.WritableLayerCaptureRequest{{
		Container: demoContainerRef(),
		Backend:   model.ImageBackendDedupCopy,
	}}
	return mock.MatchedBy(func(plan capture.Plan) bool {
		return assert.ObjectsAreEqual([]model.ImageCaptureRequest{demoImageCaptureRequest()}, plan.Images) &&
			assert.ObjectsAreEqual(layers, plan.WritableLayers)
	})
}
