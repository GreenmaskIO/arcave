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

package compose

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/captureplan"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// demoRestoredVolumes are the docker names of the volumes a build of demoRecord restores.
var demoRestoredVolumes = []string{demoVolumeName}

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

// demoUndo is a savepoint other than demoRecord, captured of a held project before it is replaced.
func demoUndo() model.Savepoint {
	return model.Savepoint{
		ID:        "019e844b-7818-7000-8000-000000000000",
		CreatedAt: time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		Stats:     []model.SnapshotStat{{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy, Payload: "undo-payload"}},
		Spec:      &model.SpecRef{Format: model.SpecFormatComposeYAML},
	}
}

// A live rollback, walked through the stages in core's order: the capture commits before the
// teardown, and the target's backend, not the live one's, restores.
func TestProvider_Inspect_WalksALiveRollback(t *testing.T) {
	// Arrange
	record := demoRecord()
	deployment := demoDeployment(record)
	introspection := demoIntrospection()
	repo := &composetest.MockSavepointRepo{}
	runner := &composetest.MockComposeRunner{}
	liveBackend := composetest.NewMockBackend(demoVolume())
	targetBackend := composetest.NewMockBackend(demoVolume())
	store := composetest.NewDirStore(t)
	registry := &mockRegistry{}
	inspector := &mockInspector{}
	mock.InOrder(
		inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(liveBackend, nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(targetBackend, nil).Once(),
		repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once(),
		runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once(),
		liveBackend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
			Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once(),
		expectSpecCommit(repo),
		// A fresh id: the capture never commits over the savepoint it is about to be replaced with.
		repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
			return savepoint.ID.Validate() == nil && savepoint.ID != record.ID
		})).Return(nil).Once(),
		runner.On("Down", mock.Anything, demoSpec).Return(nil).Once(),
		runner.On("Create", mock.Anything, deployedFrom(demoTargetSpec, deployment), demoRestoredVolumes).Return(nil).Once(),
		targetBackend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once(),
		runner.On("Start", mock.Anything, deployedFrom(demoTargetSpec, deployment), time.Minute).Return(nil).Once(),
	)
	assembler := &mockAssembler{}
	assembler.On("Assemble", mock.Anything, introspection).Return(demoAssembled, nil).Once()
	codec := &mockCodec{}
	codec.On("Marshal", demoAssembled).Return(demoSpecBytes, nil).Once()
	codec.On("Parse", mock.Anything, demoSpecBytes).Return(demoSpec, nil).Once()
	codec.On("Parse", mock.Anything, demoTargetSpecBytes).Return(demoTargetSpec, nil).Once()
	svc := New(Deps{
		SavepointRepo:    repo,
		ComposeInspector: inspector,
		ComposeRunner:    runner,
		ProjectAssembler: assembler,
		SpecCodec:        codec,
		BackendRegistry:  registry,
		CaptureDefaults:  demoCaptureDefaults,
		VolumeRemover:    &mockVolumeRemover{},
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
	captured, err := held.Capture(ctx, provider.CaptureRequest{})
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
	assert.Equal(t, provider.PreparedSummary{Volumes: []model.Volume{demoVolume()}}, prepared.Summary())
	repo.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	codec.AssertExpectations(t)
	liveBackend.AssertExpectations(t)
	targetBackend.AssertExpectations(t)
}

// A savepoint is prepared before anything is stopped: record, backends, spec, then layers loaded
// and the spec pointed at them; whatever refuses it refuses there, in that order.
func TestLive_PrepareSavepoint(t *testing.T) {
	errLoad := errors.New("daemon refused the archive")
	noSpec := demoRecordWithImages()
	noSpec.Spec = nil
	tests := []struct {
		name      string
		savepoint model.Savepoint
		found     bool
		loadErr   error
		wantErr   error
	}{
		{name: "a savepoint with its layer", savepoint: demoRecordWithImages(), found: true},
		{name: "a record not found", savepoint: demoRecordWithImages(), wantErr: errRecordNotFound},
		{name: "a record with no spec", savepoint: noSpec, found: true, wantErr: model.ErrSavepointNoSpec},
		{name: "a layer the daemon will not take", savepoint: demoRecordWithImages(), found: true, loadErr: errLoad, wantErr: errLoad},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &composetest.MockSavepointRepo{}
			var found []model.Savepoint
			if tt.found {
				found = []model.Savepoint{tt.savepoint}
			}
			calls := []*mock.Call{repo.On("LoadSavepoints", mock.Anything, []model.SavepointID{tt.savepoint.ID}).Return(found, nil).Once()}
			store := composetest.NewDirStore(t)
			registry := &mockRegistry{}
			images := &composetest.MockImageBackend{}
			codec := &mockCodec{}
			if tt.found {
				calls = append(calls, registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(demoVolume()), nil).Once())
			}
			if tt.found && tt.savepoint.Spec != nil {
				calls = append(calls,
					repo.On("LoadSpec", mock.Anything, tt.savepoint.ID).Return(demoTargetSpecBytes, nil).Once(),
					codec.On("Parse", mock.Anything, demoTargetSpecBytes).Return(demoTargetSpec, nil).Once(),
					images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
						SnapshotID: tt.savepoint.ID, Layer: demoWritableLayer(), Tag: demoRestoredTag(1),
					}).Return(demoWritableLayerRestoreResult(), tt.loadErr).Once())
			}
			mock.InOrder(calls...)
			svc := New(Deps{
				SavepointRepo:   repo,
				SpecCodec:       codec,
				BackendRegistry: registry,
				ImageBackend:    images,
				ImageLookup:     &mockImageLookup{},
				Store:           store,
			})
			live := liveWorkload{service: svc, introspection: demoIntrospectionWithContainers()}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			prepared, err := live.PrepareSavepoint(ctx, tt.savepoint.ID, provider.PrepareOptions{})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, prepared)
			} else {
				require.NoError(t, err)
				target := prepared.(preparedSavepoint)
				assert.Equal(t, demoRestoredTag(1), target.project.Services[demoService].Image)
				assert.Equal(t, types.PullPolicyNever, target.project.Services[demoService].PullPolicy)
				assert.Equal(t, []model.WritableLayerRestoreResult{demoWritableLayerRestoreResult()}, prepared.Summary().WritableLayers)
				assert.Equal(t, []model.Volume{demoVolume()}, prepared.Summary().Volumes)
			}
			repo.AssertExpectations(t)
			registry.AssertExpectations(t)
			images.AssertExpectations(t)
			codec.AssertExpectations(t)
		})
	}
}

