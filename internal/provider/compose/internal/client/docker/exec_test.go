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
	"io"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

const (
	execContainerID = "c1"
	hookExecID      = "exec-1"
)

// expectExecCreate expects the exec of the hook spec in the container.
func expectExecCreate(api *mockAPI) *mock.Call {
	return api.On("ExecCreate", mock.Anything, execContainerID, client.ExecCreateOptions{
		Cmd:          []string{"sh", "-c", "pg_ctl checkpoint"},
		User:         "postgres",
		AttachStdout: true,
		AttachStderr: true,
	}).Once()
}

// hookSpec is the exec the tests run.
var hookSpec = composemodel.ExecSpec{
	Cmd:  []string{"sh", "-c", "pg_ctl checkpoint"},
	User: "postgres",
}

// The exec's streams are kept apart, each to its tail, and its exit is read once it stops
// running: the output may end before the daemon records the exit.
func TestEngine_ExecInContainer(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	go func() {
		_, _ = io.WriteString(hj.stdout, strings.Repeat("o", composemodel.ExecOutputTail)+"out-end")
		_, _ = io.WriteString(hj.stderr, "connection refused")
		hj.closeOutput()
	}()
	api := &mockAPI{}
	create := expectExecCreate(api).Return(client.ExecCreateResult{ID: hookExecID}, nil)
	attach := api.On("ExecAttach", mock.Anything, hookExecID, client.ExecAttachOptions{}).Return(hj.resp, nil).Once()
	running := api.On("ExecInspect", mock.Anything, hookExecID, client.ExecInspectOptions{}).
		Return(client.ExecInspectResult{Running: true}, nil).Once()
	exited := api.On("ExecInspect", mock.Anything, hookExecID, client.ExecInspectOptions{}).
		Return(client.ExecInspectResult{ExitCode: 3}, nil).Once()
	mock.InOrder(create, attach, running, exited)
	e := &Engine{api: api, execPoll: time.Millisecond}

	// Act
	got, err := e.ExecInContainer(context.Background(), execContainerID, hookSpec)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 3, got.ExitCode)
	assert.Len(t, got.Stdout, composemodel.ExecOutputTail)
	assert.True(t, strings.HasSuffix(string(got.Stdout), "out-end"))
	assert.Equal(t, "connection refused", string(got.Stderr))
	api.AssertExpectations(t)
}

func TestEngine_ExecInContainer_Fails(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name      string
		createErr error
		// state and stateErr answer the inspect of the container whose exec was refused; nil and
		// nil is no inspect.
		state          *container.State
		stateErr       error
		attachErr      error
		execInspectErr error
		wantIs         error
		wantErr        string
	}{
		{
			name:      "a conflict is a container not running, as Docker says it",
			createErr: cerrdefs.ErrConflict.WithMessage("container c1 is not running"),
			wantIs:    composemodel.ErrContainerNotRunning,
		},
		{
			name:      "a refusal of a stopped container, as Podman says it",
			createErr: cerrdefs.ErrInternal.WithMessage("container state improper"),
			state:     &container.State{Status: container.StateExited},
			wantIs:    composemodel.ErrContainerNotRunning,
		},
		{
			name:      "a refusal of a frozen container",
			createErr: cerrdefs.ErrInternal.WithMessage("container state improper"),
			state:     &container.State{Status: container.StatePaused, Running: true, Paused: true},
			wantIs:    composemodel.ErrContainerNotRunning,
		},
		{
			name:      "a refusal of a container gone",
			createErr: errBoom,
			stateErr:  cerrdefs.ErrNotFound.WithMessage("no such container"),
			wantIs:    composemodel.ErrContainerNotRunning,
		},
		{
			name:      "a refusal of a running container is the daemon's",
			createErr: errBoom,
			state:     &container.State{Status: container.StateRunning, Running: true},
			wantErr:   "create exec in container c1: boom",
		},
		{
			name:      "a refusal whose container cannot be read is the daemon's",
			createErr: errBoom,
			stateErr:  errors.New("unreachable"),
			wantErr:   "create exec in container c1: boom",
		},
		{
			name:      "a container that stopped before the start is not running",
			attachErr: cerrdefs.ErrConflict.WithMessage("container c1 is not running"),
			wantIs:    composemodel.ErrContainerNotRunning,
		},
		{
			name:      "a failed start of a running container",
			attachErr: errBoom,
			state:     &container.State{Status: container.StateRunning, Running: true},
			wantErr:   "start exec exec-1: boom",
		},
		{
			name:           "a failed inspect",
			execInspectErr: errBoom,
			wantErr:        "inspect exec exec-1: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			hj := newFakeHijack()
			hj.closeOutput()
			api := &mockAPI{}
			expectExecCreate(api).Return(client.ExecCreateResult{ID: hookExecID}, tt.createErr)
			if tt.state != nil || tt.stateErr != nil {
				api.On("ContainerInspect", mock.Anything, execContainerID, client.ContainerInspectOptions{}).
					Return(container.InspectResponse{State: tt.state}, tt.stateErr).Once()
			}
			if tt.createErr == nil {
				api.On("ExecAttach", mock.Anything, hookExecID, client.ExecAttachOptions{}).Return(hj.resp, tt.attachErr).Once()
			}
			if tt.createErr == nil && tt.attachErr == nil {
				api.On("ExecInspect", mock.Anything, hookExecID, client.ExecInspectOptions{}).
					Return(client.ExecInspectResult{}, tt.execInspectErr).Once()
			}
			e := &Engine{api: api}

			// Act
			got, err := e.ExecInContainer(context.Background(), execContainerID, hookSpec)

			// Assert
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Zero(t, got)
			api.AssertExpectations(t)
		})
	}
}

