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

package workload

import (
	"context"
	"errors"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// demoRestoredVolumes are the docker names of the volumes a build of composetest.DemoRecord restores.
var demoRestoredVolumes = []string{composetest.DemoVolumeName}

// matchRetargetedImage matches a project by the image one of its services is pointed at, with
// pull policy never: the tag lives only in the local daemon.
func matchRetargetedImage(service, ref string) any {
	return mock.MatchedBy(func(project *types.Project) bool {
		svc, ok := project.Services[service]
		return ok && svc.Image == ref && svc.PullPolicy == types.PullPolicyNever
	})
}

// expectRecordRead declares the read of savepoint's record by id.
func expectRecordRead(repo *composetest.MockSavepointRepo, savepoint model.Savepoint) *mock.Call {
	return repo.On("LoadSavepoints", mock.Anything, []model.SavepointID{savepoint.ID}).
		Return([]model.Savepoint{savepoint}, nil).Once()
}

// newRestoreOf is the restore request of savepoint's one volume.
func newRestoreOf(savepoint model.Savepoint) model.BackendRestoreRequest {
	return model.BackendRestoreRequest{
		SnapshotID: savepoint.ID,
		Payload:    savepoint.Stats[0].Payload,
		SizeBytes:  savepoint.Stats[0].SizeBytes,
	}
}

// demoUndo is a savepoint other than composetest.DemoRecord, captured of a held project before it is replaced.
func demoUndo() model.Savepoint {
	return model.Savepoint{
		ID:        "019e844b-7818-7000-8000-000000000000",
		CreatedAt: time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		Stats:     []model.SnapshotStat{{Volume: composetest.DemoVolume(), Type: model.SnapshotBackendFullCopy, Payload: "undo-payload"}},
		Spec:      &model.SpecRef{Format: model.SpecFormatComposeYAML},
	}
}

// Inspect reads the live compose project under its own step and refuses a daemon older than the
// floor; a version it cannot read passes.
func TestProvider_Inspect(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr error
	}{
		{name: "a daemon new enough", version: "27.3.1"},
		{name: "an old daemon", version: "24.0.7", wantErr: model.ErrUnsupportedDaemon},
		{name: "a version that cannot be read", version: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			inspector := &composetest.MockInspector{}
			inspector.On("Introspect", mock.Anything).Return(composetest.DemoAbsentIntrospection(), nil).Once()
			svc := newComposeWorkload(Deps{ComposeReader: inspector, DaemonVersion: tt.version, MinDaemonMajor: 25})
			recorder := &composetest.StepRecorder{}

			// Act
			live, err := svc.Inspect(composetest.NewProgressContext(recorder))

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, live)
			} else {
				require.NoError(t, err)
				assert.True(t, live.Absent())
			}
			assert.Equal(t, []progress.Kind{"test", "project.introspect"}, recorder.Kinds)
			inspector.AssertExpectations(t)
		})
	}
}