// Over an absent compose project a leftover volume the savepoint restores into is refused under the
// refusing policy, else named as overwritten; a recovery is never refused.
func TestLive_PrepareSavepoint_LeftoverVolumes(t *testing.T) {
	tests := []struct {
		name       string
		policy     model.LeftoverVolumesPolicy
		recovering bool
		wantErr    error
	}{
		{name: "the default overwrites", wantErr: nil},
		{name: "overwrite", policy: model.LeftoverVolumesOverwrite},
		{name: "refuse", policy: model.LeftoverVolumesRefuse, wantErr: model.ErrLeftoverVolumes},
		{name: "refuse, but a recovery", policy: model.LeftoverVolumesRefuse, recovering: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			repo := &composetest.MockSavepointRepo{}
			read := expectRecordRead(repo, record)
			store := composetest.NewDirStore(t)
			registry := &mockRegistry{}
			codec := &mockCodec{}
			if tt.wantErr == nil {
				mock.InOrder(
					read,
					registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(demoVolume()), nil).Once(),
					repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once(),
					codec.On("Parse", mock.Anything, demoTargetSpecBytes).Return(demoTargetSpec, nil).Once(),
				)
			}
			svc := New(Deps{
				SavepointRepo:   repo,
				SpecCodec:       codec,
				BackendRegistry: registry,
				ImageLookup:     &mockImageLookup{},
				LeftoverVolumes: tt.policy,
				Store:           store,
			})
			live := liveWorkload{service: svc, introspection: demoAbsentIntrospection()}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			prepared, err := live.PrepareSavepoint(ctx, record.ID, provider.PrepareOptions{Recovering: tt.recovering})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.EqualError(t, err, model.ErrLeftoverVolumes.Error()+": demo_db")
			} else {
				require.NoError(t, err)
				assert.Equal(t, []string{"demo_db"}, prepared.Summary().Overwritten)
			}
			repo.AssertExpectations(t)
			registry.AssertExpectations(t)
			codec.AssertExpectations(t)
		})
	}
}

