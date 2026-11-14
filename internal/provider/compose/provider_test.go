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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/captureplan"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
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
			runner.On("Stop", mock.Anything, demoSpec).Return([]string{"c1"}, tt.stopErr).Once()
			svc := New(Deps{ComposeRunner: runner})
			recorder := &composetest.StepRecorder{}

			// Act
			holding, err := svc.newCapturePlan(captureplan.Plan{Project: demoSpec, Services: stopServices}, model.IntrospectionResult{}).Hold(composetest.NewProgressContext(recorder))

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.IsType(t, heldWorkload{}, holding)
				assert.Equal(t, tt.wantKilled, holding.(heldWorkload).killed)
			} else {
				assert.EqualError(t, err, tt.wantErr)
				assert.Nil(t, holding)
			}
			assert.Equal(t, []progress.Kind{"test", kindSavepointHold}, recorder.Kinds)
			runner.AssertExpectations(t)
		})
	}
}

// The containers the hold's stop killed reach the committed record.
func TestHeld_Capture(t *testing.T) {
	// Arrange
	runner := &composetest.MockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return([]string{demoContainerID}, nil).Once()
	repo := &composetest.MockSavepointRepo{}
	spec := expectSpecCommit(repo)
	var committed model.Savepoint
	commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return savepoint.ID.Validate() == nil
	})).Run(func(args mock.Arguments) { committed = args.Get(1).(model.Savepoint) }).Return(nil).Once()
	mock.InOrder(stop, spec, commit)
	svc := New(Deps{
		SavepointRepo: repo,
		ComposeRunner: runner,
		Store:         composetest.NewDirStore(t),
	})
	plan := captureplan.Plan{
		Project:    demoSpec,
		Spec:       demoSpecBytes,
		Services:   stopServices,
		Containers: []model.ContainerRef{demoContainerRef()},
	}
	ctx := composetest.NewTestContext()
	holding, err := svc.newCapturePlan(plan, model.IntrospectionResult{}).Hold(ctx)
	require.NoError(t, err)

	// Act
	captured, err := holding.Capture(ctx, provider.CaptureRequest{Name: "sp"})
	require.NoError(t, err)
	_, err = captured.Commit(ctx)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, committed.NotCovered)
	assert.Equal(t, []model.KilledContainer{{Service: demoService, Replica: 1, Container: "demo-db-1"}},
		committed.NotCovered.KilledContainers)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
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
			runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(tt.startErr).Once()
			svc := New(Deps{ComposeRunner: runner})
			recorder := &composetest.StepRecorder{}
			ctx := composetest.NewProgressContext(recorder)

			// Act
			err := svc.newCapturePlan(captureplan.Plan{Project: demoSpec, Services: stopServices}, model.IntrospectionResult{}).Release(ctx, time.Minute)

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, []progress.Kind{"test", kindSavepointRelease}, recorder.Kinds)
			runner.AssertExpectations(t)
		})
	}
}

// A plan of every mode holds stop, then pause, and brings back live, pause, then stop — the
// resumed replicas' probes waited on before the start — whether the hold or the capture failed.
func TestCapturePlan_HoldCaptureRelease(t *testing.T) {
	errBoom := errors.New("boom")
	services := []captureplan.ServiceHold{
		{Service: "db", Mode: model.CaptureModeStop, Running: []model.ContainerRef{dbReplica1}},
		{Service: "web", Mode: model.CaptureModePause, Running: []model.ContainerRef{webReplica1},
			Idle: []captureplan.IdleReplica{{Ref: webReplica2, State: "paused"}}},
		{Service: "worker", Mode: model.CaptureModeLive, Idle: []captureplan.IdleReplica{{Ref: workerReplica1, State: "paused"}}},
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
				runner.On("StopServices", mock.Anything, demoSpec, []string{"db"}).Return(nil, nil).Once(),
				control.On("PauseContainers", mock.Anything, []string{"web1"}).Return(tt.pauseErr).Once(),
			}
			if tt.pauseErr == nil {
				calls = append(calls, repo.On("SaveSpec", mock.Anything, mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil }), demoSpecBytes).
					Return(tt.specErr).Once())
			}
			calls = append(calls,
				control.On("UnpauseContainers", mock.Anything, []string{"worker1"}).Return(nil).Once(),
				control.On("UnpauseContainers", mock.Anything, []string{"web1", "web2"}).Return(nil).Once(),
				control.On("AwaitHealthProbes", mock.Anything, []string{"web1", "web2", "worker1"}).Return(nil).Once(),
				runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once(),
			)
			mock.InOrder(calls...)
			svc := New(Deps{
				SavepointRepo:    repo,
				ComposeRunner:    runner,
				ContainerControl: control,
				Store:            composetest.NewDirStore(t),
			})
			plan := svc.newCapturePlan(captureplan.Plan{
				Project:  demoSpec,
				Spec:     demoSpecBytes,
				Services: services,
			}, model.IntrospectionResult{})
			ctx := composetest.NewTestContext()

			// Act
			holding, holdErr := plan.Hold(ctx)
			var captureErr error
			if holdErr == nil {
				_, captureErr = holding.Capture(ctx, provider.CaptureRequest{})
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

func TestFanOutLimit(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		want        int
	}{
		{name: "a configured concurrency is the limit", concurrency: 5, want: 5},
		{name: "one runs the fan-out serially", concurrency: 1, want: 1},
		{name: "zero leaves the fan-out unbounded", concurrency: 0, want: -1},
		{name: "a negative concurrency leaves it unbounded", concurrency: -3, want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := fanOutLimit(tt.concurrency)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// Replicas of the demo services.
var (
	dbReplica1     = model.ContainerRef{Service: "db", Replica: 1, ID: "db1", Name: "demo-db-1"}
	webReplica1    = model.ContainerRef{Service: "web", Replica: 1, ID: "web1", Name: "demo-web-1"}
	webReplica2    = model.ContainerRef{Service: "web", Replica: 2, ID: "web2", Name: "demo-web-2"}
	workerReplica1 = model.ContainerRef{Service: "worker", Replica: 1, ID: "worker1", Name: "demo-worker-1"}
)

// stopServices is a plan's services, every one held by stop and running.
var stopServices = []captureplan.ServiceHold{{
	Service: "db",
	Mode:    model.CaptureModeStop,
	Running: []model.ContainerRef{dbReplica1},
}}
