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
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// demoReplica is db's first replica as ReadResources reports it: no deployment labels.
func demoReplica() model.Container {
	return model.Container{ID: demoContainerID, Name: demoContainerName(1), Number: 1, Labels: map[string]string{}}
}

// demoResources is a running compose project: db's replica, its volume and its network.
func demoResources() model.ComposeProjectResources {
	return model.ComposeProjectResources{
		Containers: []string{demoContainerName(1)},
		Replicas: model.ComposeProjectContainers{Services: []model.ServiceContainers{{
			Name: demoService, Containers: []model.Container{demoReplica()},
		}}},
		Volumes:  []string{demoVolumeName},
		Networks: []string{demoProject + "_default"},
	}
}

// upDeps wires a Service over a compose project whose resources ReadResources reports; the
// inspector has no Introspect expectation, so an up that introspects fails the test.
func upDeps(t *testing.T, records []model.Savepoint, resources model.ComposeProjectResources) (Deps, *mockSavepointRepo, *mockInspector, *mock.Call) {
	t.Helper()
	deps, _, _ := coldStartDeps(t, demoColdRecord())
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return(records, nil).Once()
	inspector := &mockInspector{}
	read := inspector.On("ReadResources", mock.Anything).Return(resources, nil).Once()
	deps.SavepointRepo, deps.ComposeInspector, deps.ComposeProject = repo, inspector, demoProject
	return deps, repo, inspector, read
}

// isTargetProject matches the target spec retargeted at the restored layer.
func isTargetProject() any {
	return mock.MatchedBy(func(project *types.Project) bool {
		service, ok := project.Services[demoService]
		return ok && service.Image == demoRestoredTag(1)
	})
}

// Over a daemon that holds nothing of the compose project, up is a cold start of the newest
// savepoint: no selector is latest, and nothing is dropped.
func TestService_Up_BuildsOverNothing(t *testing.T) {
	// Arrange
	older := demoColdRecord()
	older.ID, older.Name, older.CreatedAt = "019e843e-1650-7000-8000-0000000000aa", "older", older.CreatedAt.Add(-time.Hour)
	record := demoColdRecord()
	deps, repo, inspector, read := upDeps(t, []model.Savepoint{record, older}, model.ComposeProjectResources{})
	loadSpec := repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
	images := &mockImageBackend{}
	restoreImage := images.On("RestoreWritableLayer", mock.Anything, model.WritableLayerRestoreRequest{
		SnapshotID: record.ID, Layer: demoWritableLayer(), Tag: demoRestoredTag(1),
	}).Return(demoWritableLayerRestoreResult(), nil).Once()
	runner := &mockComposeRunner{}
	create := runner.On("Create", mock.Anything, isTargetProject(), demoRestoredVolumes).Return(nil).Once()
	vol := record.Stats[0].Volume
	backend := &mockBackend{volume: vol}
	restore := backend.On("Restore", mock.Anything, mock.Anything).Return(nil).Once()
	start := runner.On("Start", mock.Anything, isTargetProject(), time.Minute).Return(nil).Once()
	mock.InOrder(read, loadSpec, restoreImage, create, restore, start)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(deps.Store, vol.ObjectID)).Return(backend, nil).Once()
	deps.ImageBackend, deps.ComposeRunner, deps.BackendRegistry = images, runner, registry
	svc := New(deps)

	// Act
	res, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{WaitTimeout: time.Minute})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, record.ID, res.RolledBackTo.ID, "the newest savepoint")
	assert.Equal(t, []model.Volume{vol}, res.Restored)
	assert.Nil(t, res.Discarded)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	images.AssertExpectations(t)
	runner.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// What the daemon still holds refuses the up right after the read: nothing is loaded, marked or