// A live teardown resumes the replicas found frozen, then runs `compose down`; a failed resume is
// only logged, and over an absent compose project it removes nothing.
func TestLive_Teardown(t *testing.T) {
	paused := demoIntrospection()
	paused.Project.Services[0].Containers = []model.Container{{
		ID:     demoContainerID,
		Name:   demoContainerName(1),
		Number: 1,
		Inspect: container.InspectResponse{
			State: &container.State{Status: container.StatePaused, Running: true, Paused: true},
		},
	}}
	tests := []struct {
		name          string
		introspection model.IntrospectionResult
		unpauseErr    error
		downErr       error
		wantWarnings  []string
		wantDown      bool
		wantErr       string
	}{
		{name: "a running project", introspection: demoIntrospection(), wantDown: true},
		{
			name:          "a failed down hands back no vacant project",
			introspection: demoIntrospection(),
			downErr:       errors.New("boom"),
			wantDown:      true,
			wantErr:       "removing the compose project: boom",
		},
		{
			name:          "a frozen replica resumed first",
			introspection: paused,
			wantWarnings:  []string{"db-1 was paused: resuming it before the rollback"},
			wantDown:      true,
		},
		{
			name:          "a resume that fails goes on",
			introspection: paused,
			unpauseErr:    errors.New("boom"),
			wantWarnings:  []string{"db-1 was paused: resuming it before the rollback"},
			wantDown:      true,
		},
		{name: "an absent project", introspection: demoAbsentIntrospection()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &composetest.MockContainerControl{}
			runner := &composetest.MockComposeRunner{}
			var calls []*mock.Call
			if len(tt.wantWarnings) > 0 {
				calls = append(calls, control.On("UnpauseContainers", mock.Anything, []string{demoContainerID}).Return(tt.unpauseErr).Once())
			}
			if tt.wantDown {
				calls = append(calls, runner.On("Down", mock.Anything, demoTargetSpec).Return(tt.downErr).Once())
			}
			mock.InOrder(calls...)
			svc := New(Deps{ComposeRunner: runner, ContainerControl: control})
			live := liveWorkload{service: svc, introspection: tt.introspection}
			recorder := &composetest.StepRecorder{}

			// Act
			vacant, err := live.Teardown(composetest.NewProgressContext(recorder), preparedSavepoint{project: demoTargetSpec})

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, vacant)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, vacant)
			}
			assert.Equal(t, tt.wantWarnings, recorder.Warnings)
			control.AssertExpectations(t)
			runner.AssertExpectations(t)
		})
	}
}

// A held project prepares a savepoint captured of it over the backends that captured it: layers
// loaded and the held project pointed at them, no spec read, no image refused.
func TestHeld_PrepareSavepoint(t *testing.T) {
	// Arrange
	undo := demoUndo()
	undo.WritableLayers = []model.WritableLayer{demoWritableLayer()}
	backend := composetest.NewMockBackend(demoVolume())
	repo := &composetest.MockSavepointRepo{}
	images := &composetest.MockImageBackend{}
	tag := "arcave/demo-db-1-postgres-16:sp-" + undo.ID.String()
	mock.InOrder(
		expectRecordRead(repo, undo),
		images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
			SnapshotID: undo.ID, Layer: demoWritableLayer(), Tag: tag,
		}).Return(model.WritableLayerRestoreResult{Service: demoService, Replica: 1, Tag: tag}, nil).Once(),
	)
	svc := New(Deps{SavepointRepo: repo, ImageBackend: images})
	held := heldWorkload{service: svc, plan: captureplan.Plan{Project: demoTargetSpec, Volumes: []model.VolumeSnapshotBackend{backend}}}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	prepared, err := held.PrepareSavepoint(ctx, undo.ID, provider.PrepareOptions{Deployment: demoDeployment(undo)})

	// Assert
	require.NoError(t, err)
	target := prepared.(preparedSavepoint)
	assert.Equal(t, tag, target.project.Services[demoService].Image)
	assert.Equal(t, []model.VolumeSnapshotBackend{backend}, target.backends)
	assert.Equal(t, demoDeployment(undo), target.deployment)
	repo.AssertExpectations(t)
	images.AssertExpectations(t)
}

