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
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// The containers the hold's stop killed reach the committed record.
func TestHeld_Capture(t *testing.T) {
	// Arrange
	runner := &composetest.MockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, composetest.DemoSpec).Return([]string{composetest.DemoContainerID}, nil).Once()
	repo := &composetest.MockSavepointRepo{}
	spec := composetest.ExpectSpecCommit(repo)
	var committed model.Savepoint
	commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return savepoint.ID.Validate() == nil
	})).Run(func(args mock.Arguments) { committed = args.Get(1).(model.Savepoint) }).Return(nil).Once()
	mock.InOrder(stop, spec, commit)
	svc := newComposeWorkload(Deps{
		SavepointRepo: repo,
		ComposeRunner: runner,
		Store:         composetest.NewDirStore(t),
	})
	plan := composemodel.Plan{
		Project:    composetest.DemoSpec,
		Spec:       composetest.DemoSpecBytes,
		Services:   stopServices,
		Containers: []composemodel.ContainerRef{composetest.DemoContainerRef()},
	}
	ctx := composetest.NewTestContext()
	holding, err := svc.newCapturePlan(plan, composemodel.IntrospectionResult{}).Hold(ctx)
	require.NoError(t, err)

	// Act
	captured, err := holding.Capture(ctx, provider.CaptureRequest{ID: model.NewSavepointID(), Name: "sp"})
	require.NoError(t, err)
	_, err = captured.Commit(ctx)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, committed.NotCovered)
	assert.Equal(t, []model.KilledContainer{{Service: composetest.DemoService, Replica: 1, Container: "demo-db-1"}},
		committed.NotCovered.KilledContainers)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A held project prepares a savepoint captured of it over the backends that captured it: layers
// loaded and the held project pointed at them, no spec read, no image refused.
func TestHeld_PrepareSavepoint(t *testing.T) {
	// Arrange
	undo := demoUndo()
	undo.WritableLayers = []model.WritableLayer{composetest.DemoWritableLayer()}
	backend := composetest.NewMockBackend(composetest.DemoVolume())
	repo := &composetest.MockSavepointRepo{}
	images := &composetest.MockImageBackend{}
	tag := "arcave/demo-db-1-postgres-16:sp-" + undo.ID.String()
	mock.InOrder(
		expectRecordRead(repo, undo),
		images.On("RestoreWritableLayer", mock.Anything, composemodel.WritableLayerRestoreRequest{
			SnapshotID: undo.ID, Layer: composetest.DemoWritableLayer(), Tag: tag,
		}).Return(model.WritableLayerRestoreResult{Service: composetest.DemoService, Replica: 1, Tag: tag}, nil).Once(),
	)
	svc := newComposeWorkload(Deps{SavepointRepo: repo, ImageBackend: images})
	held := heldWorkload{service: svc, plan: composemodel.Plan{Project: composetest.DemoTargetSpec, Volumes: []model.VolumeSnapshotBackend{backend}}}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	prepared, err := held.PrepareSavepoint(ctx, undo.ID, provider.PrepareOptions{Deployment: composetest.DemoDeployment(undo)})

	// Assert
	require.NoError(t, err)
	target := prepared.(preparedSavepoint)
	assert.Equal(t, tag, target.Project.Services[composetest.DemoService].Image)
	assert.Equal(t, []model.VolumeSnapshotBackend{backend}, target.Backends)
	assert.Equal(t, composetest.DemoDeployment(undo), target.Deployment)
	repo.AssertExpectations(t)
	images.AssertExpectations(t)
}

// A held project's teardown is `compose down` of it; a later discard drops only the volumes
// created since it was inspected.
func TestHeld_Teardown(t *testing.T) {
	// Arrange: demo_db the project owned when inspected; demo_cache appeared since.
	introspection := composetest.DemoIntrospection()
	now := composetest.DemoAbsentIntrospection()
	now.Project.Volumes = append(now.Project.Volumes, composemodel.NamedVolume{Key: "demo_cache", Name: "demo_cache"})
	runner := &composetest.MockComposeRunner{}
	inspector := &composetest.MockInspector{}
	remover := &composetest.MockVolumeRemover{}
	mock.InOrder(
		runner.On("Stop", mock.Anything, composetest.DemoSpec).Return([]string(nil), nil).Once(),
		runner.On("Down", mock.Anything, composetest.DemoSpec).Return(nil).Once(),
		runner.On("Down", mock.Anything, composetest.DemoSpec).Return(nil).Once(),
		inspector.On("ReadResources", mock.Anything).Return(composetest.ResourcesOfVolumes(now), nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "demo_cache").Return(nil).Once(),
	)
	svc := newComposeWorkload(Deps{ComposeRunner: runner, ComposeReader: inspector, VolumeRemover: remover})
	plan := svc.newCapturePlan(composemodel.Plan{Project: composetest.DemoSpec, Services: []composemodel.ServiceHold{{Service: composetest.DemoService}}}, introspection)
	ctx := zerolog.Nop().WithContext(context.Background())
	holding, err := plan.Hold(ctx)
	require.NoError(t, err)

	// Act
	torn, err := holding.Teardown(ctx)
	require.NoError(t, err)
	discardErr := torn.Discard(ctx)

	// Assert
	require.NoError(t, discardErr)
	runner.AssertExpectations(t)
	inspector.AssertExpectations(t)
	remover.AssertExpectations(t)
}

// A held project whose `compose down` fails hands back no vacant project.
func TestHeld_Teardown_FailedDown(t *testing.T) {
	// Arrange
	runner := &composetest.MockComposeRunner{}
	runner.On("Down", mock.Anything, composetest.DemoSpec).Return(errors.New("boom")).Once()
	holding := heldWorkload{
		service: newComposeWorkload(Deps{ComposeRunner: runner}),
		plan:    composemodel.Plan{Project: composetest.DemoSpec},
	}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	torn, err := holding.Teardown(ctx)

	// Assert
	require.EqualError(t, err, "removing the compose project: boom")
	assert.Nil(t, torn)
	runner.AssertExpectations(t)
}

// A held project's teardown carries the anonymous volumes the project mounted when it was
// inspected: the reap past a rollback that took a safety snapshot, or past its revert, removes them.
func TestHeld_Teardown_CarriesTheAnonymousVolumes(t *testing.T) {
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
	runner := &composetest.MockComposeRunner{}
	remover := &composetest.MockVolumeRemover{}
	mock.InOrder(
		runner.On("Stop", mock.Anything, composetest.DemoSpec).Return([]string(nil), nil).Once(),
		runner.On("Down", mock.Anything, composetest.DemoSpec).Return(nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(nil).Once(),
	)
	svc := newComposeWorkload(Deps{ComposeRunner: runner, VolumeRemover: remover})
	plan := svc.newCapturePlan(composemodel.Plan{Project: composetest.DemoSpec, Services: []composemodel.ServiceHold{{Service: "app"}}}, introspection)
	ctx := zerolog.Nop().WithContext(context.Background())
	holding, err := plan.Hold(ctx)
	require.NoError(t, err)
	torn, err := holding.Teardown(ctx)
	require.NoError(t, err)
	tornDown, ok := torn.(vacantWorkload)
	require.True(t, ok)

	// Act
	restoredWorkload{vacant: tornDown, target: preparedSavepoint{Target: composemodel.Target{Savepoint: record}}}.Reap(ctx)

	// Assert
	runner.AssertExpectations(t)
	remover.AssertExpectations(t)
}
