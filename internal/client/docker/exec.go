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
	"cmp"
	"context"
	"fmt"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/greenmaskio/arcave/internal/model"
)

// defaultExecPoll is how often an exec whose output has ended is read until it reports its exit.
const defaultExecPoll = 50 * time.Millisecond

// ExecInContainer runs spec in the running container id and waits for it to exit, keeping the
// last model.ExecOutputTail bytes of its stdout and stderr. A container stopped, frozen or gone is
// model.ErrContainerNotRunning. When ctx ends first its error is returned and the process is left
// to run: the API cannot kill an exec.
func (e *Engine) ExecInContainer(ctx context.Context, id string, spec model.ExecSpec) (model.ExecResult, error) {
	created, err := e.api.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd:          spec.Cmd,
		User:         spec.User,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return model.ExecResult{}, e.wrapExecRefusal(ctx, id, "create exec in container "+id, err)
	}
	attached, err := e.api.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		// The container may stop between the create and the start.
		return model.ExecResult{}, e.wrapExecRefusal(ctx, id, "start exec "+created.ID, err)
	}
	defer attached.Close()
	// The hijacked connection outlives ctx: closing it ends the copy.
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()

	var stdout, stderr tailBuffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, attached.Reader)
	switch {
	case ctx.Err() != nil:
		return model.ExecResult{}, ctx.Err()
	case err != nil:
		return model.ExecResult{}, fmt.Errorf("read exec %s output: %w", created.ID, err)
	}
	exitCode, err := e.awaitExecExit(ctx, created.ID)
	if err != nil {
		return model.ExecResult{}, err
	}
	return model.ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

// wrapExecRefusal wraps err, the daemon refusing an exec into container id at action, as
// model.ErrContainerNotRunning when the container is idle.
func (e *Engine) wrapExecRefusal(ctx context.Context, id, action string, err error) error {
	if e.isContainerIdle(ctx, id, err) {
		return fmt.Errorf("exec in container %s: %w: %w", id, model.ErrContainerNotRunning, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

// isContainerIdle reports whether an exec was refused because the container is stopped, frozen or
// gone: Docker says so by a conflict, Podman by a server error, so the state is read.
func (e *Engine) isContainerIdle(ctx context.Context, id string, execErr error) bool {
	if cerrdefs.IsConflict(execErr) {
		return true
	}
	inspected, err := e.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return true
	case err != nil:
		return false
	}
	state := inspected.Container.State
	return state != nil && (!state.Running || state.Paused)
}

// awaitExecExit reads the exec until it no longer runs: its output may end before the daemon
// records its exit.
func (e *Engine) awaitExecExit(ctx context.Context, execID string) (int, error) {
	ticker := time.NewTicker(cmp.Or(e.execPoll, defaultExecPoll))
	defer ticker.Stop()
	for {
		inspected, err := e.api.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		switch {
		case ctx.Err() != nil:
			return 0, ctx.Err()
		case err != nil:
			return 0, fmt.Errorf("inspect exec %s: %w", execID, err)
		case !inspected.Running:
			return inspected.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

// tailBuffer keeps the last model.ExecOutputTail bytes written to it; nil for none.
type tailBuffer []byte

func (b *tailBuffer) Write(p []byte) (int, error) {
	*b = append(*b, p...)
	if excess := len(*b) - model.ExecOutputTail; excess > 0 {
		*b = (*b)[excess:]
	}
	return len(p), nil
}
