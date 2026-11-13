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

package capture

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// newMockCaptureTriggers returns n strict mock triggers, as mocks and as the triggers they form.
func newMockCaptureTriggers(n int) ([]*mockCaptureTrigger, captureTriggers) {
	mocks := make([]*mockCaptureTrigger, n)
	triggers := make(captureTriggers, n)
	for i := range n {
		mocks[i] = &mockCaptureTrigger{}
		triggers[i] = mocks[i]
	}
	return mocks, triggers
}

func TestCaptureTriggers_beforeCapture(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name string
		// errs fail each trigger's beforeCapture; a trigger past the first failure is not called.
		errs       []error
		wantKilled []string
		wantErr    string
	}{
		{
			name:       "every trigger in order, killed joined",
			errs:       []error{nil, nil, nil},
			wantKilled: []string{"c0", "c1", "c2"},
		},
		{
			name:    "a failure in the middle stops the rest",
			errs:    []error{nil, errBoom, nil},
			wantErr: "boom",
		},
		{
			name:    "a failure of the first stops every other",
			errs:    []error{errBoom, nil, nil},
			wantErr: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			mocks, triggers := newMockCaptureTriggers(len(tt.errs))
			var calls []*mock.Call
			for i, err := range tt.errs {
				calls = append(calls, mocks[i].On("beforeCapture", mock.Anything).
					Return([]string{"c" + strconv.Itoa(i)}, err).Once())
				if err != nil {
					break
				}
			}
			mock.InOrder(calls...)

			// Act
			killed, err := triggers.beforeCapture(context.Background())

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantKilled, killed)
			for _, trigger := range mocks {
				trigger.AssertExpectations(t)
			}
		})
	}
}

func TestCaptureTriggers_afterCapture(t *testing.T) {
	tests := []struct {
		name string
		// errs fail each trigger's afterCapture.
		errs    []error
		wantErr string
		// wantLog is logged; wantNoLog is not.
		wantLog   []string
		wantNoLog []string
	}{
		{
			name:      "every trigger, last to first",
			errs:      []error{nil, nil, nil},
			wantNoLog: []string{"capture trigger failed after the capture"},
		},
		{
			name:      "a failure runs the rest and the first is returned",
			errs:      []error{errors.New("first"), nil, errors.New("last")},
			wantErr:   "last",
			wantLog:   []string{"capture trigger failed after the capture", "first"},
			wantNoLog: []string{"last"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())
			mocks, triggers := newMockCaptureTriggers(len(tt.errs))
			calls := make([]*mock.Call, len(tt.errs))
			for i, err := range tt.errs {
				calls[len(tt.errs)-1-i] = mocks[i].On("afterCapture", mock.Anything).Return(err).Once()
			}
			mock.InOrder(calls...)

			// Act
			err := triggers.afterCapture(ctx)

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			for _, line := range tt.wantLog {
				assert.Contains(t, logs.String(), line)
			}
			for _, line := range tt.wantNoLog {
				assert.NotContains(t, logs.String(), line)
			}
			for _, trigger := range mocks {
				trigger.AssertExpectations(t)
			}
		})
	}
}

// A failed beforeCapture of the second still runs every trigger's afterCapture, last to first.
func TestCaptureTriggers_afterCapture_AfterAFailedBefore(t *testing.T) {
	// Arrange
	ctx := context.Background()
	mocks, triggers := newMockCaptureTriggers(3)
	mock.InOrder(
		mocks[0].On("beforeCapture", mock.Anything).Return(nil, nil).Once(),
		mocks[1].On("beforeCapture", mock.Anything).Return(nil, errors.New("boom")).Once(),
		mocks[2].On("afterCapture", mock.Anything).Return(nil).Once(),
		mocks[1].On("afterCapture", mock.Anything).Return(nil).Once(),
		mocks[0].On("afterCapture", mock.Anything).Return(nil).Once(),
	)

	// Act
	_, beforeErr := triggers.beforeCapture(ctx)
	afterErr := triggers.afterCapture(ctx)

	// Assert
	assert.EqualError(t, beforeErr, "boom")
	assert.NoError(t, afterErr)
	for _, trigger := range mocks {
		trigger.AssertExpectations(t)
	}
}

