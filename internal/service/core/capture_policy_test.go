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
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// labelledIntrospection is demoIntrospectionWithContainers, its one service carrying labels.
func labelledIntrospection(labels map[string]string) model.IntrospectionResult {
	introspection := demoIntrospectionWithContainers()
	introspection.Project.Services[0].Labels = labels
	return introspection
}

// A service's labels choose its layer's backend and leave its upstream out.
func TestService_prepare_LabelsDecideTheImages(t *testing.T) {
	// Arrange
	introspection := labelledIntrospection(map[string]string{
		model.LabelWritableLayerBackend: "fullcopy",
		model.LabelImageCapture:         "false",
	})
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(&mockBackend{volume: demoVolume()}, nil).Once()
	svc := New(Deps{
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		BackendRegistry:  registry,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
		MinDaemonMajor:   25,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	working, err := svc.prepare(ctx, 0)

	// Assert
	require.NoError(t, err)
	require.Len(t, working.images.writableLayers, 1)
	assert.Equal(t, model.ImageBackendFullCopy, working.images.writableLayers[0].Backend)
	assert.Equal(t, demoContainerID, working.images.writableLayers[0].Container.ID)
	assert.Empty(t, working.images.upstreams)
	assert.Equal(t, []model.SkippedImage{{
		Service: demoService, Image: demoBaseImage, Reason: model.SkipReasonExcluded,
		Detail: "arcave.image.capture=false on service " + demoService,
	}}, working.images.skippedImages)
	inspector.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// An old daemon is refused even when no layer is captured.
func TestService_prepare_OldDaemonIsRefusedWithoutALayer(t *testing.T) {
	// Arrange
	introspection := labelledIntrospection(map[string]string{model.LabelWritableLayerCapture: "false"})
	introspection.Daemon.ServerVersion = "24.0.7"
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	// No expectation on the registry: the refusal comes before any backend is built.
	registry := &mockRegistry{}
	svc := New(Deps{
		ComposeInspector: inspector,
		BackendRegistry:  registry,
		Store:            newDirStore(t),
		CaptureDefaults:  demoCaptureDefaults,
		MinDaemonMajor:   25,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.prepare(ctx, 0)

	// Assert
	require.ErrorIs(t, err, model.ErrUnsupportedDaemon)
	inspector.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// A rollback's safety snapshot captures what the rollback overwrites, whatever the
// labels say now, and a label it cannot read only warns.
func TestService_newSafetySnapshotPolicy(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
	}{
		{name: "it keeps what the rollback restores into", labels: map[string]string{model.LabelCapture: "false"}},
		{name: "it follows the config past a bad label", labels: map[string]string{"arcave.captur": "false"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := labelledIntrospection(tt.labels)
			introspection.Project.Volumes[0].UsedBy = []model.ServiceReference{{Service: demoService, Target: "/data"}}
			svc := New(Deps{CaptureDefaults: demoCaptureDefaults})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			policy, err := svc.newSafetySnapshotPolicy(ctx, introspection, demoRecord())

			// Assert
			require.NoError(t, err)
			assert.True(t, policy.DecideNamedVolume("demo_db").Capture)
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

// A service whose every mount a label left out is not "without volumes", and an
// upstream another captured service names is not reported as left out.
func TestNotCovered_ReportsEachExclusionOnce(t *testing.T) {
	// Arrange
	introspection := model.IntrospectionResult{Project: model.Project{Name: "demo", Services: []model.Service{
		{Name: "app", Volumes: []model.ServiceVolume{{Type: model.VolumeTypeBind, RawType: "bind", Source: "/src", Target: "/app"}}},
	}}}
	excluded := model.VolumeDecision{CaptureDecision: model.CaptureDecision{Source: "a label"}}
	volumePolicy := model.CapturePolicy{Mounts: map[model.MountRef]model.VolumeDecision{{Service: "app", Target: "/app"}: excluded}}
	containers := []model.ContainerRef{
		{Service: "a", Replica: 1, UpstreamImageID: demoUpstreamID},
		{Service: "b", Replica: 1, UpstreamImageID: demoUpstreamID},
	}
	imagePolicy := model.CapturePolicy{Services: map[string]model.ServiceDecision{
		"a": {Image: model.CaptureDecision{Source: "a label"}},
	}}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, notCovered, err := FindBackupVolumes(introspection, volumePolicy)
	taken, skipped := imageCaptureContainers(ctx, containers, imagePolicy)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, notCovered.ServicesWithoutVolumes)
	assert.Len(t, notCovered.Mounts, 1)
	assert.Equal(t, []model.ContainerRef{containers[1]}, taken)
	assert.Empty(t, skipped)
}

// A label arcave cannot act on refuses the workflow before the spec is assembled.
func TestService_prepare_InvalidLabelStopsNothing(t *testing.T) {
	// Arrange
	introspection := labelledIntrospection(map[string]string{"arcave.captur": "false"})
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	// No expectation on the assembler: the refusal comes first.
	assembler := &mockAssembler{}
	svc := New(Deps{
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        &mockCodec{},
		BackendRegistry:  &mockRegistry{},
		Store:            newDirStore(t),
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.prepare(ctx, 0)

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidCapturePolicy)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
}

func TestNewLayerCaptures(t *testing.T) {
	// Arrange
	containers := []model.ContainerRef{
		{Service: "app", Replica: 1, Name: "demo-app-1"},
		{Service: "db", Replica: 1, Name: "demo-db-1"},
		{Service: "db", Replica: 2, Name: "demo-db-2"},
	}
	policy := model.CapturePolicy{
		WritableLayerBackend: model.ImageBackendDedupCopy,
		Services: map[string]model.ServiceDecision{
			"db": {WritableLayer: model.WritableLayerDecision{CaptureDecision: model.CaptureDecision{Source: "a label"}}},
		},
	}
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	layers, skipped := newLayerCaptures(ctx, containers, policy)

	// Assert
	assert.Equal(t, []model.WritableLayerCaptureRequest{
		{Container: containers[0], Backend: model.ImageBackendDedupCopy},
	}, layers)
	assert.Equal(t, []model.SkippedWritableLayer{
		{Service: "db", Replica: 1, Container: "demo-db-1", Reason: model.SkipReasonExcluded, Detail: "a label"},
		{Service: "db", Replica: 2, Container: "demo-db-2", Reason: model.SkipReasonExcluded, Detail: "a label"},
	}, skipped)
}

// Past a bad label the safety snapshot drops the labels, not the config.
func TestService_newSafetySnapshotPolicy_FallbackKeepsTheConfig(t *testing.T) {
	// Arrange
	defaults := demoCaptureDefaults
	defaults.SavepointCapture.WritableLayers = false
	svc := New(Deps{CaptureDefaults: defaults})
	introspection := labelledIntrospection(map[string]string{"arcave.captur": "false"})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	policy, err := svc.newSafetySnapshotPolicy(ctx, introspection, demoRecord())

	// Assert
	require.NoError(t, err)
	assert.False(t, policy.DecideService(demoService).WritableLayer.Capture)
}
