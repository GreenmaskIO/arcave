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

package test

import (
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// An exec keeps its streams apart and reports its exit; a replica frozen, stopped or gone is
// composemodel.ErrContainerNotRunning, whichever engine says it how.
func TestEngine_ExecInContainer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.up(t, 3)
	engine := newRuntimeProfile(t, env.daemon).Engine
	replicas := env.replicas(t)
	running, paused, stopped := replicas[0], replicas[1], replicas[2]
	require.NoError(t, engine.PauseContainers(env.ctx, []string{paused}))
	t.Cleanup(func() { _ = engine.UnpauseContainers(env.ctx, []string{paused}) })
	_, err := env.daemon.ContainerStop(env.ctx, stopped, client.ContainerStopOptions{})
	require.NoError(t, err)

	// Act
	result, runErr := engine.ExecInContainer(env.ctx, running, composemodel.ExecSpec{
		Cmd:  []string{"sh", "-c", "echo out; echo err >&2; exit 3"},
		User: "nobody",
	})
	_, pausedErr := engine.ExecInContainer(env.ctx, paused, composemodel.ExecSpec{Cmd: []string{"true"}})
	_, stoppedErr := engine.ExecInContainer(env.ctx, stopped, composemodel.ExecSpec{Cmd: []string{"true"}})
	_, goneErr := engine.ExecInContainer(env.ctx, "arcave-no-such-container", composemodel.ExecSpec{Cmd: []string{"true"}})

	// Assert
	require.NoError(t, runErr)
	assert.Equal(t, composemodel.ExecResult{ExitCode: 3, Stdout: []byte("out\n"), Stderr: []byte("err\n")}, result)
	assert.ErrorIs(t, pausedErr, composemodel.ErrContainerNotRunning)
	assert.ErrorIs(t, stoppedErr, composemodel.ErrContainerNotRunning)
	assert.ErrorIs(t, goneErr, composemodel.ErrContainerNotRunning)
}