// Replicas of the demo services, as the trigger tests name them.
var (
	dbReplica1     = model.ContainerRef{Service: "db", Replica: 1, ID: "db1", Name: "demo-db-1"}
	webReplica1    = model.ContainerRef{Service: "web", Replica: 1, ID: "web1", Name: "demo-web-1"}
	webReplica2    = model.ContainerRef{Service: "web", Replica: 2, ID: "web2", Name: "demo-web-2"}
	workerReplica1 = model.ContainerRef{Service: "worker", Replica: 1, ID: "worker1", Name: "demo-worker-1"}
)

// stopServices is a plan's services, every one held by stop and running.
var stopServices = []ServiceHold{{Service: "db", Mode: model.CaptureModeStop, Running: []model.ContainerRef{dbReplica1}}}

// newProgressContext is a context under a root step reporting to recorder.
func newProgressContext(recorder progress.Renderer) context.Context {
	ctx := zerolog.Nop().WithContext(context.Background())
	ctx, _ = progress.Begin(progress.WithTracker(ctx, progress.New(recorder, time.Now)), progress.Step{Kind: "test"})
	return ctx
}

func TestStopTrigger_beforeCapture(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name          string
		services      []string
		allServices   bool
		pausedOnEntry []model.ContainerRef
		unpauseErr    error
		stopErr       error
		// wantStop and wantStopServices are the calls expected; neither is no stop at all.
		wantStop         bool
		wantStopServices bool
		wantKilled       []string
		wantWarnings     []string
		wantErr          string
	}{
		{
			name:        "every service is stop: the project is stopped",
			services:    []string{"db"},
			allServices: true,
			wantStop:    true,
			wantKilled:  []string{"c1"},
		},
		{
			name:             "some services are stop: those are stopped",
			services:         []string{"db", "web"},
			wantStopServices: true,
			wantKilled:       []string{"c1"},
		},
		{
			name: "no service is stop: nothing is stopped",
		},
		{
			name:          "a replica paused on entry is resumed before the stop",
			services:      []string{"db"},
			allServices:   true,
			pausedOnEntry: []model.ContainerRef{dbReplica1},
			wantStop:      true,
			wantKilled:    []string{"c1"},
			wantWarnings:  []string{"db-1 was paused: resuming it to stop it"},
		},
		{
			name:          "a failed resume stops nothing",
			services:      []string{"db"},
			allServices:   true,
			pausedOnEntry: []model.ContainerRef{dbReplica1},
			unpauseErr:    errBoom,
			wantWarnings:  []string{"db-1 was paused: resuming it to stop it"},
			wantErr:       "resuming containers: boom",
		},
		{
			name:        "a failed stop",
			services:    []string{"db"},
			allServices: true,
			stopErr:     errBoom,
			wantStop:    true,
			wantErr:     "stopping services: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &mockComposeRunner{}
			control := &mockContainerControl{}
			var calls []*mock.Call
			if len(tt.pausedOnEntry) > 0 {
				calls = append(calls, control.On("UnpauseContainers", mock.Anything, []string{"db1"}).
					Return(tt.unpauseErr).Once())
			}
			if tt.wantStop {
				calls = append(calls, runner.On("Stop", mock.Anything, demoSpec).
					Return([]string{"c1"}, tt.stopErr).Once())
			}
			if tt.wantStopServices {
				calls = append(calls, runner.On("StopServices", mock.Anything, demoSpec, tt.services).
					Return([]string{"c1"}, tt.stopErr).Once())
			}
			mock.InOrder(calls...)
			trigger := stopTrigger{
				composeRunner: runner,
				control:       control,
				project:       demoSpec,
				waitTimeout:   time.Minute,
				services:      tt.services,
				allServices:   tt.allServices,
				pausedOnEntry: tt.pausedOnEntry,
			}
			recorder := &stepRecorder{}

			// Act
			killed, err := trigger.beforeCapture(newProgressContext(recorder))

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantKilled, killed)
			assert.Equal(t, tt.wantWarnings, recorder.warnings)
			runner.AssertExpectations(t)
			control.AssertExpectations(t)
		})
	}
}

