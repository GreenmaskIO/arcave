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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/hold"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestCapturePlan_Hold(t *testing.T) {
	tests := []struct {
		name       string
		stopErr    error
		wantKilled []string
		wantErr    string
	}{
		{
			name:       "stops the project",
			wantKilled: []string{"c1"},
		},
		{
			name:    "a failed stop",
			stopErr: errors.New("boom"),
			wantErr: "stopping services: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &composetest.MockComposeRunner{}
			runner.On("Stop", mock.Anything, composetest.DemoSpec).Return([]string{"c1"}, tt.stopErr).Once()
			svc := newComposeWorkload(Deps{ComposeRunner: runner})
			recorder := &composetest.StepRecorder{}

			// Act
			holding, err := svc.newCapturePlan(composemodel.Plan{Project: composetest.DemoSpec, Services: stopServices}, composemodel.IntrospectionResult{}).Hold(composetest.NewProgressContext(recorder))

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.IsType(t, heldWorkload{}, holding)
				assert.Equal(t, hold.Outcome{Killed: tt.wantKilled}, holding.(heldWorkload).outcome)
			} else {
				assert.EqualError(t, err, tt.wantErr)
				assert.Nil(t, holding)
			}
			assert.Equal(t, []progress.Kind{"test", kindSavepointHold, "compose.stop"}, recorder.Kinds)
			runner.AssertExpectations(t)
		})
	}
}

// A pre-capture hook's failure let through rides on the held workload, beside the killed containers.
func TestCapturePlan_Hold_CarriesFailedHooks(t *testing.T) {
	// Arrange
	runner := &composetest.MockComposeRunner{}
	control := &composetest.MockContainerControl{}
	pre := control.On("ExecInContainer", mock.Anything, composetest.DemoContainerID, composemodel.ExecSpec{Cmd: []string{"pre"}}).
		Return(composemodel.ExecResult{ExitCode: 3}, nil).Once()
	stop := runner.On("Stop", mock.Anything, composetest.DemoSpec).Return([]string{"c1"}, nil).Once()
	mock.InOrder(pre, stop)
	svc := newComposeWorkload(Deps{ComposeRunner: runner, ContainerControl: control})
	plan := composemodel.Plan{Project: composetest.DemoSpec, Services: []composemodel.ServiceHold{{
		Service: composetest.DemoService,
		Mode:    model.CaptureModeStop,
		Running: []composemodel.ContainerRef{composetest.DemoContainerRef()},
		PreCapture: &composemodel.Hook{
			Command: []string{"pre"},
			Timeout: time.Minute,
			OnError: model.HookOnErrorContinue,
		},
	}}}

	// Act
	holding, err := svc.newCapturePlan(plan, composemodel.IntrospectionResult{}).Hold(composetest.NewTestContext())

	// Assert
	require.NoError(t, err)
	require.IsType(t, heldWorkload{}, holding)
	outcome := holding.(heldWorkload).outcome
	assert.Equal(t, []string{"c1"}, outcome.Killed)
	require.Len(t, outcome.FailedHooks, 1)
	assert.Equal(t, "exit 3", outcome.FailedHooks[0].Error)
	runner.AssertExpectations(t)
	control.AssertExpectations(t)
}

// Release starts the whole project under a savepoint.release step.
func TestCapturePlan_Release(t *testing.T) {
	tests := []struct {
		name     string
		startErr error
		wantErr  string
	}{
		{
			name: "starts the project",
		},
		{
			name:     "a failed start",
			startErr: errors.New("boom"),
			wantErr:  "starting services: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &composetest.MockComposeRunner{}
			runner.On("Start", mock.Anything, composetest.DemoSpec, time.Minute).Return(tt.startErr).Once()
			svc := newComposeWorkload(Deps{ComposeRunner: runner})
			recorder := &composetest.StepRecorder{}
			ctx := composetest.NewProgressContext(recorder)

			// Act
			err := svc.newCapturePlan(composemodel.Plan{Project: composetest.DemoSpec, Services: stopServices}, composemodel.IntrospectionResult{}).Release(ctx, time.Minute)

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, []progress.Kind{"test", kindSavepointRelease, "compose.start"}, recorder.Kinds)
			runner.AssertExpectations(t)
		})
	}
}