// created, so the image backend, the registry and the runner carry no expectation.
func TestService_Up_RefusesAPresentComposeProject(t *testing.T) {
	tests := []struct {
		name      string
		resources model.ComposeProjectResources
		want      *model.ComposeProjectPresentError
	}{
		{
			name:      "a running compose project",
			resources: demoResources(),
			want:      &model.ComposeProjectPresentError{Containers: []string{demoContainerName(1)}, Volumes: []string{demoVolumeName}},
		},
		{
			name:      "a volume the user's compose down kept",
			resources: model.ComposeProjectResources{Volumes: []string{demoVolumeName}},
			want:      &model.ComposeProjectPresentError{Volumes: []string{demoVolumeName}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			deps, repo, inspector, _ := upDeps(t, []model.Savepoint{demoColdRecord()}, tt.resources)
			images, runner, registry, inflight := &mockImageBackend{}, &mockComposeRunner{}, &mockRegistry{}, &fakeInflight{}
			deps.ImageBackend, deps.ComposeRunner, deps.BackendRegistry, deps.Inflight = images, runner, registry, inflight
			svc := New(deps)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := svc.Up(ctx, model.UpRequest{})

			// Assert
			require.ErrorIs(t, err, model.ErrComposeProjectPresent)
			var present *model.ComposeProjectPresentError
			require.ErrorAs(t, err, &present)
			assert.Equal(t, tt.want, present)
			_, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			assert.False(t, marked)
			repo.AssertExpectations(t)
			inspector.AssertExpectations(t)
			images.AssertExpectations(t)
			runner.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}

// A forced up over a running compose project drops it whole, volumes included, after the layers
// are loaded and under the marker, then builds the savepoint; the result names what went.
// The inspector has no Introspect: a broken environment a full read would trip on does not stop it.
func TestService_Up_ForceDropsWhatIsThere(t *testing.T) {
	// Arrange
	record := demoColdRecord()
	deps, repo, inspector, read := upDeps(t, []model.Savepoint{record}, demoResources())
	loadSpec := repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
	images := &mockImageBackend{}
	restoreImage := images.On("RestoreWritableLayer", mock.Anything, mock.Anything).
		Return(demoWritableLayerRestoreResult(), nil).Once()
	inflight := &mockInflight{}
	gate := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	putRecreating := inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRecreating)).Return(nil).Once()
	inflight.On("Put", mock.Anything, mock.Anything).Return(nil)
	inflight.On("Clear", mock.Anything).Return(nil).Once()
	runner := &mockComposeRunner{}
	drop := runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
	gone := inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()
	create := runner.On("Create", mock.Anything, isTargetProject(), demoRestoredVolumes).Return(nil).Once()
	start := runner.On("Start", mock.Anything, isTargetProject(), time.Duration(0)).Return(nil).Once()
	mock.InOrder(gate, read, loadSpec, restoreImage, putRecreating, drop, gone, create, start)
	vol := record.Stats[0].Volume
	backend := &mockBackend{volume: vol}
	backend.On("Restore", mock.Anything, mock.Anything).Return(nil).Once()
	registry := &mockRegistry{}
	registry.On("New", matchSpec(deps.Store, vol.ObjectID)).Return(backend, nil).Once()
	deps.ImageBackend, deps.ComposeRunner, deps.BackendRegistry, deps.Inflight = images, runner, registry, inflight
	svc := New(deps)

	// Act
	res, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{Force: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, &model.DiscardedResources{
		Containers: []string{demoContainerName(1)},
		Volumes:    []string{demoVolumeName},
		Networks:   []string{demoProject + "_default"},
	}, res.Discarded)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	runner.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// A forced up that fails past the marker removes the target it built and every volume of the
// compose project: nothing of the old one is owed any. The marker, still recreating, clears.
func TestService_Up_ForceFailureDropsEveryVolume(t *testing.T) {
	tests := []struct {
		name      string
		dropErr   error
		createErr error
	}{
		{name: "the drop fails", dropErr: errors.New("network in use")},
		{name: "the create fails", createErr: errors.New("port is taken")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoColdRecord()
			deps, repo, inspector, _ := upDeps(t, []model.Savepoint{record}, demoResources())
			repo.On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
			images := &mockImageBackend{}
			images.On("RestoreWritableLayer", mock.Anything, mock.Anything).Return(demoWritableLayerRestoreResult(), nil).Once()
			runner := &mockComposeRunner{}
			drop := runner.On("DownWithVolumes", mock.Anything, demoProject).Return(tt.dropErr).Once()
			calls := []*mock.Call{drop}
			if tt.dropErr == nil {
				gone := inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()
				create := runner.On("Create", mock.Anything, isTargetProject(), demoRestoredVolumes).Return(tt.createErr).Once()
				calls = append(calls, gone, create)
			}
			revert := runner.On("Down", mock.Anything, isTargetProject()).Return(nil).Once()
			// The narrow read after the revert finds the volume the drop left, or the create made.
			reread := inspector.On("ReadResources", mock.Anything).
				Return(model.ComposeProjectResources{Volumes: []string{demoVolumeName}}, nil).Once()
			remover := &mockVolumeRemover{}
			remove := remover.On("RemoveVolume", mock.Anything, demoVolumeName).Return(nil).Once()
			mock.InOrder(append(calls, revert, reread, remove)...)
			inflight := &fakeInflight{}
			deps.ImageBackend, deps.ComposeRunner, deps.VolumeRemover, deps.Inflight = images, runner, remover, inflight
			deps.BackendRegistry = &mockRegistry{}
			deps.BackendRegistry.(*mockRegistry).On("New", mock.Anything).Return(&mockBackend{volume: record.Stats[0].Volume}, nil).Once()
			svc := New(deps)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := svc.Up(ctx, model.UpRequest{Force: true})

			// Assert
			require.Error(t, err)
			_, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			assert.False(t, marked, "nothing was restored yet, so the marker clears")
			runner.AssertExpectations(t)
			inspector.AssertExpectations(t)
			remover.AssertExpectations(t)
		})
	}
}

// A forced up proves the drop before it builds: a volume compose's down passed over is removed by
// its docker name, and one something outside the compose project still holds refuses the build —
// nothing is created, nothing restored, and the volume with its old data is never reused.
func TestService_Up_ForceProvesTheDrop(t *testing.T) {
	busy := errors.New("remove volume demo_db: volume is in use - [f00d]")
	tests := []struct {
		name    string
		left    []string
		removed map[string]error
		wantErr error
	}{
		{name: "a busy volume refuses the build", left: []string{demoVolumeName}, removed: map[string]error{demoVolumeName: busy},
			wantErr: model.ErrComposeProjectLeft},
		{name: "volumes compose passed over go by name", left: []string{"demo_extra_a", "demo_extra_b"},
			removed: map[string]error{"demo_extra_a": nil, "demo_extra_b": nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoColdRecord()
			deps, _, inspector, _ := upDeps(t, []model.Savepoint{record}, demoResources())
			deps.SavepointRepo.(*mockSavepointRepo).On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
			images := &mockImageBackend{}
			images.On("RestoreWritableLayer", mock.Anything, mock.Anything).Return(demoWritableLayerRestoreResult(), nil).Once()
			runner := &mockComposeRunner{}
			remover := &mockVolumeRemover{}
			drop := runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
			read := inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{Volumes: tt.left}, nil).Once()
			calls := []*mock.Call{drop, read}
			var stillThere []string
			for _, name := range tt.left {
				calls = append(calls, remover.On("RemoveVolume", mock.Anything, name).Return(tt.removed[name]).Once())
				if tt.removed[name] != nil {
					stillThere = append(stillThere, name)
				}
			}
			calls = append(calls, inspector.On("ReadResources", mock.Anything).
				Return(model.ComposeProjectResources{Volumes: stillThere}, nil).Once())
			if tt.wantErr == nil {
				// Past the barrier the build runs; its failure keeps the test short.
				calls = append(calls, runner.On("Create", mock.Anything, isTargetProject(), demoRestoredVolumes).
					Return(errors.New("create boom")).Once())
			}
			revert := runner.On("Down", mock.Anything, isTargetProject()).Return(nil).Once()
			cleanupRead := inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{Volumes: stillThere}, nil).Once()
			calls = append(calls, revert, cleanupRead)
			for _, name := range stillThere {
				calls = append(calls, remover.On("RemoveVolume", mock.Anything, name).Return(busy).Once())
			}
			mock.InOrder(calls...)
			registry := &mockRegistry{}
			registry.On("New", mock.Anything).Return(&mockBackend{volume: record.Stats[0].Volume}, nil).Once()
			inflight := &fakeInflight{}
			deps.ImageBackend, deps.ComposeRunner, deps.VolumeRemover, deps.ProjectRemover = images, runner, remover, remover
			deps.BackendRegistry, deps.Inflight = registry, inflight
			svc := New(deps)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := svc.Up(ctx, model.UpRequest{Force: true})

			// Assert
			require.Error(t, err)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				var left *model.ComposeProjectLeftError
				require.ErrorAs(t, err, &left)
				assert.Equal(t, stillThere, left.Volumes)
				assert.ErrorIs(t, err, busy, "the daemon's refusal says what holds it")
			} else {
				assert.ErrorContains(t, err, "create boom")
			}
			_, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			assert.False(t, marked, "nothing was restored, so the marker clears")
			runner.AssertExpectations(t)
			inspector.AssertExpectations(t)
			remover.AssertExpectations(t)
		})
	}
}

