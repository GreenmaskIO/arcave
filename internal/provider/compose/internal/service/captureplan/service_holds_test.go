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

package captureplan

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// newStateReplica is replica n of service in the given state; a nil state is none reported.
func newStateReplica(service string, n int, state *container.State) composemodel.Container {
	return composemodel.Container{
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
	refOf := func(replica composemodel.Container, service string) composemodel.ContainerRef {
		return composemodel.ContainerRef{Service: service, Replica: replica.Number, ID: replica.ID, Name: replica.Name}
	}

	tests := []struct {
		name     string
		services []composemodel.Service
		policy   composemodel.CapturePolicy
		want     func(services []composemodel.Service) []composemodel.ServiceHold
	}{
		{
			name: "mixed modes, a service with no decision held by the policy's mode",
			services: []composemodel.Service{
				{Name: "db", Containers: []composemodel.Container{newStateReplica("db", 1, running)}},
				{Name: "web", Containers: []composemodel.Container{newStateReplica("web", 1, running)}},
				{Name: "worker", Containers: []composemodel.Container{newStateReplica("worker", 1, running)}},
			},
			policy: composemodel.CapturePolicy{
				Mode: composeconfig.CaptureModeLive,
				Services: map[string]composemodel.ServiceDecision{
					"db":  {Mode: composeconfig.CaptureModeStop},
					"web": {Mode: composeconfig.CaptureModePause},
				},
			},
			want: func(services []composemodel.Service) []composemodel.ServiceHold {
				return []composemodel.ServiceHold{
					{Service: "db", Mode: composeconfig.CaptureModeStop, Running: []composemodel.ContainerRef{refOf(services[0].Containers[0], "db")}},
					{Service: "web", Mode: composeconfig.CaptureModePause, Running: []composemodel.ContainerRef{refOf(services[1].Containers[0], "web")}},
					{Service: "worker", Mode: composeconfig.CaptureModeLive, Running: []composemodel.ContainerRef{refOf(services[2].Containers[0], "worker")}},
				}
			},
		},
		{
			name: "a service's hooks are carried",
			services: []composemodel.Service{
				{Name: "db", Containers: []composemodel.Container{newStateReplica("db", 1, running)}},
			},
			policy: composemodel.CapturePolicy{Services: map[string]composemodel.ServiceDecision{
				"db": {PreCapture: &composemodel.Hook{Command: []string{"pre"}}, PostCapture: &composemodel.Hook{Command: []string{"post"}}},
			}},
			want: func(services []composemodel.Service) []composemodel.ServiceHold {
				return []composemodel.ServiceHold{{
					Service:     "db",
					Mode:        composeconfig.CaptureModeStop,
					Running:     []composemodel.ContainerRef{refOf(services[0].Containers[0], "db")},
					PreCapture:  &composemodel.Hook{Command: []string{"pre"}},
					PostCapture: &composemodel.Hook{Command: []string{"post"}},
				}}
			},
		},
		{
			name: "a paused, exited, created or restarting replica is idle; no state is running",
			services: []composemodel.Service{{Name: "db", Containers: []composemodel.Container{
				newStateReplica("db", 1, paused),
				newStateReplica("db", 2, exited),
				newStateReplica("db", 3, created),
				newStateReplica("db", 4, restarting),
				newStateReplica("db", 5, nil),
			}}},
			want: func(services []composemodel.Service) []composemodel.ServiceHold {
				replicas := services[0].Containers
				return []composemodel.ServiceHold{{
					Service: "db",
					Mode:    composeconfig.CaptureModeStop,
					Running: []composemodel.ContainerRef{refOf(replicas[4], "db")},
					Idle: []composemodel.IdleReplica{
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
			introspection := composemodel.IntrospectionResult{Project: composemodel.Project{Name: composetest.DemoProject, Services: tt.services}}

			// Act
			holds := newServiceHolds(introspection, tt.policy)

			// Assert
			assert.Equal(t, tt.want(tt.services), holds)
		})
	}
}

// A capture before a restore stops every service, whatever a label or the config say.
func TestPlanner_newReplacementPolicy_StopsEveryService(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		mode   composeconfig.CaptureMode
	}{
		{name: "a pause label", labels: map[string]string{string(composemodel.CaptureLabelCaptureMode): "pause"}},
		{name: "a live label", labels: map[string]string{string(composemodel.CaptureLabelCaptureMode): "live"}},
		{name: "a pause config", mode: composeconfig.CaptureModePause},
		{name: "past a bad label, a pause config", labels: map[string]string{"arcave.captur": "false"}, mode: composeconfig.CaptureModePause},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			defaults := composetest.DemoCaptureDefaults
			defaults.Mode = tt.mode
			svc := New(Deps{CaptureDefaults: defaults})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			policy, _ := svc.newReplacementPolicy(ctx, composetest.LabelledIntrospection(tt.labels), composetest.DemoRecord())

			// Assert
			assert.Equal(t, composeconfig.CaptureModeStop, policy.Mode)
			assert.Equal(t, composeconfig.CaptureModeStop, policy.DecideService(composetest.DemoService).Mode)
			assert.Equal(t, composeconfig.CaptureModeStop, policy.DecideService("unlabelled").Mode)
		})
	}
}

// A capture before a restore runs no hook, whatever the labels say.
func TestPlanner_newReplacementPolicy_RunsNoHook(t *testing.T) {
	// Arrange
	defaults := composetest.DemoCaptureDefaults
	defaults.HookTimeout = time.Minute
	defaults.HookOnError = composeconfig.HookOnErrorFail
	svc := New(Deps{CaptureDefaults: defaults})
	ctx := zerolog.Nop().WithContext(context.Background())
	labels := map[string]string{
		string(composemodel.CaptureLabelHookPreCapture):  "pre",
		string(composemodel.CaptureLabelHookPostCapture): "post",
	}

	// Act
	policy, labelsIgnored := svc.newReplacementPolicy(ctx, composetest.LabelledIntrospection(labels), composetest.DemoRecord())

	// Assert
	assert.NoError(t, labelsIgnored)
	assert.Nil(t, policy.DecideService(composetest.DemoService).PreCapture)
	assert.Nil(t, policy.DecideService(composetest.DemoService).PostCapture)
}

// A savepoint's plan holds each service by its label, or by the config's mode.
func TestPlanner_Plan_HoldsEachServiceByItsMode(t *testing.T) {
	tests := []struct {
		name       string
		labels     map[string]string
		configMode composeconfig.CaptureMode
		wantMode   composeconfig.CaptureMode
	}{
		{name: "the label", labels: map[string]string{string(composemodel.CaptureLabelCaptureMode): "pause"}, wantMode: composeconfig.CaptureModePause},
		{name: "the config", configMode: composeconfig.CaptureModeLive, wantMode: composeconfig.CaptureModeLive},
		{name: "neither: stop", wantMode: composeconfig.CaptureModeStop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := composetest.LabelledIntrospection(tt.labels)
			store := composetest.NewDirStore(t)
			registry := &composetest.MockRegistry{}
			registry.On("New", composetest.MatchSpec(store, "volume-named-demo_db")).Return(composetest.NewMockBackend(composetest.DemoVolume()), nil).Once()
			defaults := composetest.DemoCaptureDefaults
			defaults.Mode = tt.configMode
			svc := New(Deps{
				ProjectAssembler: composetest.AssemblerFor(introspection),
				SpecCodec:        composetest.DemoCodec(),
				BackendRegistry:  registry,
				Store:            store,
				CaptureDefaults:  defaults,
			})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			plan, err := svc.Plan(ctx, introspection, nil)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []composemodel.ServiceHold{{
				Service: composetest.DemoService,
				Mode:    tt.wantMode,
				Running: []composemodel.ContainerRef{{Service: composetest.DemoService, Replica: 1, ID: composetest.DemoContainerID, Name: composetest.DemoContainerName(1)}},
			}}, plan.Services)
			registry.AssertExpectations(t)
		})
	}
}
