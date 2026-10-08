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

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// healthCheckDisabledSpec is a service whose healthcheck is disabled beside one that runs
// its own: compose's start --wait refuses the first, so Start waits for both itself.
const healthCheckDisabledSpec = `
name: %s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    volumes:
      - /cache
    healthcheck:
      disable: true
  checked:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    healthcheck:
      test: [CMD, "true"]
      interval: 1s
      timeout: 1s
      retries: 2
`

func TestRollback_StartsAServiceWithItsHealthcheckDisabled(t *testing.T) {
	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(healthCheckDisabledSpec, env.project))
	app := env.service(t, "app")
	env.stamp(t, app.ID, "original")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "health-disabled"})
	require.NoError(t, err)

	env.stamp(t, app.ID, "drifted")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	rebuilt := env.service(t, "app")
	assert.Equal(t, "original", env.read(t, rebuilt.ID, layerMarker), "the writable layer")
	assert.Equal(t, "original", env.read(t, rebuilt.ID, cacheMarker), "the anonymous volume")
	require.NotNil(t, rebuilt.Inspect.Config.Healthcheck)
	assert.Equal(t, []string{"NONE"}, rebuilt.Inspect.Config.Healthcheck.Test, "the healthcheck stays disabled")
	checked := env.service(t, "checked")
	require.NotNil(t, checked.Inspect.State.Health)
	assert.Equal(t, container.Healthy, checked.Inspect.State.Health.Status)
}

// service is the one replica of a service.
func (e *imageRollbackEnv) service(t *testing.T, name string) model.Container {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	for _, svc := range res.Project.Services {
		if svc.Name == name {
			require.Len(t, svc.Containers, 1)
			return svc.Containers[0]
		}
	}
	require.Failf(t, "service not found", "%s", name)
	return model.Container{}
}
