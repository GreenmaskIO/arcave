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

// cacheMarker sits in each replica's own anonymous volume.
const cacheMarker = "/cache/marker"

// replicaGapSpec is one service on three replicas, each with its own writable layer
// and its own anonymous volume.
const replicaGapSpec = `
name: %s
services:
  worker:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    deploy:
      replicas: 3
    volumes:
      - /cache
`

func TestRollback_KeepsAGapInReplicaNumbering(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(replicaGapSpec, env.project))
	for number, c := range env.workers(t) {
		env.stamp(t, c.ID, fmt.Sprintf("replica-%d", number))
	}
	// Replica 2 goes by hand, as one that crashed and was cleaned up would.
	require.NoError(t, env.daemon.ContainerRemove(env.ctx, env.workers(t)[2].ID,
		container.RemoveOptions{Force: true, RemoveVolumes: true}))

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "with-a-gap"})
	require.NoError(t, err)
	require.Len(t, savepoint.WritableLayers, 2)

	for _, c := range env.workers(t) {
		env.stamp(t, c.ID, "drifted")
	}

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	rebuilt := env.workers(t)
	require.Len(t, rebuilt, 2)
	for _, number := range []int{1, 3} {
		c, ok := rebuilt[number]
		require.True(t, ok, "replica %d is rebuilt under its recorded number", number)
		assert.Equal(t, fmt.Sprintf("%s-worker-%d", env.project, number), c.Name)
		want := fmt.Sprintf("replica-%d", number)
		assert.Equal(t, want, env.read(t, c.ID, layerMarker), "replica %d's writable layer", number)
		assert.Equal(t, want, env.read(t, c.ID, cacheMarker), "replica %d's anonymous volume", number)
	}
	assert.NotContains(t, rebuilt, 2, "no replica the savepoint did not hold is created")
}

// workers maps each worker replica's number to its container.
func (e *imageRollbackEnv) workers(t *testing.T) map[int]model.Container {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	require.Len(t, res.Project.Services, 1)
	workers := map[int]model.Container{}
	for _, c := range res.Project.Services[0].Containers {
		workers[c.Number] = c
	}
	return workers
}

// stamp writes content past every mount and into the replica's anonymous volume.
func (e *imageRollbackEnv) stamp(t *testing.T, containerID, content string) {
	t.Helper()

	e.exec(t, containerID, "sh", "-c",
		fmt.Sprintf("printf %%s %s > %s && printf %%s %s > %s", content, layerMarker, content, cacheMarker))
}
