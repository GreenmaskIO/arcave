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
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// A build labels every service with the deployment, dated and with its replicas; the caller's
// project stays unlabelled, and the one started is the one created.
func TestVacant_Build_LabelsTheDeployment(t *testing.T) {
	// Arrange: no volume to restore, so only the compose calls carry the deployment.
	target := &types.Project{
		Name: composetest.DemoProject,
		Services: types.Services{
			"db":  {Name: "db", Image: composetest.DemoBaseImage},
			"web": {Name: "web", Image: "nginx:1.27"},
		},
		Volumes:  types.Volumes{"db": {Name: composetest.DemoVolumeName}},
		Networks: types.Networks{"default": {Name: "demo_default"}},
	}
	savepoint := composetest.DemoRecord()
	savepoint.Stats = nil
	deployment := composetest.DemoDeployment(savepoint)
	var created, started *types.Project
	runner := &composetest.MockComposeRunner{}
	mock.InOrder(
		runner.On("Create", mock.Anything, mock.MatchedBy(composetest.MatchDeploymentOf(target)), []string(nil)).
			Run(func(args mock.Arguments) { created = args.Get(1).(*types.Project) }).Return(nil).Once(),
		runner.On("Start", mock.Anything, mock.MatchedBy(composetest.MatchDeploymentOf(target)), time.Minute).
			Run(func(args mock.Arguments) { started = args.Get(1).(*types.Project) }).Return(nil).Once(),
	)
	svc := newComposeWorkload(Deps{ComposeRunner: runner})
	vacant := vacantWorkload{service: svc}
	prepared := preparedSavepoint{Target: composemodel.Target{Savepoint: savepoint, Project: target, Deployment: deployment}}
	ctx := zerolog.Nop().WithContext(context.Background())
	before := time.Now().UTC().Truncate(time.Second)

	// Act
	built, err := vacant.Build(ctx, prepared)
	require.NoError(t, err)
	restored, err := built.RestoreData(ctx)
	require.NoError(t, err)
	err = restored.Start(ctx, time.Minute)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, created)
	stamped, ok, err := model.ParseSavepointDeployment(created.Services["db"].Labels)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, deployment.ArcaveProject, stamped.ArcaveProject)
	assert.Equal(t, deployment.Session, stamped.Session)
	assert.Equal(t, model.DeployedSavepoint{
		ID:        savepoint.ID,
		Name:      savepoint.Name,
		CreatedAt: savepoint.CreatedAt.UTC().Truncate(time.Second),
	}, stamped.Savepoint)
	assert.Equal(t, map[string][]int{"db": {1}, "web": {1}}, stamped.ServiceReplicas)
	assert.False(t, stamped.DeployedAt.Before(before), "dated when it is built")
	assert.Equal(t, created.Services["db"].Labels, created.Services["web"].Labels, "every service carries the one deployment")
	assert.Equal(t, created, started, "the project started is the one created")
	assert.Nil(t, target.Services["db"].Labels, "the target the caller holds is left unlabelled")
	runner.AssertExpectations(t)
}

