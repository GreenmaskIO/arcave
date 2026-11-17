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
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// pausedPostgresSpec is postgres held by pause under a writer that never stops inserting; the
// writer itself is captured live.
const pausedPostgresSpec = `
name: %s
services:
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: arcave
    labels:
      arcave.capture.mode: pause
      arcave.image.capture: "false"
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]
      interval: 1s
      retries: 60
    volumes:
      - pgdata:/var/lib/postgresql/data
  writer:
    image: postgres:16
    environment:
      PGPASSWORD: arcave
    labels:
      arcave.capture.mode: live
      arcave.image.capture: "false"
      arcave.writable-layer.capture: "false"
    stop_signal: SIGKILL
    depends_on:
      db:
        condition: service_healthy
    command:
      - sh
      - -c
      - |
        until psql -h db -U postgres -qc 'create table if not exists t (id serial primary key, tag text not null)'; do sleep 0.2; done
        while true; do psql -h db -U postgres -qc "insert into t (tag) values ('load')" >/dev/null 2>&1; done
volumes:
  pgdata: {}
`

// A postgres held by pause is captured under load with no restart, and a rollback to it comes up
// from its crash-consistent copy: what was written before the savepoint is there, what came
// after is not, and every relation checks clean.
func TestSavepoint_PausedPostgresUnderLoad(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(pausedPostgresSpec, env.project))
	db := env.container(t, "db")
	psql := func(sql string) string {
		return strings.TrimSpace(env.exec(t, env.container(t, "db"), "psql", "-U", "postgres", "-tAc", sql))
	}
	require.Eventually(t, func() bool {
		out := env.exec(t, db, "sh", "-c", "psql -U postgres -tAc 'select count(*) from t' 2>/dev/null || echo 0")
		return strings.TrimSpace(out) != "0"
	}, time.Minute, 100*time.Millisecond, "the writer inserts")
	psql("insert into t (tag) values ('before')")
	entry := env.inspect(t, db)

	// Act
	savepoint, createErr := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "under-load"})
	held := env.inspect(t, db)
	psql("insert into t (tag) values ('after')")
	_, rollbackErr := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, createErr)
	assert.Equal(t, entry.State.StartedAt, held.State.StartedAt, "the capture did not restart postgres")
	assert.Equal(t, entry.RestartCount, held.RestartCount)
	assert.True(t, held.State.Running)
	assert.False(t, held.State.Paused, "the capture resumed what it froze")
	require.NoError(t, rollbackErr)
	assert.Equal(t, "1", psql("select count(*) from t where tag = 'before'"))
	assert.Equal(t, "0", psql("select count(*) from t where tag = 'after'"))
	env.exec(t, env.container(t, "db"), "pg_amcheck", "-U", "postgres", "--install-missing", "--heapallindexed", "postgres")
}

// convergeSpec holds one service by each mode.
const convergeSpec = `
name: %s
services:
  stopped:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    volumes:
      - data:/data
  paused:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.capture.mode: pause
  live:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.capture.mode: live
volumes:
  data: {}
`

// convergedStates are the containers of convergeSpec, every one running.
var convergedStates = map[string]string{"stopped-1": "running", "paused-1": "running", "live-1": "running"}

// A compose project the user froze whole comes out of a savepoint running, every mode alike.
func TestSavepoint_ConvergesFromPausedProject(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(convergeSpec, env.project))
	env.pauseAll(t)

	// Act
	_, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "frozen"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, convergedStates, env.states(t))
}

// A rollback of a compose project the user froze whole resumes it before its stop and leaves it
// running.
func TestRollback_ConvergesFromPausedProject(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(convergeSpec, env.project))
	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "frozen"})
	require.NoError(t, err)
	env.pauseAll(t)

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, convergedStates, env.states(t))
}

// healthPausedSpec is a paused service whose healthcheck fails fast.
const healthPausedSpec = `
name: %s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.capture.mode: pause
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
    volumes:
      - data:/data
volumes:
  data: {}
`

// A service frozen past its healthcheck's interval × retries reads unhealthy until its next
// probe once resumed; the wait after a capture outlasts that rather than failing on it.
func TestSavepoint_ResumedServiceOutlivesItsStaleHealth(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(healthPausedSpec, env.project))
	app := env.container(t, "app")
	env.pauseAll(t)
	require.Eventually(t, func() bool {
		health := env.inspect(t, app).State.Health
		return health != nil && health.Status == container.Unhealthy
	}, time.Minute, 100*time.Millisecond, "a frozen container reads unhealthy")

	// Act
	_, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "stale-health"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"app-1": "running"}, env.states(t))
}

// inspect is the daemon's view of one container.
func (e *imageRollbackEnv) inspect(t *testing.T, containerID string) container.InspectResponse {
	t.Helper()

	res, err := e.daemon.ContainerInspect(e.ctx, containerID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	return res.Container
}

// pauseAll freezes every container of the compose project, as `docker compose pause` does.
func (e *imageRollbackEnv) pauseAll(t *testing.T) {
	t.Helper()

	var ids []string
	for _, c := range e.containers(t) {
		ids = append(ids, c.ID)
	}
	require.NoError(t, e.engine().PauseContainers(e.ctx, ids))
}

// states maps each container of the compose project to its state, by <service>-<replica>.
func (e *imageRollbackEnv) states(t *testing.T) map[string]string {
	t.Helper()

	states := map[string]string{}
	for name, c := range e.containers(t) {
		states[name] = string(e.inspect(t, c.ID).State.Status)
	}
	return states
}