// A held project whose `compose down` fails hands back no vacant project.
func TestHeld_Teardown_FailedDown(t *testing.T) {
	// Arrange
	runner := &composetest.MockComposeRunner{}
	runner.On("Down", mock.Anything, demoSpec).Return(errors.New("boom")).Once()
	holding := heldWorkload{
		service: New(Deps{ComposeRunner: runner}),
		plan:    captureplan.Plan{Project: demoSpec},
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
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: "app",
			Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/scratch", VolumeName: "9f8e7d", Replica: 1},
			},
			Containers: []model.Container{{Name: "demo-app-1", Number: 1}},
		}},
	}}
	record := demoRecord()
	record.Stats = []model.SnapshotStat{{Volume: anonVol, Type: model.SnapshotBackendFullCopy, Payload: "sp-payload"}}
	runner := &composetest.MockComposeRunner{}
	remover := &mockVolumeRemover{}
	mock.InOrder(
		runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once(),
		runner.On("Down", mock.Anything, demoSpec).Return(nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(nil).Once(),
	)
	svc := New(Deps{ComposeRunner: runner, VolumeRemover: remover})
	plan := svc.newCapturePlan(captureplan.Plan{Project: demoSpec, Services: []captureplan.ServiceHold{{Service: "app"}}}, introspection)
	ctx := zerolog.Nop().WithContext(context.Background())
	holding, err := plan.Hold(ctx)
	require.NoError(t, err)
	torn, err := holding.Teardown(ctx)
	require.NoError(t, err)
	tornDown, ok := torn.(vacantWorkload)
	require.True(t, ok)

	// Act
	restoredWorkload{vacant: tornDown, target: preparedSavepoint{savepoint: record}}.Reap(ctx)

	// Assert
	runner.AssertExpectations(t)
	remover.AssertExpectations(t)
}

