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
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/service/composespec"
)

// gracefulSpec is a service that takes 3s to shut down on TERM and declares the time
// it needs; the configured floor alone (1s) would kill it mid-shutdown.
const gracefulSpec = `
name: %s
services:
  db:
    image: busybox
    stop_grace_period: 8s
    entrypoint: ["sh", "-c", "trap 'sleep 3; touch /data/clean; exit 0' TERM; while true; do sleep 1; done"]
    volumes:
      - data:/data
volumes:
  data: {}
`

// stubbornSpec is a service that ignores TERM and declares no grace period.
const stubbornSpec = `
name: %s
services:
  db:
    image: busybox
    entrypoint: ["sh", "-c", "trap '' TERM; while true; do sleep 1; done"]
`

func TestClient_Stop_HonoursTheDeclaredGracePeriod(t *testing.T) {
	// Arrange
	env := newStopEnv(t)
	project := env.up(t, gracefulSpec)

	// Act
	err := env.compose.Stop(env.ctx, project)

	// Assert
	require.NoError(t, err)
	inspect, err := env.daemon.ContainerInspect(env.ctx, env.project+"-db-1")
	require.NoError(t, err)
	assert.Equal(t, 0, inspect.State.ExitCode, "the service finished its shutdown")
	assert.Equal(t, "clean", busybox(t, env.engine, env.project+"_data", "test -f /data/clean && printf clean"))
	assert.NotContains(t, env.logs.String(), "was killed")
}

func TestClient_Stop_WarnsOfAKilledContainer(t *testing.T) {
	// Arrange
	env := newStopEnv(t)
	project := env.up(t, stubbornSpec)

	// Act
	err := env.compose.Stop(env.ctx, project)

	// Assert
	require.NoError(t, err, "a killed container is warned of, not failed on")
	inspect, err := env.daemon.ContainerInspect(env.ctx, env.project+"-db-1")
	require.NoError(t, err)
	assert.Equal(t, 137, inspect.State.ExitCode)
	assert.Contains(t, env.logs.String(), fmt.Sprintf("container %s-db-1 did not exit within 1s and was killed", env.project))
}

type stopEnv struct {
	ctx     context.Context
	compose *composeclient.Client
	engine  *dockerclient.Engine
	daemon  *client.Client
	logs    *syncBuffer
	project string
}

// newStopEnv is a compose client whose configured grace period is 1s, logging into a buffer.
func newStopEnv(t *testing.T) *stopEnv {
	t.Helper()

	daemonAPI := newDaemonClient(t)
	project := "arcave-stop-grace-it-" + randomSuffix(t)

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	progress.Mode = progress.ModeQuiet

	logs := &syncBuffer{}
	env := &stopEnv{
		ctx: zerolog.New(logs).WithContext(context.Background()),
		compose: composeclient.NewClient(dockercompose.NewComposeService(cli), composeclient.Options{
			WaitTimeout: 90 * time.Second, StopTimeout: time.Second,
		}),
		engine:  dockerclient.NewEngine(daemonAPI),
		daemon:  daemonAPI,
		logs:    logs,
		project: project,
	}
	t.Cleanup(func() { removeComposeProject(t, env.daemon, env.project) })
	return env
}

// up creates and starts the compose project document describes, named after the test.
func (e *stopEnv) up(t *testing.T, document string) *types.Project {
	t.Helper()

	project, err := composespec.NewCodec().Parse(e.ctx, fmt.Appendf(nil, document, e.project))
	require.NoError(t, err)
	require.NoError(t, e.compose.Create(e.ctx, project))
	require.NoError(t, e.compose.Start(e.ctx, project, 90*time.Second))
	return project
}
