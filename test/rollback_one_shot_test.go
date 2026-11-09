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

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// oneShotSpec is a migrate no service waits on, beside a long-running app: compose's
// start --wait refuses its exit 0, so Start waits for both itself.
const oneShotSpec = `
name: %s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    volumes:
      - /cache
  migrate:
    image: busybox
    command: ["true"]
`

func TestRollback_StartsAOneShotNobodyAwaits(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(oneShotSpec, env.project))
	app := env.service(t, "app")
	env.stamp(t, app.ID, "original")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "one-shot"})
	require.NoError(t, err)

	env.stamp(t, app.ID, "drifted")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	rebuilt := env.service(t, "app")
	assert.Equal(t, "original", env.read(t, rebuilt.ID, layerMarker), "the writable layer")
	assert.Equal(t, "original", env.read(t, rebuilt.ID, cacheMarker), "the anonymous volume")
	migrate := env.service(t, "migrate")
	assert.Equal(t, container.StateExited, migrate.Inspect.State.Status)
	assert.Equal(t, 0, migrate.Inspect.State.ExitCode)
}

