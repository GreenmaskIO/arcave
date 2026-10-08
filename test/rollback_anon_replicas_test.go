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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// anonReplicasSpec is one service on three replicas, each holding its own anonymous volume.
const anonReplicasSpec = `
name: %s
services:
  worker:
    image: busybox
    command: ["sh", "-c", "sleep 3600"]
    stop_signal: SIGKILL
    deploy:
      replicas: 3
    volumes:
      - /cache
`

func TestRollback_RestoresEveryReplicasAnonymousVolume(t *testing.T) {
	// Arrange
	env := newRecreateEnv(t)
	project, err := env.codec.Parse(env.ctx, fmt.Appendf(nil, anonReplicasSpec, env.project))
	require.NoError(t, err)
	env.up(t, project)
	for replica, volumeName := range env.cacheVolumes(t) {
		env.write(t, volumeName, "marker", fmt.Sprintf("replica %d", replica))
	}

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "per-replica"})
	require.NoError(t, err)
	var anonIDs []string
	for _, stat := range savepoint.Stats {
		if stat.Volume.Type == model.VolumeTypeAnon {
			anonIDs = append(anonIDs, stat.Volume.ObjectID)
		}
	}
	assert.ElementsMatch(t, []string{
		"volume-anon-worker-1-cache", "volume-anon-worker-2-cache", "volume-anon-worker-3-cache",
	}, anonIDs, "one unit per replica")

	for _, volumeName := range env.cacheVolumes(t) {
		env.write(t, volumeName, "marker", "corrupted")
	}

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	restored := env.cacheVolumes(t)
	require.Len(t, restored, 3)
	for replica, volumeName := range restored {
		assert.Equal(t, fmt.Sprintf("replica %d", replica), env.read(t, volumeName, "marker"), "replica %d", replica)
	}
}

// cacheVolumes maps each worker replica's number to the docker name of its anonymous volume.
func (e *recreateEnv) cacheVolumes(t *testing.T) map[int]string {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	require.Len(t, res.Project.Services, 1)
	volumes := map[int]string{}
	for _, c := range res.Project.Services[0].Containers {
		for _, mount := range c.Inspect.Mounts {
			if mount.Destination == "/cache" {
				volumes[c.Number] = mount.Name
			}
		}
	}
	require.Len(t, volumes, len(res.Project.Services[0].Containers), "every replica mounts /cache")
	return volumes
}