// A broken output stream, with the context still live, is the daemon's failure.
func TestEngine_ExecInContainer_BrokenOutput(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	go func() { _ = hj.outW.CloseWithError(errors.New("connection reset")) }()
	api := &mockAPI{}
	expectExecCreate(api).Return(client.ExecCreateResult{ID: hookExecID}, nil)
	api.On("ExecAttach", mock.Anything, hookExecID, client.ExecAttachOptions{}).Return(hj.resp, nil).Once()
	e := &Engine{api: api}

	// Act
	got, err := e.ExecInContainer(context.Background(), execContainerID, hookSpec)

	// Assert
	assert.EqualError(t, err, "read exec exec-1 output: connection reset")
	assert.Zero(t, got)
	api.AssertExpectations(t)
}

// A context that ends while the exec is read until it exits ends the wait with its error.
func TestEngine_ExecInContainer_ContextEndsAwaitingTheExit(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	hj.closeOutput()
	api := &mockAPI{}
	ctx, cancel := context.WithCancel(context.Background())
	expectExecCreate(api).Return(client.ExecCreateResult{ID: hookExecID}, nil)
	api.On("ExecAttach", mock.Anything, hookExecID, client.ExecAttachOptions{}).Return(hj.resp, nil).Once()
	api.On("ExecInspect", mock.Anything, hookExecID, client.ExecInspectOptions{}).
		Run(func(mock.Arguments) { cancel() }).
		Return(client.ExecInspectResult{Running: true}, nil).Once()
	e := &Engine{api: api, execPoll: time.Hour}

	// Act
	got, err := e.ExecInContainer(ctx, execContainerID, hookSpec)

	// Assert
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, got)
	api.AssertExpectations(t)
}

// A context that ends while the exec runs ends the wait with its error; the exec is left to run.
func TestEngine_ExecInContainer_ContextEnds(t *testing.T) {
	// Arrange: an exec that prints nothing and never exits.
	hj := newFakeHijack()
	api := &mockAPI{}
	expectExecCreate(api).Return(client.ExecCreateResult{ID: hookExecID}, nil)
	api.On("ExecAttach", mock.Anything, hookExecID, client.ExecAttachOptions{}).Return(hj.resp, nil).Once()
	e := &Engine{api: api}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	// Act
	got, err := e.ExecInContainer(ctx, execContainerID, hookSpec)

	// Assert
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Zero(t, got)
	api.AssertExpectations(t)
}
