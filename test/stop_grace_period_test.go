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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	killed, err := env.compose.Stop(env.ctx, project)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, killed)
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
	killed, err := env.compose.Stop(env.ctx, project)

	// Assert
	require.NoError(t, err, "a killed container is warned of, not failed on")
	inspect, err := env.daemon.ContainerInspect(env.ctx, env.project+"-db-1")
	require.NoError(t, err)
	assert.Equal(t, []string{inspect.ID}, killed)
	assert.Equal(t, 137, inspect.State.ExitCode)
	assert.Contains(t, env.logs.String(), fmt.Sprintf("container %s-db-1 did not exit within 1s and was killed", env.project))
}

// newStopEnv is a compose client whose configured grace period is 1s, logging into a buffer.
func newStopEnv(t *testing.T) *composeEnv {
	t.Helper()

	return newComposeEnv(t, "stop-grace", time.Second)
}
