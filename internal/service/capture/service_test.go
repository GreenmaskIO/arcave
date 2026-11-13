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
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestService_Hold(t *testing.T) {
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
			runner := &mockComposeRunner{}
			runner.On("Stop", mock.Anything, demoSpec).Return([]string{"c1"}, tt.stopErr).Once()
			svc := New(Deps{ComposeRunner: runner})
			recorder := &stepRecorder{}

			// Act
			killed, err := svc.Hold(newProgressContext(recorder), Plan{Project: demoSpec, WaitTimeout: time.Minute, Services: stopServices})

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantKilled, killed)
			assert.Equal(t, []progress.Kind{"test", kindSavepointHold}, recorder.kinds)
			runner.AssertExpectations(t)
		})
	}
}

// Release starts the project on a live context even when the one it is handed was cancelled.
func TestService_Release(t *testing.T) {
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
			runner := &mockComposeRunner{}
			revertCtx := mock.MatchedBy(func(ctx context.Context) bool {
				_, bounded := ctx.Deadline()
				return ctx.Err() == nil && bounded
			})
			runner.On("Start", revertCtx, demoSpec, time.Minute).Return(tt.startErr).Once()
			svc := New(Deps{ComposeRunner: runner})
			recorder := &stepRecorder{}
			ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(newProgressContext(recorder)))
			cancel()

			// Act
			err := svc.Release(ctx, Plan{Project: demoSpec, WaitTimeout: time.Minute, Services: stopServices})

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, []progress.Kind{"test", kindSavepointRelease}, recorder.kinds)
			runner.AssertExpectations(t)
		})
	}
}

// A plan of every mode holds stop, then pause, and brings back live, pause, then stop — the
// resumed replicas' probes waited on before the start — whether the hold or the capture failed.
func TestService_HoldCaptureRelease(t *testing.T) {
	errBoom := errors.New("boom")
	services := []ServiceHold{
		{Service: "db", Mode: model.CaptureModeStop, Running: []model.ContainerRef{dbReplica1}},
		{Service: "web", Mode: model.CaptureModePause, Running: []model.ContainerRef{webReplica1},
			Idle: []IdleReplica{{Ref: webReplica2, State: "paused"}}},
		{Service: "worker", Mode: model.CaptureModeLive, Idle: []IdleReplica{{Ref: workerReplica1, State: "paused"}}},
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
			runner := &mockComposeRunner{}
			control := &mockContainerControl{}
			repo := &mockSavepointRepo{}
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
				Store:            newDirStore(t),
			})
			plan := Plan{
				Project:     demoSpec,
				Spec:        demoSpecBytes,
				WaitTimeout: time.Minute,
				Services:    services,
			}
			ctx := newTestContext()

			// Act
			killed, holdErr := svc.Hold(ctx, plan)
			var captureErr error
			if holdErr == nil {
				_, captureErr = svc.Capture(ctx, fakeGrantedSessionLock{}, plan, killed, Request{})
			}
			releaseErr := svc.Release(ctx, plan)

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