func TestStopTrigger_afterCapture(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name     string
		resumed  []model.ContainerRef
		awaitErr error
		startErr error
		// wantAwait are the containers whose health probes are waited on; nil is no wait.
		wantAwait []string
		wantErr   string
	}{
		{
			name: "starts the project",
		},
		{
			name:      "the resumed replicas' probes are waited on before the start",
			resumed:   []model.ContainerRef{webReplica1, workerReplica1},
			wantAwait: []string{"web1", "worker1"},
		},
		{
			name:      "a failed wait still starts the project",
			resumed:   []model.ContainerRef{webReplica1},
			awaitErr:  errBoom,
			wantAwait: []string{"web1"},
		},
		{
			name:     "a failed start",
			startErr: errBoom,
			wantErr:  "starting services: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &mockComposeRunner{}
			control := &mockContainerControl{}
			var calls []*mock.Call
			if tt.wantAwait != nil {
				calls = append(calls, control.On("AwaitHealthProbes", mock.Anything, tt.wantAwait).
					Return(tt.awaitErr).Once())
			}
			calls = append(calls, runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(tt.startErr).Once())
			mock.InOrder(calls...)
			trigger := stopTrigger{
				composeRunner: runner,
				control:       control,
				project:       demoSpec,
				waitTimeout:   time.Minute,
				resumed:       tt.resumed,
			}

			// Act
			err := trigger.afterCapture(zerolog.Nop().WithContext(context.Background()))

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			runner.AssertExpectations(t)
			control.AssertExpectations(t)
		})
	}
}

func TestPauseTrigger_beforeCapture(t *testing.T) {
	tests := []struct {
		name      string
		running   []model.ContainerRef
		pauseErr  error
		wantPause []string
		wantErr   string
	}{
		{
			name:      "freezes the running replicas",
			running:   []model.ContainerRef{webReplica1, webReplica2},
			wantPause: []string{"web1", "web2"},
		},
		{
			name: "no running replica: no call",
		},
		{
			name:      "a failed pause",
			running:   []model.ContainerRef{webReplica1},
			pauseErr:  errors.New("boom"),
			wantPause: []string{"web1"},
			wantErr:   "pausing containers: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &mockContainerControl{}
			if tt.wantPause != nil {
				control.On("PauseContainers", mock.Anything, tt.wantPause).Return(tt.pauseErr).Once()
			}
			trigger := pauseTrigger{control: control, running: tt.running}

			// Act
			killed, err := trigger.beforeCapture(context.Background())

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Empty(t, killed)
			control.AssertExpectations(t)
		})
	}
}

func TestPauseTrigger_afterCapture(t *testing.T) {
	tests := []struct {
		name          string
		running       []model.ContainerRef
		pausedOnEntry []model.ContainerRef
		unpauseErr    error
		wantUnpause   []string
		wantWarnings  []string
		wantErr       string
	}{
		{
			name:        "resumes its own",
			running:     []model.ContainerRef{webReplica1},
			wantUnpause: []string{"web1"},
		},
		{
			name:          "resumes the ones frozen before it too, with a warning",
			running:       []model.ContainerRef{webReplica1},
			pausedOnEntry: []model.ContainerRef{webReplica2},
			wantUnpause:   []string{"web1", "web2"},
			wantWarnings:  []string{"web-2 was paused before the capture: resuming it"},
		},
		{
			name: "no replica: no call",
		},
		{
			name:        "a failed resume",
			running:     []model.ContainerRef{webReplica1},
			unpauseErr:  errors.New("boom"),
			wantUnpause: []string{"web1"},
			wantErr:     "resuming containers: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			control := &mockContainerControl{}
			if tt.wantUnpause != nil {
				control.On("UnpauseContainers", mock.Anything, tt.wantUnpause).Return(tt.unpauseErr).Once()
			}
			trigger := pauseTrigger{control: control, running: tt.running, pausedOnEntry: tt.pausedOnEntry}
			recorder := &stepRecorder{}

			// Act
			err := trigger.afterCapture(newProgressContext(recorder))

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantWarnings, recorder.warnings)
			control.AssertExpectations(t)
		})
	}
}