// A scaled service is created on its first replica's layer, then its second replica is rebuilt
// on its own: the container set has to be final before a volume is restored into it.
func TestVacant_Build_RebuildsDivergentReplicasBeforeTheRestore(t *testing.T) {
	// Arrange
	record := composetest.DemoScaledRecord()
	restoredLayers := []model.WritableLayerRestoreResult{
		composetest.DemoWritableLayerRestoreResult(),
		{Service: composetest.DemoService, Replica: 2, ContainerName: composetest.DemoContainerName(2), ImageID: composetest.DemoSecondImageID, Tag: composetest.DemoRestoredTag(2)},
	}
	// The target spec as a prepare retargets it: the service on its lowest replica's layer.
	project := &types.Project{Name: composetest.DemoProject, Services: types.Services{
		composetest.DemoService: {Name: composetest.DemoService, Image: composetest.DemoRestoredTag(1), PullPolicy: types.PullPolicyNever},
	}}
	backend := composetest.NewMockBackend(composetest.DemoVolume())
	runner := &composetest.MockComposeRunner{}
	inspector := &composetest.MockInspector{}
	rebuilder := &composetest.MockContainerRebuilder{}
	target := matchRetargetedImage(composetest.DemoService, composetest.DemoRestoredTag(1))
	mock.InOrder(
		runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(nil).Once(),
		inspector.On("Introspect", mock.Anything).Return(composetest.DemoScaledIntrospection(), nil).Once(),
		rebuilder.On("RecreateWithImage", mock.Anything, composetest.DemoSecondContainerID,
			composemodel.ReplicaIdentity{Name: composetest.DemoContainerName(2), Number: 2}, composetest.DemoRestoredTag(2)).Return("rebuilt", nil).Once(),
		backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once(),
	)
	svc := newComposeWorkload(Deps{ComposeRunner: runner, ComposeReader: inspector, ContainerRebuilder: rebuilder})
	vacant := vacantWorkload{service: svc}
	prepared := preparedSavepoint{Target: composemodel.Target{
		Savepoint:  record,
		Project:    project,
		Restored:   restoredLayers,
		Backends:   []model.VolumeSnapshotBackend{backend},
		Deployment: composetest.DemoDeployment(record),
	}}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	built, err := vacant.Build(ctx, prepared)
	require.NoError(t, err)
	_, err = built.RestoreData(ctx)

	// Assert
	require.NoError(t, err)
	runner.AssertExpectations(t)
	inspector.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// The build's details land under the step the caller opened for them, a line per compose
// operation and per volume restored.
func TestVacant_Build_Progress(t *testing.T) {
	// Arrange
	record := composetest.DemoRecord()
	backend := composetest.NewMockBackend(composetest.DemoVolume())
	backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
	runner := &composetest.MockComposeRunner{}
	runner.On("Down", mock.Anything, composetest.DemoTargetSpec).Return(nil).Once()
	runner.On("Create", mock.Anything, composetest.DeployedFrom(composetest.DemoTargetSpec, composetest.DemoDeployment(record)), demoRestoredVolumes).Return(nil).Once()
	runner.On("Start", mock.Anything, composetest.DeployedFrom(composetest.DemoTargetSpec, composetest.DemoDeployment(record)), time.Minute).Return(nil).Once()
	svc := newComposeWorkload(Deps{ComposeRunner: runner})
	held := heldWorkload{service: svc, plan: composemodel.Plan{Project: composetest.DemoTargetSpec}}
	prepared := preparedSavepoint{Target: composemodel.Target{
		Savepoint:  record,
		Project:    composetest.DemoTargetSpec,
		Backends:   []model.VolumeSnapshotBackend{backend},
		Deployment: composetest.DemoDeployment(record),
	}}
	recorder := &composetest.StepRecorder{}
	ctx := composetest.NewProgressContext(recorder)
	torn, err := held.Teardown(ctx)
	require.NoError(t, err)

	// Act
	built, err := torn.Build(ctx, prepared)
	require.NoError(t, err)
	restored, err := built.RestoreData(ctx)
	require.NoError(t, err)
	err = restored.Start(ctx, time.Minute)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []progress.Kind{
		"test", "compose.down", "compose.create", "volume.restore", "volume", "compose.start",
	}, recorder.Kinds)
	runner.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// A discard runs `compose down` of the project and drops the volumes the project owns now and did
// not own before; a down that fails still drops them, and is the error.
func TestVacant_Discard(t *testing.T) {
	errDown := errors.New("down boom")
	tests := []struct {
		name    string
		downErr error
	}{
		{name: "what the build created is removed"},
		{name: "a failed down still drops the volumes", downErr: errDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: demo_db outlived the teardown and stays; demo_cache is the build's own.
			now := composetest.DemoAbsentIntrospection()
			now.Project.Volumes = append(now.Project.Volumes, composemodel.NamedVolume{Key: "demo_cache", Name: "demo_cache"})
			runner := &composetest.MockComposeRunner{}
			inspector := &composetest.MockInspector{}
			remover := &composetest.MockVolumeRemover{}
			mock.InOrder(
				runner.On("Down", mock.Anything, composetest.DemoTargetSpec).Return(tt.downErr).Once(),
				inspector.On("ReadResources", mock.Anything).Return(composetest.ResourcesOfVolumes(now), nil).Once(),
				remover.On("RemoveVolume", mock.Anything, "demo_cache").Return(nil).Once(),
			)
			svc := newComposeWorkload(Deps{ComposeRunner: runner, ComposeReader: inspector, VolumeRemover: remover})
			vacant := vacantWorkload{service: svc, ownedBefore: map[string]bool{"demo_db": true}, downProject: composetest.DemoTargetSpec}

			// Act
			err := vacant.Discard(zerolog.Nop().WithContext(context.Background()))

			// Assert
			if tt.downErr != nil {
				require.ErrorIs(t, err, errDown)
				assert.EqualError(t, err, "removing the compose project: down boom")
			} else {
				require.NoError(t, err)
			}
			runner.AssertExpectations(t)
			inspector.AssertExpectations(t)
			remover.AssertExpectations(t)
		})
	}
}
