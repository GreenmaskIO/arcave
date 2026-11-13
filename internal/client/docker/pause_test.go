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

package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

var errDaemon = errors.New("daemon refused")

func TestEngine_PauseContainers(t *testing.T) {
	tests := []struct {
		name     string
		pauseErr error
		wantErr  string
	}{
		{
			name: "every container is paused",
		},
		{
			name:     "a refused pause fails",
			pauseErr: errDaemon,
			wantErr:  "pause container b: daemon refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			api.On("ContainerPause", mock.Anything, "a", client.ContainerPauseOptions{}).Return(nil).Once()
			api.On("ContainerPause", mock.Anything, "b", client.ContainerPauseOptions{}).Return(tt.pauseErr).Once()
			e := &Engine{api: api}

			// Act
			err := e.PauseContainers(context.Background(), []string{"a", "b"})

			// Assert
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			api.AssertExpectations(t)
		})
	}
}

func TestEngine_UnpauseContainers(t *testing.T) {
	tests := []struct {
		name        string
		inspect     container.InspectResponse
		inspectErr  error
		wantUnpause bool
		unpauseErr  error
		wantErr     string
	}{
		{
			name:        "a paused container is unpaused",
			inspect:     inspectState(container.State{Status: container.StatePaused, Running: true, Paused: true}),
			wantUnpause: true,
		},
		{
			name:    "a running container is left alone",
			inspect: inspectState(container.State{Status: container.StateRunning, Running: true}),
		},
		{
			name:    "a stopped container is left alone",
			inspect: inspectState(container.State{Status: container.StateExited}),
		},
		{
			name:       "a container gone before the inspect is no error",
			inspectErr: cerrdefs.ErrNotFound,
		},
		{
			name:        "a container gone before the unpause is no error",
			inspect:     inspectState(container.State{Status: container.StatePaused, Running: true, Paused: true}),
			wantUnpause: true,
			unpauseErr:  cerrdefs.ErrNotFound,
		},
		{
			name:       "a failed inspect fails",
			inspectErr: errDaemon,
			wantErr:    "inspect container a: daemon refused",
		},
		{
			name:        "a refused unpause fails",
			inspect:     inspectState(container.State{Status: container.StatePaused, Running: true, Paused: true}),
			wantUnpause: true,
			unpauseErr:  errDaemon,
			wantErr:     "unpause container a: daemon refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			inspect := api.On("ContainerInspect", mock.Anything, "a", client.ContainerInspectOptions{}).
				Return(tt.inspect, tt.inspectErr).Once()
			if tt.wantUnpause {
				api.On("ContainerUnpause", mock.Anything, "a", client.ContainerUnpauseOptions{}).
					Return(tt.unpauseErr).Once().NotBefore(inspect)
			}
			e := &Engine{api: api}

			// Act
			err := e.UnpauseContainers(context.Background(), []string{"a"})

			// Assert
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			api.AssertExpectations(t)
		})
	}
}

// A container reading the unhealthy a pause leaves is waited on until a probe lands; one a probe
// failed, or one with no health, is not.
func TestEngine_AwaitHealthProbes(t *testing.T) {
	healthcheck := &container.Config{Healthcheck: &container.HealthConfig{Interval: time.Second}}
	staleForever := &container.Config{Healthcheck: &container.HealthConfig{Interval: time.Millisecond, Timeout: time.Millisecond}}
	withHealth := func(config *container.Config, status container.HealthStatus, streak int) container.InspectResponse {
		return container.InspectResponse{
			Config: config,
			State: &container.State{
				Status:  container.StateRunning,
				Running: true,
				Health:  &container.Health{Status: status, FailingStreak: streak},
			},
		}
	}

	tests := []struct {
		name string
		// reads are what the inspects answer, in order; readErr fails the last of them.
		reads   []container.InspectResponse
		readErr error
		// staleUntilBound reads a stale health on every inspect until the bound.
		staleUntilBound bool
		wantErr         string
	}{
		{
			name: "a stale unhealthy is read until the probe",
			reads: []container.InspectResponse{
				withHealth(healthcheck, container.Unhealthy, 0),
				withHealth(healthcheck, container.Unhealthy, 0),
				withHealth(healthcheck, container.Healthy, 0),
			},
		},
		{
			name:  "an unhealthy a probe failed is not waited on",
			reads: []container.InspectResponse{withHealth(healthcheck, container.Unhealthy, 3)},
		},
		{
			name:  "a healthy container is not waited on",
			reads: []container.InspectResponse{withHealth(healthcheck, container.Healthy, 0)},
		},
		{
			name:  "no healthcheck is not waited on",
			reads: []container.InspectResponse{inspectState(container.State{Status: container.StateRunning, Running: true})},
		},
		{
			name:            "a health still stale at the bound is the caller's to judge",
			staleUntilBound: true,
		},
		{
			name:    "a container gone before the wait is no error",
			reads:   []container.InspectResponse{{}},
			readErr: cerrdefs.ErrNotFound,
		},
		{
			name:    "a container gone during the wait is no error",
			reads:   []container.InspectResponse{withHealth(healthcheck, container.Unhealthy, 0), {}},
			readErr: cerrdefs.ErrNotFound,
		},
		{
			name:    "a failed read fails",
			reads:   []container.InspectResponse{{}},
			readErr: errDaemon,
			wantErr: "inspect container a: daemon refused",
		},
		{
			name:    "a failed read during the wait fails",
			reads:   []container.InspectResponse{withHealth(healthcheck, container.Unhealthy, 0), {}},
			readErr: errDaemon,
			wantErr: "inspect container a: daemon refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			var calls []*mock.Call
			for i, read := range tt.reads {
				var err error
				if i == len(tt.reads)-1 {
					err = tt.readErr
				}
				calls = append(calls, api.On("ContainerInspect", mock.Anything, "a", client.ContainerInspectOptions{}).
					Return(read, err).Once())
			}
			if tt.staleUntilBound {
				calls = append(calls, api.On("ContainerInspect", mock.Anything, "a", client.ContainerInspectOptions{}).
					Return(withHealth(staleForever, container.Unhealthy, 0), nil))
			}
			mock.InOrder(calls...)
			e := &Engine{api: api, healthPoll: time.Millisecond}

			// Act
			err := e.AwaitHealthProbes(context.Background(), []string{"a"})

			// Assert
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			api.AssertExpectations(t)
		})
	}
}

// A cancelled wait returns the cancellation's cause.
func TestEngine_AwaitHealthProbes_Cancelled(t *testing.T) {
	// Arrange
	errInterrupted := errors.New("interrupted")
	ctx, cancel := context.WithCancelCause(context.Background())
	stale := container.InspectResponse{
		Config: &container.Config{Healthcheck: &container.HealthConfig{Interval: time.Hour}},
		State: &container.State{
			Status:  container.StateRunning,
			Running: true,
			Health:  &container.Health{Status: container.Unhealthy},
		},
	}
	api := &mockAPI{}
	api.On("ContainerInspect", mock.Anything, "a", client.ContainerInspectOptions{}).Return(stale, nil).Once()
	api.On("ContainerInspect", mock.Anything, "a", client.ContainerInspectOptions{}).
		Run(func(mock.Arguments) { cancel(errInterrupted) }).Return(stale, nil).Once()
	e := &Engine{api: api, healthPoll: time.Millisecond}

	// Act
	err := e.AwaitHealthProbes(ctx, []string{"a"})

	// Assert
	assert.ErrorIs(t, err, errInterrupted)
	api.AssertExpectations(t)
}

func inspectState(state container.State) container.InspectResponse {
	return container.InspectResponse{State: &state}
}