func TestLiveTrigger_afterCapture(t *testing.T) {
	// Arrange
	control := &mockContainerControl{}
	control.On("UnpauseContainers", mock.Anything, []string{"worker1"}).Return(nil).Once()
	trigger := liveTrigger{control: control, pausedOnEntry: []model.ContainerRef{workerReplica1}}
	recorder := &warnedRecorder{}

	// Act
	err := trigger.afterCapture(newProgressContext(recorder))

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, []string{"worker-1 was paused before the capture: resuming it"}, recorder.msgs)
	control.AssertExpectations(t)
}

func TestNewCaptureTriggersByMode(t *testing.T) {
	pausedWeb := IdleReplica{Ref: webReplica2, State: "paused"}
	exitedWeb := IdleReplica{Ref: webReplica2, State: "exited"}
	pausedWorker := IdleReplica{Ref: workerReplica1, State: "paused"}

	tests := []struct {
		name     string
		services []ServiceHold
		want     func(runner composeRunner, control containerControl) captureTriggers
	}{
		{
			name: "each group its own trigger, stop then pause then live",
			services: []ServiceHold{
				{Service: "db", Mode: model.CaptureModeStop, Running: []model.ContainerRef{dbReplica1}},
				{Service: "web", Mode: model.CaptureModePause, Running: []model.ContainerRef{webReplica1}, Idle: []IdleReplica{pausedWeb}},
				{Service: "worker", Mode: model.CaptureModeLive, Idle: []IdleReplica{pausedWorker}},
			},
			want: func(runner composeRunner, control containerControl) captureTriggers {
				return captureTriggers{
					stopTrigger{composeRunner: runner, control: control, project: demoSpec, waitTimeout: time.Minute,
						services: []string{"db"},
						resumed:  []model.ContainerRef{webReplica1, webReplica2, workerReplica1}},
					pauseTrigger{control: control, running: []model.ContainerRef{webReplica1},
						pausedOnEntry: []model.ContainerRef{webReplica2}},
					liveTrigger{control: control, pausedOnEntry: []model.ContainerRef{workerReplica1}},
				}
			},
		},
		{
			name: "every service stop, an unknown mode stopped too",
			services: []ServiceHold{
				{Service: "db", Mode: model.CaptureModeStop, Running: []model.ContainerRef{dbReplica1}},
				{Service: "web", Idle: []IdleReplica{exitedWeb, pausedWeb}},
			},
			want: func(runner composeRunner, control containerControl) captureTriggers {
				return captureTriggers{
					stopTrigger{composeRunner: runner, control: control, project: demoSpec, waitTimeout: time.Minute,
						services: []string{"db", "web"}, allServices: true, pausedOnEntry: []model.ContainerRef{webReplica2}},
					pauseTrigger{control: control},
					liveTrigger{control: control},
				}
			},
		},
		{
			name: "every service live: a stop trigger with no service, to start the project after",
			services: []ServiceHold{
				{Service: "db", Mode: model.CaptureModeLive, Running: []model.ContainerRef{dbReplica1}},
			},
			want: func(runner composeRunner, control containerControl) captureTriggers {
				return captureTriggers{
					stopTrigger{composeRunner: runner, control: control, project: demoSpec, waitTimeout: time.Minute},
					pauseTrigger{control: control},
					liveTrigger{control: control},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &mockComposeRunner{}
			control := &mockContainerControl{}
			plan := Plan{Project: demoSpec, WaitTimeout: time.Minute, Services: tt.services}

			// Act
			triggers := newCaptureTriggersByMode(runner, control, plan)

			// Assert
			assert.Equal(t, tt.want(runner, control), triggers)
		})
	}
}