// A live rollback, walked through the stages in core's order: the capture commits before the
// teardown, and the target's backend, not the live one's, restores.
func TestProvider_Inspect_WalksALiveRollback(t *testing.T) {
	// Arrange
	record := composetest.DemoRecord()
	deployment := composetest.DemoDeployment(record)
	introspection := composetest.DemoIntrospection()
	repo := &composetest.MockSavepointRepo{}
	runner := &composetest.MockComposeRunner{}
	liveBackend := composetest.NewMockBackend(composetest.DemoVolume())
	targetBackend := composetest.NewMockBackend(composetest.DemoVolume())
	store := composetest.NewDirStore(t)
	registry := &composetest.MockRegistry{}
	inspector := &composetest.MockInspector{}
	mock.InOrder(
		inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", composetest.MatchSpec(store, "volume-named-demo_db")).Return(liveBackend, nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", composetest.MatchSpec(store, "volume-named-demo_db")).Return(targetBackend, nil).Once(),
		repo.On("LoadSpec", mock.Anything, record.ID).Return(composetest.DemoTargetSpecBytes, nil).Once(),
		runner.On("Stop", mock.Anything, composetest.DemoSpec).Return([]string(nil), nil).Once(),
		liveBackend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
			Return(model.SnapshotStat{Volume: composetest.DemoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once(),
		composetest.ExpectSpecCommit(repo),
		// A fresh id: the capture never commits over the savepoint it is about to be replaced with.
		repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
			return savepoint.ID.Validate() == nil && savepoint.ID != record.ID
		})).Return(nil).Once(),
		runner.On("Down", mock.Anything, composetest.DemoSpec).Return(nil).Once(),
		runner.On("Create", mock.Anything, composetest.DeployedFrom(composetest.DemoTargetSpec, deployment), demoRestoredVolumes).Return(nil).Once(),
		targetBackend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once(),
		runner.On("Start", mock.Anything, composetest.DeployedFrom(composetest.DemoTargetSpec, deployment), time.Minute).Return(nil).Once(),
	)
	assembler := &composetest.MockAssembler{}
	assembler.On("Assemble", mock.Anything, introspection).Return(composetest.DemoAssembled, nil).Once()
	codec := &composetest.MockCodec{}
	codec.On("Marshal", composetest.DemoAssembled).Return(composetest.DemoSpecBytes, nil).Once()
	codec.On("Parse", mock.Anything, composetest.DemoSpecBytes).Return(composetest.DemoSpec, nil).Once()
	codec.On("Parse", mock.Anything, composetest.DemoTargetSpecBytes).Return(composetest.DemoTargetSpec, nil).Once()
	svc := newComposeWorkload(Deps{
		SavepointRepo:    repo,
		ComposeReader:    inspector,
		ComposeRunner:    runner,
		ProjectAssembler: assembler,
		SpecCodec:        codec,
		BackendRegistry:  registry,
		CaptureDefaults:  composetest.DemoCaptureDefaults,
		VolumeRemover:    &composetest.MockVolumeRemover{},
		Store:            store,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	live, err := svc.Inspect(ctx)
	require.NoError(t, err)
	plan, err := live.PlanCapture(ctx, provider.CaptureOptions{Restoring: record.ID})
	require.NoError(t, err)
	prepared, err := live.PrepareSavepoint(ctx, record.ID, provider.PrepareOptions{Deployment: deployment})
	require.NoError(t, err)
	held, err := plan.Hold(ctx)
	require.NoError(t, err)
	captured, err := held.Capture(ctx, provider.CaptureRequest{ID: model.NewSavepointID()})
	require.NoError(t, err)
	_, err = captured.Commit(ctx)
	require.NoError(t, err)
	vacant, err := held.Teardown(ctx)
	require.NoError(t, err)
	built, err := vacant.Build(ctx, prepared)
	require.NoError(t, err)
	restored, err := built.RestoreData(ctx)
	require.NoError(t, err)
	err = restored.Start(ctx, time.Minute)
	restored.Reap(ctx)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, provider.PreparedSummary{Volumes: []model.Volume{composetest.DemoVolume()}}, prepared.Summary())
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	codec.AssertExpectations(t)
	liveBackend.AssertExpectations(t)
	targetBackend.AssertExpectations(t)
}

