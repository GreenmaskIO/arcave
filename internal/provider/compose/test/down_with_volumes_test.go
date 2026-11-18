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
	"errors"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

// compose's down by name alone takes a project down in its dependency order, and one with no
// container left still loses its volumes and networks.
func TestComposeClient_DownWithVolumes(t *testing.T) {
	t.Parallel()

	// Arrange: web waits on db, so a down that ignored the order would fail on the network.
	env := newImageRollbackEnv(t)
	document := fmt.Sprintf(`
name: %s
services:
  db:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    volumes: ["data:/data"]
  web:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    depends_on: [db]
volumes:
  data: {}
`, env.project)
	env.upDocument(t, document)
	since := time.Now()

	// Act
	err := env.compose.DownWithVolumes(env.ctx, env.project)

	// Assert
	require.NoError(t, err)
	daemontest.RequireComposeProjectGone(t, env.daemon, env.project)
	assert.Equal(t, []string{"web", "db"}, env.destroyedServices(t, since), "in reverse dependency order")

	// Arrange: the containers gone, the volume and the network left.
	env.upDocument(t, document)
	byProject := make(client.Filters).Add("label", api.ProjectLabel+"="+env.project)
	containers, err := env.daemon.ContainerList(env.ctx, client.ContainerListOptions{All: true, Filters: byProject})
	require.NoError(t, err)
	for _, c := range containers.Items {
		_, err := env.daemon.ContainerRemove(env.ctx, c.ID, client.ContainerRemoveOptions{Force: true})
		require.NoError(t, err)
	}

	// Act
	err = env.compose.DownWithVolumes(env.ctx, env.project)

	// Assert
	require.NoError(t, err)
	daemontest.RequireComposeProjectGone(t, env.daemon, env.project)
}

// destroyedServices are the services of the compose project whose containers the daemon destroyed
// since then, in the order it did.
func (e *imageRollbackEnv) destroyedServices(t *testing.T, since time.Time) []string {
	t.Helper()

	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	stream := e.daemon.Events(ctx, client.EventsListOptions{
		Since: strconv.FormatInt(since.Unix(), 10),
		Until: strconv.FormatInt(time.Now().Add(time.Second).Unix(), 10),
		Filters: make(client.Filters).Add("type", "container").Add("event", string(events.ActionDestroy)).
			Add("label", api.ProjectLabel+"="+e.project),
	})
	var services []string
	for {
		select {
		case msg, ok := <-stream.Messages:
			if !ok {
				return services
			}
			services = append(services, msg.Actor.Attributes[api.ServiceLabel])
		case err := <-stream.Err:
			if errors.Is(err, io.EOF) {
				return services
			}
			require.NoError(t, err)
			return services
		}
	}
}
