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
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/capture"
)

// newStateReplica is replica n of service in the given state; a nil state is none reported.
func newStateReplica(service string, n int, state *container.State) model.Container {
	return model.Container{
		ID:      fmt.Sprintf("%s-id-%d", service, n),
		Name:    fmt.Sprintf("demo-%s-%d", service, n),
		Number:  n,
		Inspect: container.InspectResponse{State: state},
	}
}

// Each service is held by its decided mode, its replicas sorted by what the daemon reported.
func TestNewServiceHolds(t *testing.T) {
	var (
		running    = &container.State{Status: container.StateRunning, Running: true}
		paused     = &container.State{Status: container.StatePaused, Running: true, Paused: true}
		exited     = &container.State{Status: container.StateExited}
		created    = &container.State{Status: container.StateCreated}
		restarting = &container.State{Status: container.StateRestarting, Running: true, Restarting: true}
	)
	refOf := func(replica model.Container, service string) model.ContainerRef {
		return model.ContainerRef{Service: service, Replica: replica.Number, ID: replica.ID, Name: replica.Name}
	}

	tests := []struct {
		name     string
		services []model.Service
		policy   model.CapturePolicy
		want     func(services []model.Service) []capture.ServiceHold
	}{
		{
			name: "mixed modes, a service with no decision held by the policy's mode",
			services: []model.Service{
				{Name: "db", Containers: []model.Container{newStateReplica("db", 1, running)}},
				{Name: "web", Containers: []model.Container{newStateReplica("web", 1, running)}},
				{Name: "worker", Containers: []model.Container{newStateReplica("worker", 1, running)}},
			},
			policy: model.CapturePolicy{
				Mode: model.CaptureModeLive,
				Services: map[string]model.ServiceDecision{
					"db":  {Mode: model.CaptureModeStop},
					"web": {Mode: model.CaptureModePause},
				},
			},
			want: func(services []model.Service) []capture.ServiceHold {
				return []capture.ServiceHold{
					{Service: "db", Mode: model.CaptureModeStop, Running: []model.ContainerRef{refOf(services[0].Containers[0], "db")}},
					{Service: "web", Mode: model.CaptureModePause, Running: []model.ContainerRef{refOf(services[1].Containers[0], "web")}},
					{Service: "worker", Mode: model.CaptureModeLive, Running: []model.ContainerRef{refOf(services[2].Containers[0], "worker")}},
				}
			},
		},
		{
			name: "a paused, exited, created or restarting replica is idle; no state is running",
			services: []model.Service{{Name: "db", Containers: []model.Container{
				newStateReplica("db", 1, paused),
				newStateReplica("db", 2, exited),
				newStateReplica("db", 3, created),
				newStateReplica("db", 4, restarting),
				newStateReplica("db", 5, nil),
			}}},
			want: func(services []model.Service) []capture.ServiceHold {
				replicas := services[0].Containers
				return []capture.ServiceHold{{
					Service: "db",
					Mode:    model.CaptureModeStop,
					Running: []model.ContainerRef{refOf(replicas[4], "db")},
					Idle: []capture.IdleReplica{
						{Ref: refOf(replicas[0], "db"), State: "paused"},
						{Ref: refOf(replicas[1], "db"), State: "exited"},
						{Ref: refOf(replicas[2], "db"), State: "created"},
						{Ref: refOf(replicas[3], "db"), State: "restarting"},
					},
				}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := model.IntrospectionResult{Project: model.Project{Name: demoProject, Services: tt.services}}

			// Act
			holds := newServiceHolds(introspection, tt.policy)

			// Assert
			assert.Equal(t, tt.want(tt.services), holds)
		})
	}
}

// A rollback's safety snapshot stops every service, whatever a label or the config say.
func TestService_newSafetySnapshotPolicy_StopsEveryService(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		mode   model.CaptureMode
	}{
		{name: "a pause label", labels: map[string]string{string(model.CaptureLabelCaptureMode): "pause"}},
		{name: "a live label", labels: map[string]string{string(model.CaptureLabelCaptureMode): "live"}},
		{name: "a pause config", mode: model.CaptureModePause},
		{name: "past a bad label, a pause config", labels: map[string]string{"arcave.captur": "false"}, mode: model.CaptureModePause},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			defaults := demoCaptureDefaults
			defaults.Mode = tt.mode
			svc := New(Deps{SessionLocker: fakeSessionLocker{}, CaptureDefaults: defaults})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			policy, err := svc.newSafetySnapshotPolicy(ctx, labelledIntrospection(tt.labels), demoRecord())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, model.CaptureModeStop, policy.Mode)
			assert.Equal(t, model.CaptureModeStop, policy.DecideService(demoService).Mode)
			assert.Equal(t, model.CaptureModeStop, policy.DecideService("unlabelled").Mode)
		})
	}
}

// A savepoint's plan holds each service by its label, or by the config's mode.
func TestService_prepare_HoldsEachServiceByItsMode(t *testing.T) {
	tests := []struct {
		name       string
		labels     map[string]string
		configMode model.CaptureMode
		wantMode   model.CaptureMode
	}{
		{name: "the label", labels: map[string]string{string(model.CaptureLabelCaptureMode): "pause"}, wantMode: model.CaptureModePause},
		{name: "the config", configMode: model.CaptureModeLive, wantMode: model.CaptureModeLive},
		{name: "neither: stop", wantMode: model.CaptureModeStop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := labelledIntrospection(tt.labels)
			inspector := &mockInspector{}
			inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
			store := newDirStore(t)
			registry := &mockRegistry{}
			registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(&mockBackend{volume: demoVolume()}, nil).Once()
			defaults := demoCaptureDefaults
			defaults.Mode = tt.configMode
			svc := New(Deps{
				ArcaveProject:    demoArcaveProject,
				SessionLocker:    fakeSessionLocker{},
				ComposeInspector: inspector,
				ProjectAssembler: assemblerFor(introspection),
				SpecCodec:        demoCodec(),
				BackendRegistry:  registry,
				Store:            store,
				CaptureDefaults:  defaults,
			})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			working, err := svc.prepare(ctx, 0)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []capture.ServiceHold{{
				Service: demoService,
				Mode:    tt.wantMode,
				Running: []model.ContainerRef{{Service: demoService, Replica: 1, ID: demoContainerID, Name: demoContainerName(1)}},
			}}, working.plan.Services)
			inspector.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}