// A failed cold-start build is discarded, dropping only the volumes created since inspection,
// never a leftover the savepoint has no copy of.
func TestProvider_Inspect_DiscardsAFailedColdStart(t *testing.T) {
	// Arrange: demo_db outlived the project; demo_cache is the failed build's own.
	record := composetest.DemoRecord()
	now := composetest.DemoAbsentIntrospection()
	now.Project.Volumes = append(now.Project.Volumes, composemodel.NamedVolume{Key: "demo_cache", Name: "demo_cache"})
	repo := &composetest.MockSavepointRepo{}
	store := composetest.NewDirStore(t)
	registry := &composetest.MockRegistry{}
	codec := &composetest.MockCodec{}
	inspector := &composetest.MockInspector{}
	runner := &composetest.MockComposeRunner{}
	remover := &composetest.MockVolumeRemover{}
	mock.InOrder(
		inspector.On("Introspect", mock.Anything).Return(composetest.DemoAbsentIntrospection(), nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", composetest.MatchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(composetest.DemoVolume()), nil).Once(),
		repo.On("LoadSpec", mock.Anything, record.ID).Return(composetest.DemoTargetSpecBytes, nil).Once(),
		codec.On("Parse", mock.Anything, composetest.DemoTargetSpecBytes).Return(composetest.DemoTargetSpec, nil).Once(),
		runner.On("Create", mock.Anything, composetest.DeployedFrom(composetest.DemoTargetSpec, composetest.DemoDeployment(record)), demoRestoredVolumes).
			Return(errors.New("create boom")).Once(),
		runner.On("Down", mock.Anything, composetest.DemoTargetSpec).Return(nil).Once(),
		// The narrow read: a container left half-built must not hide the volumes.
		inspector.On("ReadResources", mock.Anything).Return(composetest.ResourcesOfVolumes(now), nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "demo_cache").Return(nil).Once(),
	)
	svc := newComposeWorkload(Deps{
		SavepointRepo:   repo,
		ComposeReader:   inspector,
		ComposeRunner:   runner,
		SpecCodec:       codec,
		BackendRegistry: registry,
		VolumeRemover:   remover,
		ImageLookup:     &composetest.MockImageLookup{},
		Store:           store,
	})
	ctx := zerolog.Nop().WithContext(context.Background())
	live, err := svc.Inspect(ctx)
	require.NoError(t, err)
	prepared, err := live.PrepareSavepoint(ctx, record.ID, provider.PrepareOptions{Deployment: composetest.DemoDeployment(record)})
	require.NoError(t, err)
	vacant, err := live.Teardown(ctx, prepared)
	require.NoError(t, err)

	// Act
	_, buildErr := vacant.Build(ctx, prepared)
	discardErr := vacant.Discard(ctx)

	// Assert
	require.ErrorContains(t, buildErr, "create boom")
	require.NoError(t, discardErr)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	codec.AssertExpectations(t)
	inspector.AssertExpectations(t)
	runner.AssertExpectations(t)
	remover.AssertExpectations(t)
}

// A rollback without a capture, walked through the stages: the anonymous volume the live project
// mounted is reaped by the docker name it had before the teardown, once the restore replaced it.
func TestProvider_Inspect_ReapsTheOrphanedAnonymousVolume(t *testing.T) {
	// Arrange
	anonVol := model.Volume{ObjectID: "volume-anon-app-1-scratch", Backend: model.SnapshotBackendFullCopy}
	introspection := composemodel.IntrospectionResult{Project: composemodel.Project{
		Name: composetest.DemoProject,
		Services: []composemodel.Service{{
			Name: "app",
			Volumes: []composemodel.ServiceVolume{
				{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/scratch", VolumeName: "9f8e7d", Replica: 1},
			},
			Containers: []composemodel.Container{{Name: "demo-app-1", Number: 1}},
		}},
	}}
	record := composetest.DemoRecord()
	record.Stats = []model.SnapshotStat{{Volume: anonVol, Type: model.SnapshotBackendFullCopy, Payload: "sp-payload"}}
	backend := composetest.NewMockBackend(anonVol)
	repo := &composetest.MockSavepointRepo{}
	store := composetest.NewDirStore(t)
	registry := &composetest.MockRegistry{}
	codec := &composetest.MockCodec{}
	inspector := &composetest.MockInspector{}
	runner := &composetest.MockComposeRunner{}
	remover := &composetest.MockVolumeRemover{}
	mock.InOrder(
		inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", composetest.MatchSpec(store, anonVol.ObjectID)).Return(backend, nil).Once(),
		repo.On("LoadSpec", mock.Anything, record.ID).Return(composetest.DemoTargetSpecBytes, nil).Once(),
		codec.On("Parse", mock.Anything, composetest.DemoTargetSpecBytes).Return(composetest.DemoTargetSpec, nil).Once(),
		runner.On("Down", mock.Anything, composetest.DemoTargetSpec).Return(nil).Once(),
		runner.On("Create", mock.Anything, composetest.DeployedFrom(composetest.DemoTargetSpec, composetest.DemoDeployment(record)), []string(nil)).Return(nil).Once(),
		backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once(),
		runner.On("Start", mock.Anything, composetest.DeployedFrom(composetest.DemoTargetSpec, composetest.DemoDeployment(record)), time.Minute).Return(nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(nil).Once(),
	)
	svc := newComposeWorkload(Deps{
		SavepointRepo:   repo,
		ComposeReader:   inspector,
		ComposeRunner:   runner,
		SpecCodec:       codec,
		BackendRegistry: registry,
		VolumeRemover:   remover,
		ImageLookup:     &composetest.MockImageLookup{},
		Store:           store,
	})
	ctx := zerolog.Nop().WithContext(context.Background())
	live, err := svc.Inspect(ctx)
	require.NoError(t, err)
	prepared, err := live.PrepareSavepoint(ctx, record.ID, provider.PrepareOptions{Deployment: composetest.DemoDeployment(record)})
	require.NoError(t, err)
	vacant, err := live.Teardown(ctx, prepared)
	require.NoError(t, err)
	built, err := vacant.Build(ctx, prepared)
	require.NoError(t, err)
	restored, err := built.RestoreData(ctx)
	require.NoError(t, err)
	require.NoError(t, restored.Start(ctx, time.Minute))

	// Act
	restored.Reap(ctx)

	// Assert
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	codec.AssertExpectations(t)
	inspector.AssertExpectations(t)
	runner.AssertExpectations(t)
	backend.AssertExpectations(t)
	remover.AssertExpectations(t)
}