// A plan of every mode holds stop, then pause, and brings back live, pause, then stop — the
// resumed replicas' probes waited on before the start — whether the hold or the capture failed.
func TestCapturePlan_HoldCaptureRelease(t *testing.T) {
	errBoom := errors.New("boom")
	services := []composemodel.ServiceHold{
		{Service: "db", Mode: model.CaptureModeStop, Running: []composemodel.ContainerRef{dbReplica1}},
		{Service: "web", Mode: model.CaptureModePause, Running: []composemodel.ContainerRef{webReplica1},
			Idle: []composemodel.IdleReplica{{Ref: webReplica2, State: "paused"}}},
		{Service: "worker", Mode: model.CaptureModeLive, Idle: []composemodel.IdleReplica{{Ref: workerReplica1, State: "paused"}}},
	}

	tests := []struct {
		name        string
		pauseErr    error
		specErr     error
		wantHold    string
		wantCapture string
	}{
		{
			name:     "the pause fails",
			pauseErr: errBoom,
			wantHold: "pausing containers: boom",
		},
		{
			name:        "the capture fails",
			specErr:     errBoom,
			wantCapture: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &composetest.MockComposeRunner{}
			control := &composetest.MockContainerControl{}
			repo := &composetest.MockSavepointRepo{}
			calls := []*mock.Call{
				runner.On("StopServices", mock.Anything, composetest.DemoSpec, []string{"db"}).Return(nil, nil).Once(),
				control.On("PauseContainers", mock.Anything, []string{"web1"}).Return(tt.pauseErr).Once(),
			}
			if tt.pauseErr == nil {
				calls = append(calls, repo.On("SaveSpec", mock.Anything, mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil }), composetest.DemoSpecBytes).
					Return(tt.specErr).Once())
			}
			calls = append(calls,
				control.On("UnpauseContainers", mock.Anything, []string{"worker1"}).Return(nil).Once(),
				control.On("UnpauseContainers", mock.Anything, []string{"web1", "web2"}).Return(nil).Once(),
				control.On("AwaitHealthProbes", mock.Anything, []string{"web1", "web2", "worker1"}).Return(nil).Once(),
				runner.On("Start", mock.Anything, composetest.DemoSpec, time.Minute).Return(nil).Once(),
			)
			mock.InOrder(calls...)
			svc := newComposeWorkload(Deps{
				SavepointRepo:    repo,
				ComposeRunner:    runner,
				ContainerControl: control,
				Store:            composetest.NewDirStore(t),
			})
			plan := svc.newCapturePlan(composemodel.Plan{
				Project:  composetest.DemoSpec,
				Spec:     composetest.DemoSpecBytes,
				Services: services,
			}, composemodel.IntrospectionResult{})
			ctx := composetest.NewTestContext()

			// Act
			holding, holdErr := plan.Hold(ctx)
			var captureErr error
			if holdErr == nil {
				_, captureErr = holding.Capture(ctx, provider.CaptureRequest{ID: model.NewSavepointID()})
			}
			releaseErr := plan.Release(ctx, time.Minute)

			// Assert
			if tt.wantHold == "" {
				assert.NoError(t, holdErr)
			} else {
				assert.EqualError(t, holdErr, tt.wantHold)
			}
			if tt.wantCapture != "" {
				assert.ErrorContains(t, captureErr, tt.wantCapture)
			}
			assert.NoError(t, releaseErr)
			runner.AssertExpectations(t)
			control.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

// Replicas of the demo services.
var (
	dbReplica1     = composemodel.ContainerRef{Service: "db", Replica: 1, ID: "db1", Name: "demo-db-1"}
	webReplica1    = composemodel.ContainerRef{Service: "web", Replica: 1, ID: "web1", Name: "demo-web-1"}
	webReplica2    = composemodel.ContainerRef{Service: "web", Replica: 2, ID: "web2", Name: "demo-web-2"}
	workerReplica1 = composemodel.ContainerRef{Service: "worker", Replica: 1, ID: "worker1", Name: "demo-worker-1"}
)

// stopServices is a plan's services, every one held by stop and running.
var stopServices = []composemodel.ServiceHold{{
	Service: "db",
	Mode:    model.CaptureModeStop,
	Running: []composemodel.ContainerRef{dbReplica1},
}}
