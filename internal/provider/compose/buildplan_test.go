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
	"fmt"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
)

// A service's labels choose its layer's backend and leave its upstream out.
func TestService_BuildPlan_LabelsDecideTheImages(t *testing.T) {
	// Arrange
	introspection := labelledIntrospection(map[string]string{
		string(model.CaptureLabelWritableLayerBackend): "fullcopy",
		string(model.CaptureLabelImageCapture):         "false",
	})
	store := composetest.NewDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(demoVolume()), nil).Once()
	svc := New(Deps{
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		BackendRegistry:  registry,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	plan, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{})

	// Assert
	require.NoError(t, err)
	require.Len(t, plan.WritableLayers, 1)
	assert.Equal(t, model.ImageBackendFullCopy, plan.WritableLayers[0].Backend)
	assert.Equal(t, demoContainerID, plan.WritableLayers[0].Container.ID)
	assert.Empty(t, plan.Images)
	assert.Equal(t, []model.SkippedImage{{
		Service: demoService, Image: demoBaseImage, Reason: model.SkipReasonExcluded,
		Detail: "arcave.image.capture=false on service " + demoService,
	}}, plan.NotCovered.Images)
	assert.NoError(t, plan.LabelsIgnored)
	registry.AssertExpectations(t)
}

// A capture before a restore holds what the restore overwrites, whatever the labels say now;
// a label it cannot read is returned as ignored, never refused.
func TestService_newReplacementPolicy(t *testing.T) {
	tests := []struct {
		name        string
		labels      map[string]string
		wantIgnored bool
	}{
		{name: "it keeps what the restore writes into", labels: map[string]string{string(model.CaptureLabelCapture): "false"}},
		{
			name:        "it follows the config past a bad label",
			labels:      map[string]string{"arcave.captur": "false"},
			wantIgnored: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := labelledIntrospection(tt.labels)
			introspection.Project.Volumes[0].UsedBy = []model.ServiceReference{{Service: demoService, Target: "/data"}}
			svc := New(Deps{CaptureDefaults: demoCaptureDefaults})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			policy, labelsIgnored, err := svc.newReplacementPolicy(ctx, introspection, demoRecord())

			// Assert
			require.NoError(t, err)
			assert.True(t, policy.DecideNamedVolume("demo_db").Capture)
			if tt.wantIgnored {
				assert.ErrorIs(t, labelsIgnored, model.ErrInvalidCapturePolicy)
			} else {
				assert.NoError(t, labelsIgnored)
			}
		})
	}
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