// A held project's teardown is `compose down` of it; a later discard drops only the volumes
// created since it was inspected.
func TestHeld_Teardown(t *testing.T) {
	// Arrange: demo_db the project owned when inspected; demo_cache appeared since.
	introspection := demoIntrospection()
	now := demoAbsentIntrospection()
	now.Project.Volumes = append(now.Project.Volumes, model.NamedVolume{Key: "demo_cache", Name: "demo_cache"})
	runner := &composetest.MockComposeRunner{}
	inspector := &mockInspector{}
	remover := &mockVolumeRemover{}
	mock.InOrder(
		runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once(),
		runner.On("Down", mock.Anything, demoSpec).Return(nil).Once(),
		runner.On("Down", mock.Anything, demoSpec).Return(nil).Once(),
		inspector.On("Introspect", mock.Anything).Return(now, nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "demo_cache").Return(nil).Once(),
	)
	svc := New(Deps{ComposeRunner: runner, ComposeInspector: inspector, VolumeRemover: remover})
	plan := svc.newCapturePlan(captureplan.Plan{Project: demoSpec, Services: []captureplan.ServiceHold{{Service: demoService}}}, introspection)
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

// A build labels every service with the deployment, dated and with its replicas; the caller's
// project stays unlabelled, and the one started is the one created.
func TestVacant_Build_LabelsTheDeployment(t *testing.T) {
	// Arrange: no volume to restore, so only the compose calls carry the deployment.
	target := &types.Project{
		Name: demoProject,
		Services: types.Services{
			"db":  {Name: "db", Image: demoBaseImage},
			"web": {Name: "web", Image: "nginx:1.27"},
		},
		Volumes:  types.Volumes{"db": {Name: demoVolumeName}},
		Networks: types.Networks{"default": {Name: "demo_default"}},
	}
	savepoint := demoRecord()
	savepoint.Stats = nil
	deployment := demoDeployment(savepoint)
	var created, started *types.Project
	runner := &composetest.MockComposeRunner{}
	mock.InOrder(
		runner.On("Create", mock.Anything, mock.MatchedBy(matchDeploymentOf(target)), []string(nil)).
			Run(func(args mock.Arguments) { created = args.Get(1).(*types.Project) }).Return(nil).Once(),
		runner.On("Start", mock.Anything, mock.MatchedBy(matchDeploymentOf(target)), time.Minute).
			Run(func(args mock.Arguments) { started = args.Get(1).(*types.Project) }).Return(nil).Once(),
	)
	svc := New(Deps{ComposeRunner: runner})
	vacant := vacantWorkload{service: svc}
	prepared := preparedSavepoint{savepoint: savepoint, project: target, deployment: deployment}
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
	record := demoScaledRecord()
	restoredLayers := []model.WritableLayerRestoreResult{
		demoWritableLayerRestoreResult(),
		{Service: demoService, Replica: 2, ContainerName: demoContainerName(2), ImageID: demoSecondImageID, Tag: demoRestoredTag(2)},
	}
	project := retargetImages(demoTargetSpec, restoredLayers)
	backend := composetest.NewMockBackend(demoVolume())
	runner := &composetest.MockComposeRunner{}
	inspector := &mockInspector{}
	rebuilder := &mockContainerRebuilder{}
	target := matchRetargetedImage(demoService, demoRestoredTag(1))
	mock.InOrder(
		runner.On("Create", mock.Anything, target, demoRestoredVolumes).Return(nil).Once(),
		inspector.On("Introspect", mock.Anything).Return(demoScaledIntrospection(), nil).Once(),
		rebuilder.On("RecreateWithImage", mock.Anything, demoSecondContainerID,
			model.ReplicaIdentity{Name: demoContainerName(2), Number: 2}, demoRestoredTag(2)).Return("rebuilt", nil).Once(),
		backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once(),
	)
	svc := New(Deps{ComposeRunner: runner, ComposeInspector: inspector, ContainerRebuilder: rebuilder})
	vacant := vacantWorkload{service: svc}
	prepared := preparedSavepoint{
		savepoint:  record,
		project:    project,
		restored:   restoredLayers,
		backends:   []model.VolumeSnapshotBackend{backend},
		deployment: demoDeployment(record),
	}
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

// Every line a volume's restore logs is attributed to the volume, its backend and its savepoint.
func TestBuilt_RestoreData_LogsAreAttributedPerVolume(t *testing.T) {
	// Arrange
	record := demoRecord()
	backend := composetest.NewMockBackend(demoVolume())
	backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
	built := builtWorkload{
		vacant: vacantWorkload{service: New(Deps{})},
		target: preparedSavepoint{savepoint: record, backends: []model.VolumeSnapshotBackend{backend}},
	}
	logs := &composetest.SyncBuffer{}
	ctx := zerolog.New(logs).Level(zerolog.DebugLevel).WithContext(context.Background())

	// Act
	_, err := built.RestoreData(ctx)

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(), `"volume":"volume-named-demo_db"`)
	assert.Contains(t, logs.String(), `"backend":"fullcopy"`)
	assert.Contains(t, logs.String(), `"snapshot_id":"`+record.ID.String()+`"`)
	assert.Contains(t, logs.String(), `"op":"restore"`)
	backend.AssertExpectations(t)
}

// A volume whose restore fails fails the stage, and no restored project is handed back.
func TestBuilt_RestoreData_FailedRestore(t *testing.T) {
	// Arrange
	record := demoRecord()
	backend := composetest.NewMockBackend(demoVolume())
	backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(errors.New("boom")).Once()
	building := builtWorkload{
		vacant: vacantWorkload{service: New(Deps{})},
		target: preparedSavepoint{savepoint: record, backends: []model.VolumeSnapshotBackend{backend}},
	}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	handle, err := building.RestoreData(ctx)

	// Assert
	require.EqualError(t, err, "restore volume-named-demo_db: restore capture: boom")
	assert.Nil(t, handle)
	backend.AssertExpectations(t)
}

// The reap removes the anonymous volumes the teardown orphaned whose data the restore replaced, by
// the docker name they had before it; one it cannot remove is a warning, never a failure.
func TestRestored_Reap(t *testing.T) {
	anonVol := model.Volume{ObjectID: "volume-anon-app-1-scratch", Backend: model.SnapshotBackendFullCopy}
	tests := []struct {
		name      string
		removeErr error
		wantWarn  []string
	}{
		{name: "the orphan is removed"},
		{
			name:      "an orphan that cannot be removed is left with a warning",
			removeErr: errors.New("in use"),
			wantWarn:  []string{"orphaned anonymous volume 9f8e7d left behind: in use"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			record.Stats = []model.SnapshotStat{{Volume: anonVol}, {Volume: demoVolume()}}
			remover := &mockVolumeRemover{}
			remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(tt.removeErr).Once()
			restored := restoredWorkload{
				vacant: vacantWorkload{
					service:              New(Deps{VolumeRemover: remover}),
					anonymousVolumeNames: map[string]string{anonVol.ObjectID: "9f8e7d", "volume-anon-app-1-kept": "kept"},
				},
				target: preparedSavepoint{savepoint: record},
			}
			recorder := &composetest.StepRecorder{}

			// Act
			restored.Reap(composetest.NewProgressContext(recorder))

			// Assert
			assert.Equal(t, tt.wantWarn, recorder.Warnings)
			remover.AssertExpectations(t)
		})
	}
}

// A reap of a build that restored writable layers says where they live; one that restored none says
// nothing.
func TestRestored_Reap_LogsTheRestoredLayers(t *testing.T) {
	tests := []struct {
		name     string
		restored []model.WritableLayerRestoreResult
		wantLog  bool
	}{
		{name: "no layer restored"},
		{
			name:     "layers restored",
			restored: []model.WritableLayerRestoreResult{demoWritableLayerRestoreResult()},
			wantLog:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &composetest.SyncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())
			restored := restoredWorkload{
				vacant: vacantWorkload{service: New(Deps{})},
				target: preparedSavepoint{savepoint: demoRecord(), restored: tt.restored},
			}

			// Act
			restored.Reap(ctx)

			// Assert
			assert.Equal(t, tt.wantLog, strings.Contains(logs.String(), "the containers run on the savepoint's writable layers"))
		})
	}
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
			now := demoAbsentIntrospection()
			now.Project.Volumes = append(now.Project.Volumes, model.NamedVolume{Key: "demo_cache", Name: "demo_cache"})
			runner := &composetest.MockComposeRunner{}
			inspector := &mockInspector{}
			remover := &mockVolumeRemover{}
			mock.InOrder(
				runner.On("Down", mock.Anything, demoTargetSpec).Return(tt.downErr).Once(),
				inspector.On("Introspect", mock.Anything).Return(now, nil).Once(),
				remover.On("RemoveVolume", mock.Anything, "demo_cache").Return(nil).Once(),
			)
			svc := New(Deps{ComposeRunner: runner, ComposeInspector: inspector, VolumeRemover: remover})
			vacant := vacantWorkload{service: svc, ownedBefore: map[string]bool{"demo_db": true}, downProject: demoTargetSpec}

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
			inspector := &mockInspector{}
			inspector.On("Introspect", mock.Anything).Return(demoAbsentIntrospection(), nil).Once()
			svc := New(Deps{ComposeInspector: inspector, DaemonVersion: tt.version, MinDaemonMajor: 25})
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
			assert.Equal(t, []progress.Kind{"test", kindProjectIntrospect}, recorder.Kinds)
			inspector.AssertExpectations(t)
		})
	}
}