// A forced up removes what is no replica before compose's down, so a finished one-off goes with its
// anonymous volumes rather than leave them unlabelled and unseen.
func TestService_Up_ForceRemovesTheOneOffsFirst(t *testing.T) {
	// Arrange
	record := demoColdRecord()
	resources := demoResources()
	resources.Containers = append(resources.Containers, "demo-db-run-1")
	deps, _, inspector, _ := upDeps(t, []model.Savepoint{record}, resources)
	deps.SavepointRepo.(*mockSavepointRepo).On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
	images := &mockImageBackend{}
	images.On("RestoreWritableLayer", mock.Anything, mock.Anything).Return(demoWritableLayerRestoreResult(), nil).Once()
	runner := &mockComposeRunner{}
	remover := &mockVolumeRemover{}
	removeOneOff := remover.On("RemoveContainer", mock.Anything, "demo-db-run-1").Return(nil).Once()
	drop := runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
	gone := inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()
	// Past the barrier the build runs; its failure keeps the test short.
	create := runner.On("Create", mock.Anything, isTargetProject(), demoRestoredVolumes).Return(errors.New("create boom")).Once()
	runner.On("Down", mock.Anything, isTargetProject()).Return(nil).Once()
	inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()
	mock.InOrder(removeOneOff, drop, gone, create)
	registry := &mockRegistry{}
	registry.On("New", mock.Anything).Return(&mockBackend{volume: record.Stats[0].Volume}, nil).Once()
	deps.ImageBackend, deps.ComposeRunner, deps.ProjectRemover, deps.BackendRegistry = images, runner, remover, registry
	svc := New(deps)

	// Act
	_, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{Force: true})

	// Assert
	require.ErrorContains(t, err, "create boom")
	runner.AssertExpectations(t)
	inspector.AssertExpectations(t)
	remover.AssertExpectations(t)
}