// A label arcave cannot act on refuses the plan, as a capture policy error, before the spec is
// assembled.
func TestService_BuildPlan_InvalidLabelBuildsNothing(t *testing.T) {
	// Arrange
	introspection := labelledIntrospection(map[string]string{"arcave.captur": "false"})
	// No expectation on the assembler: the refusal comes first.
	assembler := &mockAssembler{}
	svc := New(Deps{
		ProjectAssembler: assembler,
		SpecCodec:        &mockCodec{},
		BackendRegistry:  &mockRegistry{},
		Store:            composetest.NewDirStore(t),
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidCapturePolicy)
	var policyErr *provider.CapturePolicyError
	assert.ErrorAs(t, err, &policyErr)
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

// Past a bad label a capture before a restore drops the labels, not the config.
func TestService_newReplacementPolicy_FallbackKeepsTheConfig(t *testing.T) {
	// Arrange
	defaults := demoCaptureDefaults
	defaults.SavepointCapture.WritableLayers = false
	svc := New(Deps{CaptureDefaults: defaults})
	introspection := labelledIntrospection(map[string]string{"arcave.captur": "false"})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	policy, _, err := svc.newReplacementPolicy(ctx, introspection, demoRecord())

	// Assert
	require.NoError(t, err)
	assert.False(t, policy.DecideService(demoService).WritableLayer.Capture)
}

// A plan that fails past a bad label still says the labels were ignored: no plan is left to
// carry that back to the caller.
func TestService_BuildPlan_FailedPlanWarnsOfIgnoredLabels(t *testing.T) {
	// Arrange
	record := demoRecord()
	introspection := labelledIntrospection(map[string]string{"arcave.captur": "false"})
	repo := &composetest.MockSavepointRepo{}
	repo.On("LoadSavepoints", mock.Anything, []model.SavepointID{record.ID}).Return([]model.Savepoint{record}, nil).Once()
	assembler := &mockAssembler{}
	assembler.On("Assemble", mock.Anything, introspection).Return((*types.Project)(nil), errors.New("boom")).Once()
	svc := New(Deps{
		SavepointRepo:    repo,
		ProjectAssembler: assembler,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &composetest.SyncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	plan, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{Restoring: record.ID})

	// Assert
	require.EqualError(t, err, "assembling the compose spec: boom")
	assert.Zero(t, plan)
	assert.Contains(t, logs.String(), "the capture ignored the labels and followed the config")
	assert.Contains(t, logs.String(), model.ErrInvalidCapturePolicy.Error())
	repo.AssertExpectations(t)
	assembler.AssertExpectations(t)
}

// The live compose project is planned as its artifact parses back, not as the assembler built
// it: a rollback acts on what the same loader reads.
func TestService_BuildPlan(t *testing.T) {
	// Arrange
	introspection := demoIntrospection()
	assembler := &mockAssembler{}
	codec := &mockCodec{}
	mock.InOrder(
		assembler.On("Assemble", mock.Anything, introspection).Return(demoAssembled, nil).Once(),
		codec.On("Marshal", demoAssembled).Return(demoSpecBytes, nil).Once(),
		codec.On("Parse", mock.Anything, demoSpecBytes).Return(demoSpec, nil).Once(),
	)
	store := composetest.NewDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(demoVolume()), nil).Once()
	svc := New(Deps{
		ProjectAssembler: assembler,
		SpecCodec:        codec,
		BackendRegistry:  registry,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	plan, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{})

	// Assert
	require.NoError(t, err)
	assert.Same(t, demoSpec, plan.Project)
	assert.Equal(t, demoSpecBytes, plan.Spec)
	assembler.AssertExpectations(t)
	codec.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// A plan before a restore reads the restoring record by id and keeps what it overwrites; a record
// missing or unreadable refuses the plan before anything is assembled, and is no capture policy error:
// the policy was never resolved.
func TestService_BuildPlan_Restoring(t *testing.T) {
	record := demoRecord()
	errRead := errors.New("store unreachable")
	tests := []struct {
		name    string
		found   []model.Savepoint
		readErr error
		wantErr error
	}{
		{name: "the record is read and kept", found: []model.Savepoint{record}},
		{name: "a record not found", wantErr: errRecordNotFound},
		{name: "a record that cannot be read", readErr: errRead, wantErr: errRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := labelledIntrospection(map[string]string{string(model.CaptureLabelCapture): "false"})
			introspection.Project.Volumes[0].UsedBy = []model.ServiceReference{{Service: demoService, Target: demoTarget}}
			repo := &composetest.MockSavepointRepo{}
			repo.On("LoadSavepoints", mock.Anything, []model.SavepointID{record.ID}).Return(tt.found, tt.readErr).Once()
			store := composetest.NewDirStore(t)
			registry := &mockRegistry{}
			// A refusal assembles nothing: the assembler then carries no expectation.
			assembler := &mockAssembler{}
			if tt.wantErr == nil {
				assembler.On("Assemble", mock.Anything, introspection).Return(demoAssembled, nil).Once()
				registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(demoVolume()), nil).Once()
			}
			svc := New(Deps{
				SavepointRepo:    repo,
				ProjectAssembler: assembler,
				SpecCodec:        demoCodec(),
				BackendRegistry:  registry,
				Store:            store,
				CaptureDefaults:  demoCaptureDefaults,
			})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			plan, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{Restoring: record.ID})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				var policyErr *provider.CapturePolicyError
				assert.False(t, errors.As(err, &policyErr))
				assert.Zero(t, plan)
			} else {
				require.NoError(t, err)
				require.Len(t, plan.Volumes, 1)
				assert.Equal(t, "volume-named-demo_db", plan.Volumes[0].Volume().ObjectID)
			}
			repo.AssertExpectations(t)
			assembler.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}

func TestNewReplicaGaps(t *testing.T) {
	tests := []struct {
		name    string
		numbers []int
		want    string
		wantGap []model.ReplicaGap
	}{
		{name: "no gap", numbers: []int{2, 1, 3}},
		{name: "a gap in the middle", numbers: []int{3, 1},
			want:    `"service":"db","replicas":[1,3],"missing":[2],"message":"some replicas of this service are gone`,
			wantGap: []model.ReplicaGap{{Service: demoService, Replicas: []int{1, 3}, Missing: []int{2}}}},
		{name: "the first replica gone", numbers: []int{2, 3},
			want:    `"service":"db","replicas":[2,3],"missing":[1],"message":"some replicas of this service are gone`,
			wantGap: []model.ReplicaGap{{Service: demoService, Replicas: []int{2, 3}, Missing: []int{1}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoIntrospection()
			svc := model.Service{Name: demoService}
			for _, n := range tt.numbers {
				svc.Containers = append(svc.Containers, model.Container{Number: n})
			}
			introspection.Project.Services = []model.Service{svc}
			logs := &composetest.SyncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			gaps := newReplicaGaps(ctx, introspection)

			// Assert
			assert.Equal(t, tt.wantGap, gaps)
			if tt.want == "" {
				assert.Empty(t, logs.String())
				return
			}
			assert.Contains(t, logs.String(), tt.want)
		})
	}
}

// One named volume and one bind mount are units; the tmpfs mount is a skip and web, mounting
// nothing, is not covered. Every backend is rooted at the one store the DI root left.
func TestService_BuildPlan_CoversEveryUnit(t *testing.T) {
	// Arrange
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{
			{
				Name: "app",
				Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg"},
					{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
				},
			},
			{
				Name: "db",
				Volumes: []model.ServiceVolume{{
					Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata",
					Target: "/var/lib/postgresql/data", VolumeName: "demo_pgdata",
				}},
			},
			{Name: "web"},
		},
		Volumes: []model.NamedVolume{{
			Key: "pgdata", Name: "demo_pgdata",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		}},
	}}
	store := composetest.NewDirStore(t)
	pgBackend := composetest.NewMockBackend(model.Volume{ObjectID: "volume-named-demo_pgdata"})
	cfgBackend := composetest.NewMockBackend(model.Volume{ObjectID: "volume-service-app-etc-cfg"})
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_pgdata")).Return(pgBackend, nil).Once()
	registry.On("New", matchSpec(store, "volume-service-app-etc-cfg")).Return(cfgBackend, nil).Once()
	svc := New(Deps{
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		BackendRegistry:  registry,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &composetest.SyncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	plan, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{})

	// Assert
	require.NoError(t, err)
	assert.ElementsMatch(t, []model.VolumeSnapshotBackend{pgBackend, cfgBackend}, plan.Volumes)
	assert.Equal(t, model.NotCovered{
		Mounts: []model.SkippedVolume{{Service: "app", Target: "/scratch", RawType: "tmpfs",
			Reason: model.SkipReasonTmpfs, Detail: "non-persistent (tmpfs lives in RAM)"}},
		ServicesWithoutVolumes: []string{"web"},
	}, plan.NotCovered)
	assert.Contains(t, logs.String(), "volume will not be backed up")
	assert.Contains(t, logs.String(), "/scratch")
	assert.Contains(t, logs.String(), "tmpfs")
	registry.AssertExpectations(t)
}

// Every replica's anonymous volume is a unit of its own, captured by its own backend.
func TestService_BuildPlan_AnonymousVolumePerReplica(t *testing.T) {
	// Arrange
	anon := func(replica int) model.ServiceVolume {
		return model.ServiceVolume{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/cache", Replica: replica}
	}
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{{
			Name:    "worker",
			Volumes: []model.ServiceVolume{anon(1), anon(2), anon(3)},
		}},
	}}
	store := composetest.NewDirStore(t)
	registry := &mockRegistry{}
	var backends []model.VolumeSnapshotBackend
	for replica := 1; replica <= 3; replica++ {
		objectID := fmt.Sprintf("volume-anon-worker-%d-cache", replica)
		backend := composetest.NewMockBackend(model.Volume{ObjectID: objectID})
		registry.On("New", mock.MatchedBy(func(spec model.BackendSpec) bool {
			return spec.Store == store && spec.Volume.ObjectID == objectID &&
				spec.Volume.Anon != nil && spec.Volume.Anon.Replica == replica
		})).Return(backend, nil).Once()
		backends = append(backends, backend)
	}
	svc := New(Deps{
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		BackendRegistry:  registry,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	plan, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{})

	// Assert
	require.NoError(t, err)
	assert.ElementsMatch(t, backends, plan.Volumes)
	registry.AssertExpectations(t)
}

// The backend a new savepoint captures with is the configured one, not a constant: it is stamped
// on every volume the plan resolves.
func TestService_BuildPlan_StampsTheConfiguredBackend(t *testing.T) {
	// Arrange
	backend := composetest.NewMockBackend(model.Volume{ObjectID: "volume-named-demo_db", Backend: model.SnapshotBackendCDC})
	registry := &mockRegistry{}
	registry.On("New", mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Volume.Backend == model.SnapshotBackendCDC
	})).Return(backend, nil).Once()
	defaults := demoCaptureDefaults
	defaults.VolumeBackend = model.SnapshotBackendCDC
	svc := New(Deps{
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		BackendRegistry:  registry,
		Store:            composetest.NewDirStore(t),
		CaptureDefaults:  defaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	plan, err := svc.buildPlan(ctx, demoIntrospection(), provider.CaptureOptions{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.VolumeSnapshotBackend{backend}, plan.Volumes)
	registry.AssertExpectations(t)
}

// A named volume no service mounts is covered like any other: a backend addresses a volume by
// name, so having no container to read it through is no reason to leave it out.
func TestService_BuildPlan_CoversAnUnmountedVolume(t *testing.T) {
	// Arrange
	introspection := model.IntrospectionResult{Project: model.Project{
		Name:     "demo",
		Services: demoIntrospection().Project.Services,
		Volumes: []model.NamedVolume{
			{Key: "db", Name: "demo_db", UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}},
			// Mounted by no service — docker created it, so it can hold data.
			{Key: "orphan", Name: "demo_orphan"},
		},
	}}
	store := composetest.NewDirStore(t)
	registry := &mockRegistry{}
	backends := make([]model.VolumeSnapshotBackend, 0, 2)
	for _, objectID := range []string{"volume-named-demo_db", "volume-named-demo_orphan"} {
		backend := composetest.NewMockBackend(model.Volume{ObjectID: objectID})
		registry.On("New", matchSpec(store, objectID)).Return(backend, nil).Once()
		backends = append(backends, backend)
	}
	svc := New(Deps{
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		BackendRegistry:  registry,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &composetest.SyncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	plan, err := svc.buildPlan(ctx, introspection, provider.CaptureOptions{})

	// Assert
	require.NoError(t, err)
	assert.ElementsMatch(t, backends, plan.Volumes)
	assert.Nil(t, plan.NotCovered.Mounts)
	assert.NotContains(t, logs.String(), "volume will not be backed up",
		"nothing is left out for being unreachable any more")
	registry.AssertExpectations(t)
}

// A plan that cannot be built is refused before anything is touched: an assembler that fails,
// a backend without a factory, a capture of nothing, a compose project taken down.
func TestService_BuildPlan_Refusals(t *testing.T) {
	errAssemble := errors.New("compose files are gone")
	errNoFactory := errors.New("no factory")
	nothing := model.IntrospectionResult{Project: model.Project{
		Name:     demoProject,
		Services: []model.Service{{Name: demoService}},
	}}
	tests := []struct {
		name          string
		introspection model.IntrospectionResult
		arrange       func(assembler *mockAssembler, registry *mockRegistry, store storages.Storager)
		wantErr       error
		wantText      string
	}{
		{
			name:          "an assembler failure",
			introspection: demoIntrospection(),
			arrange: func(assembler *mockAssembler, _ *mockRegistry, _ storages.Storager) {
				assembler.On("Assemble", mock.Anything, demoIntrospection()).Return(nil, errAssemble).Once()
			},
			wantErr: errAssemble,
		},
		{
			name:          "a backend without a factory",
			introspection: demoIntrospection(),
			arrange: func(assembler *mockAssembler, registry *mockRegistry, store storages.Storager) {
				assembler.On("Assemble", mock.Anything, demoIntrospection()).Return(demoAssembled, nil).Once()
				registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(nil, errNoFactory).Once()
			},
			wantErr: errNoFactory,
		},
		{
			name:          "a capture of nothing",
			introspection: nothing,
			arrange: func(assembler *mockAssembler, _ *mockRegistry, _ storages.Storager) {
				assembler.On("Assemble", mock.Anything, nothing).Return(demoAssembled, nil).Once()
			},
			wantErr: errNoVolumesToBackUp,
		},
		{
			name: "a compose project taken down",
			introspection: model.IntrospectionResult{Project: model.Project{
				Name:    demoProject,
				Volumes: demoIntrospection().Project.Volumes,
			}},
			arrange:  func(*mockAssembler, *mockRegistry, storages.Storager) {},
			wantErr:  model.ErrComposeProjectAbsent,
			wantText: `compose project "demo"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := composetest.NewDirStore(t)
			assembler := &mockAssembler{}
			registry := &mockRegistry{}
			tt.arrange(assembler, registry, store)
			svc := New(Deps{
				ProjectAssembler: assembler,
				SpecCodec:        demoCodec(),
				BackendRegistry:  registry,
				Store:            store,
				CaptureDefaults:  demoCaptureDefaults,
			})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			plan, err := svc.buildPlan(ctx, tt.introspection, provider.CaptureOptions{})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), tt.wantText)
			assert.Zero(t, plan)
			assembler.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}

// A plan takes the live compose project's upstream and its writable layer, whether for a savepoint
// or before a restore.
func TestService_BuildPlan_PlansTheImagesAndWritableLayers(t *testing.T) {
	record := demoRecord()
	tests := []struct {
		name string
		opts provider.CaptureOptions
	}{
		{name: "a savepoint"},
		{name: "before a restore", opts: provider.CaptureOptions{Restoring: record.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoIntrospectionWithContainers()
			repo := &composetest.MockSavepointRepo{}
			if tt.opts.Restoring != "" {
				repo.On("LoadSavepoints", mock.Anything, []model.SavepointID{record.ID}).
					Return([]model.Savepoint{record}, nil).Once()
			}
			store := composetest.NewDirStore(t)
			registry := &mockRegistry{}
			registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(demoVolume()), nil).Once()
			svc := New(Deps{
				SavepointRepo:    repo,
				ProjectAssembler: assemblerFor(introspection),
				SpecCodec:        demoCodec(),
				BackendRegistry:  registry,
				Store:            store,
				CaptureDefaults:  demoCaptureDefaults,
			})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			plan, err := svc.buildPlan(ctx, introspection, tt.opts)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []model.ImageCaptureRequest{demoImageCaptureRequest()}, plan.Images)
			assert.Equal(t, []model.WritableLayerCaptureRequest{{
				Container: demoContainerRef(),
				Backend:   model.ImageBackendDedupCopy,
			}}, plan.WritableLayers)
			assert.Equal(t, []model.ContainerRef{demoContainerRef()}, plan.Containers)
			repo.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}

// The stamps go onto the services they name, beside the user's labels, and nowhere else.
func TestStampServices(t *testing.T) {
	// Arrange
	project := &types.Project{Services: types.Services{"db": {Name: "db", Labels: types.Labels{"tier": "data"}}}}

	// Act
	stampServices(project, map[string]map[string]string{
		"db":   {string(model.SystemLabelWritableLayerStamp): "true; origin=config"},
		"gone": {string(model.SystemLabelWritableLayerStamp): "false; origin=config"},
	})

	// Assert
	assert.Equal(t, types.Labels{"tier": "data", string(model.SystemLabelWritableLayerStamp): "true; origin=config"}, project.Services["db"].Labels)
	assert.NotContains(t, project.Services, "gone")
}