// A replica with a restored layer is built on its tag; one the savepoint holds no layer of
// on its service's own image, under the number and name the record keeps for it.
func TestNewRecordedReplicas(t *testing.T) {
	// Arrange
	record := model.Savepoint{NotCovered: &model.NotCovered{WritableLayers: []model.SkippedWritableLayer{
		{Service: "worker", Replica: 1, Container: "demo-worker-1"},
		{Service: "worker", Replica: 3, Container: "demo-worker-3"},
	}}}
	project := &types.Project{Services: types.Services{
		"worker": {Name: "worker", Image: "busybox"},
		"app":    {Name: "app", Image: "app:1"},
	}}
	layered := []model.WritableLayerRestoreResult{{Service: "app", Replica: 2, Tag: "arcave/app:sp-1", ContainerName: "demo-app-2"}}

	// Act
	got := newRecordedReplicas(record, project, layered)

	// Assert
	assert.Equal(t, []recordedReplica{
		{service: "app", number: 2, containerName: "demo-app-2", image: "arcave/app:sp-1"},
		{service: "worker", number: 1, containerName: "demo-worker-1", image: "busybox"},
		{service: "worker", number: 3, containerName: "demo-worker-3", image: "busybox"},
	}, got)
}

// A rollback lets compose recreate only a named volume it restores: a bind or an anonymous
// volume has no config to drift, and a named volume the savepoint left out has no copy.
func TestNewRestoredVolumeNames(t *testing.T) {
	named := model.Volume{ObjectID: "volume-named-demo_db", Named: &model.VolumePropertiesNamed{VolumeName: "demo_db"}}
	bind := model.Volume{ObjectID: "volume-service-app-/src", Bind: &model.VolumePropertiesBind{Service: "app", Target: "/src"}}
	anon := model.Volume{ObjectID: "volume-anon-app-1-/scratch", Anon: &model.VolumePropertiesAnon{Replica: 1}}
	tests := []struct {
		name  string
		stats []model.SnapshotStat
		want  []string
	}{
		{
			name:  "only the named volume, by its docker name",
			stats: []model.SnapshotStat{{Volume: bind}, {Volume: named}, {Volume: anon}},
			want:  []string{"demo_db"},
		},
		{
			name:  "no named volume restored",
			stats: []model.SnapshotStat{{Volume: bind}, {Volume: anon}},
		},
		{
			name: "nothing restored",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newRestoredVolumeNames(model.Savepoint{Stats: tt.stats})

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestVolumesOf pins the projection, and that it is never nil — restored must
// marshal as [], not null.
func TestVolumesOf(t *testing.T) {
	vol := model.Volume{ObjectID: "volume-named-demo_db", Backend: model.SnapshotBackendFullCopy}

	tests := []struct {
		name   string
		record model.Savepoint
		want   []model.Volume
	}{
		{
			name:   "a stat per volume yields a volume per stat",
			record: model.Savepoint{ID: "100", Stats: []model.SnapshotStat{{Volume: vol, Type: model.SnapshotBackendFullCopy}}},
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

// A failed cold-start build is discarded, dropping only the volumes created since inspection,
// never a leftover the savepoint has no copy of.
func TestProvider_Inspect_DiscardsAFailedColdStart(t *testing.T) {
	// Arrange: demo_db outlived the project; demo_cache is the failed build's own.
	record := demoRecord()
	now := demoAbsentIntrospection()
	now.Project.Volumes = append(now.Project.Volumes, model.NamedVolume{Key: "demo_cache", Name: "demo_cache"})
	repo := &composetest.MockSavepointRepo{}
	store := composetest.NewDirStore(t)
	registry := &mockRegistry{}
	codec := &mockCodec{}
	inspector := &mockInspector{}
	runner := &composetest.MockComposeRunner{}
	remover := &mockVolumeRemover{}
	mock.InOrder(
		inspector.On("Introspect", mock.Anything).Return(demoAbsentIntrospection(), nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(demoVolume()), nil).Once(),
		repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once(),
		codec.On("Parse", mock.Anything, demoTargetSpecBytes).Return(demoTargetSpec, nil).Once(),
		runner.On("Create", mock.Anything, deployedFrom(demoTargetSpec, demoDeployment(record)), demoRestoredVolumes).
			Return(errors.New("create boom")).Once(),
		runner.On("Down", mock.Anything, demoTargetSpec).Return(nil).Once(),
		inspector.On("Introspect", mock.Anything).Return(now, nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "demo_cache").Return(nil).Once(),
	)
	svc := New(Deps{
		SavepointRepo:    repo,
		ComposeInspector: inspector,
		ComposeRunner:    runner,
		SpecCodec:        codec,
		BackendRegistry:  registry,
		VolumeRemover:    remover,
		ImageLookup:      &mockImageLookup{},
		Store:            store,
	})
	ctx := zerolog.Nop().WithContext(context.Background())
	live, err := svc.Inspect(ctx)
	require.NoError(t, err)
	prepared, err := live.PrepareSavepoint(ctx, record.ID, provider.PrepareOptions{Deployment: demoDeployment(record)})
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
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: "app",
			Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/scratch", VolumeName: "9f8e7d", Replica: 1},
			},
			Containers: []model.Container{{Name: "demo-app-1", Number: 1}},
		}},
	}}
	record := demoRecord()
	record.Stats = []model.SnapshotStat{{Volume: anonVol, Type: model.SnapshotBackendFullCopy, Payload: "sp-payload"}}
	backend := composetest.NewMockBackend(anonVol)
	repo := &composetest.MockSavepointRepo{}
	store := composetest.NewDirStore(t)
	registry := &mockRegistry{}
	codec := &mockCodec{}
	inspector := &mockInspector{}
	runner := &composetest.MockComposeRunner{}
	remover := &mockVolumeRemover{}
	mock.InOrder(
		inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once(),
		expectRecordRead(repo, record),
		registry.On("New", matchSpec(store, anonVol.ObjectID)).Return(backend, nil).Once(),
		repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once(),
		codec.On("Parse", mock.Anything, demoTargetSpecBytes).Return(demoTargetSpec, nil).Once(),
		runner.On("Down", mock.Anything, demoTargetSpec).Return(nil).Once(),
		runner.On("Create", mock.Anything, deployedFrom(demoTargetSpec, demoDeployment(record)), []string(nil)).Return(nil).Once(),
		backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once(),
		runner.On("Start", mock.Anything, deployedFrom(demoTargetSpec, demoDeployment(record)), time.Minute).Return(nil).Once(),
		remover.On("RemoveVolume", mock.Anything, "9f8e7d").Return(nil).Once(),
	)
	svc := New(Deps{
		SavepointRepo:    repo,
		ComposeInspector: inspector,
		ComposeRunner:    runner,
		SpecCodec:        codec,
		BackendRegistry:  registry,
		VolumeRemover:    remover,
		ImageLookup:      &mockImageLookup{},
		Store:            store,
	})
	ctx := zerolog.Nop().WithContext(context.Background())
	live, err := svc.Inspect(ctx)
	require.NoError(t, err)
	prepared, err := live.PrepareSavepoint(ctx, record.ID, provider.PrepareOptions{Deployment: demoDeployment(record)})
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

// The build's details land under the step the caller opened for them, a line per compose
// operation and per volume restored.
func TestVacant_Build_Progress(t *testing.T) {
	// Arrange
	record := demoRecord()
	backend := composetest.NewMockBackend(demoVolume())
	backend.On("Restore", mock.Anything, newRestoreOf(record)).Return(nil).Once()
	runner := &composetest.MockComposeRunner{}
	runner.On("Down", mock.Anything, demoTargetSpec).Return(nil).Once()
	runner.On("Create", mock.Anything, deployedFrom(demoTargetSpec, demoDeployment(record)), demoRestoredVolumes).Return(nil).Once()
	runner.On("Start", mock.Anything, deployedFrom(demoTargetSpec, demoDeployment(record)), time.Minute).Return(nil).Once()
	svc := New(Deps{ComposeRunner: runner})
	held := heldWorkload{service: svc, plan: captureplan.Plan{Project: demoTargetSpec}}
	prepared := preparedSavepoint{
		savepoint:  record,
		project:    demoTargetSpec,
		backends:   []model.VolumeSnapshotBackend{backend},
		deployment: demoDeployment(record),
	}
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
		"test", kindComposeDown, kindComposeCreate, kindVolumeRestore, kindVolume, kindComposeStart,
	}, recorder.Kinds)
	runner.AssertExpectations(t)
	backend.AssertExpectations(t)
}