// An interrupt that lands while the layers load stops a forced up before the marker and the drop:
// what the daemon holds is untouched.
func TestService_Up_InterruptBeforeTheDrop(t *testing.T) {
	// Arrange
	record := demoColdRecord()
	deps, _, inspector, _ := upDeps(t, []model.Savepoint{record}, demoResources())
	deps.SavepointRepo.(*mockSavepointRepo).On("LoadSpec", mock.Anything, record.ID).Return(demoTargetSpecBytes, nil).Once()
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	images := &mockImageBackend{}
	images.On("RestoreWritableLayer", mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { cancel() }).Return(demoWritableLayerRestoreResult(), nil).Once()
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}
	registry.On("New", mock.Anything).Return(&mockBackend{volume: record.Stats[0].Volume}, nil).Once()
	deps.ImageBackend, deps.ComposeRunner, deps.BackendRegistry, deps.Inflight = images, runner, registry, inflight
	svc := New(deps)

	// Act
	_, err := svc.Up(ctx, model.UpRequest{Force: true})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	inflight.AssertExpectations(t)
	runner.AssertExpectations(t)
	inspector.AssertExpectations(t)
}

// Under an inflight marker up refuses, forced or not and whatever it names: recovering is a
// rollback's. Nothing past the gate is read.
func TestService_Up_RefusesUnderAMarker(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "unforced", true: "forced"}[force], func(t *testing.T) {
			// Arrange: the marker names the very savepoint up would build.
			record := demoColdRecord()
			deps, _, _ := coldStartDeps(t, record)
			inspector := &mockInspector{}
			deps.ComposeInspector, deps.ComposeProject = inspector, demoProject
			deps.Inflight = &fakeInflight{marker: &model.InflightMarker{
				Phase: model.InflightRecreating, Session: model.DefaultSession, ComposeProject: deps.ComposeProject,
				Target: record.ID,
			}}
			svc := New(deps)

			// Act
			_, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{Force: force})

			// Assert
			var interrupted *model.InterruptedRollbackError
			require.ErrorAs(t, err, &interrupted)
			inspector.AssertExpectations(t)
		})
	}
}

// A session with no savepoint to bring up says so by the sentinel.
func TestService_Up_NoSavepoints(t *testing.T) {
	// Arrange
	deps, _, _ := coldStartDeps(t, demoColdRecord())
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint(nil), nil).Once()
	deps.SavepointRepo, deps.ComposeInspector = repo, &mockInspector{}
	svc := New(deps)

	// Act
	_, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrNoSavepoints)
	repo.AssertExpectations(t)
}
