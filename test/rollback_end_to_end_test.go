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
	"slices"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
)

// endToEndSpec is one compose project carrying everything a rollback has to rebuild: a named
// volume, another container's network namespace, the default bridge, a subpath mount, replicas
// with their own anonymous volumes, volumes_from and a disabled healthcheck. Two upstreams.
const endToEndSpec = `
name: %[1]s
x-sleep: &sleep
  command: ["sleep", "3600"]
  stop_signal: SIGKILL
services:
  db:
    <<: *sleep
    image: busybox
    volumes: [dbdata:/data]
  sidecar:
    <<: *sleep
    image: busybox
    network_mode: service:db
  legacy:
    <<: *sleep
    image: busybox
    network_mode: bridge
  app:
    <<: *sleep
    image: alpine:3.22
    volumes:
      - {type: volume, source: shared, target: /sub, volume: {subpath: app}}
  worker:
    <<: *sleep
    image: busybox
    deploy: {replicas: 2}
    volumes: [/cache]
  data:
    <<: *sleep
    image: busybox
    volumes: [/shared]
  reader:
    <<: *sleep
    image: busybox
    volumes_from: [data]
  quiet:
    <<: *sleep
    image: busybox
    healthcheck: {disable: true}
volumes:
  dbdata: {}
  shared: {name: %[1]s-shared, external: true}
`

// endToEndVolumes are the volume markers, by the container that writes and reads them.
var endToEndVolumes = map[string]string{
	"db-1":     "/data/marker",
	"app-1":    "/sub/marker",
	"worker-1": cacheMarker,
	"worker-2": cacheMarker,
	"data-1":   "/shared/marker",
}

func TestRollback_RebuildsEverythingTheSavepointRecorded(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.externalVolume(t, env.project+"-shared", "mkdir /data/app")
	env.upDocument(t, fmt.Sprintf(endToEndSpec, env.project))
	env.stampAll(t, "original")

	first, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp-1"})
	require.NoError(t, err)
	env.stampAll(t, "drifted")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &first.ID})
	require.NoError(t, err)
	second, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp-2"})
	require.NoError(t, err)

	// Assert
	containers := env.containers(t)
	require.Len(t, containers, 9)
	for name, c := range containers {
		assert.Equal(t, name+"-original", env.read(t, c.ID, layerMarker), "%s's writable layer", name)
		if path, ok := endToEndVolumes[name]; ok {
			assert.Equal(t, name+"-original", env.read(t, c.ID, path), "%s's volume", name)
		}
	}
	assert.Equal(t, "data-1-original", env.read(t, containers["reader-1"].ID, "/shared/marker"),
		"reader sees data's volume")

	assert.Equal(t, container.NetworkMode("container:"+containers["db-1"].ID),
		containers["sidecar-1"].Inspect.HostConfig.NetworkMode, "sidecar joins the new db")
	assert.Equal(t, container.NetworkMode("bridge"), containers["legacy-1"].Inspect.HostConfig.NetworkMode)

	require.Len(t, second.Images, 2)
	var refs []string
	for _, upstream := range second.Images {
		refs = append(refs, upstream.Refs...)
	}
	assert.Subset(t, refs, []string{"busybox", "alpine:3.22"})
	for _, ref := range refs {
		assert.False(t, model.IsLayerRef(ref), "%s is an upstream, not arcave's tag", ref)
	}
	assert.ElementsMatch(t, upstreamIDs(first), env.storedUpstreams(t), "each upstream is stored once")

	assert.Equal(t, env.spec(t, first.ID), env.spec(t, second.ID), "sp-2 records the project sp-1 did")

	for _, stat := range second.Stats {
		assert.NotContains(t, stat.Volume.ObjectID, "reader", "reader's mount is data's volume")
	}

	// reader is covered through data; a SIGKILL stop signal is no deadline kill; a layer
	// names its upstream by label.
	for _, sp := range []model.Savepoint{first, second} {
		require.NotNil(t, sp.NotCovered, sp.Name)
		assert.Equal(t, model.NotCovered{ServicesWithoutVolumes: []string{"legacy", "quiet", "sidecar"}},
			sortedNotCovered(*sp.NotCovered), sp.Name)
	}
}

// A compose project taken down comes back from the savepoint alone: its spec, its layers
// and its data, over the named volume `compose down` kept.
func TestRollback_RebuildsAComposeProjectThatWasTakenDown(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.externalVolume(t, env.project+"-shared", "mkdir /data/app")
	env.upDocument(t, fmt.Sprintf(endToEndSpec, env.project))
	env.stampAll(t, "original")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)
	env.stampAll(t, "drifted")
	env.down(t)

	// Act
	res, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, savepoint.ID, res.Savepoint.ID)
	containers := env.containers(t)
	require.Len(t, containers, 9)
	for name, c := range containers {
		assert.Equal(t, name+"-original", env.read(t, c.ID, layerMarker), "%s's writable layer", name)
		if path, ok := endToEndVolumes[name]; ok {
			assert.Equal(t, name+"-original", env.read(t, c.ID, path), "%s's volume", name)
		}
	}
	assert.Equal(t, container.NetworkMode("container:"+containers["db-1"].ID),
		containers["sidecar-1"].Inspect.HostConfig.NetworkMode, "sidecar joins the new db")

	// No safety snapshot is taken: there was nothing running to save.
	records, err := env.svc.ListSavepoints(env.ctx, model.ListRequest{IncludeSafetySnapshots: true})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, savepoint.ID, records[0].ID)
}

// down takes the compose project down as `docker compose down` does, keeping its named
// volumes; the anonymous ones it orphans are named first and dropped after.
func (e *imageRollbackEnv) down(t *testing.T) {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	require.NoError(t, e.compose.Down(e.ctx, &types.Project{Name: e.project}))

	for _, svc := range res.Project.Services {
		for _, v := range svc.Volumes {
			if v.Type == model.VolumeTypeAnon && v.VolumeName != "" && v.InheritedFrom == "" {
				assert.NoError(t, e.daemon.VolumeRemove(e.ctx, v.VolumeName, false), "remove volume %s", v.VolumeName)
			}
		}
	}
}

// sortedNotCovered orders what the daemon reports in no fixed order.
func sortedNotCovered(n model.NotCovered) model.NotCovered {
	slices.Sort(n.ServicesWithoutVolumes)
	return n
}

func TestRollback_RenumbersAroundAMissingFirstReplica(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(replicaGapSpec, env.project))
	for number, c := range env.workers(t) {
		env.stamp(t, c.ID, fmt.Sprintf("replica-%d", number))
	}
	require.NoError(t, env.daemon.ContainerRemove(env.ctx, env.workers(t)[1].ID,
		container.RemoveOptions{Force: true, RemoveVolumes: true}))

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "no-first"})
	require.NoError(t, err)
	for _, c := range env.workers(t) {
		env.stamp(t, c.ID, "drifted")
	}

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	rebuilt := env.workers(t)
	require.Len(t, rebuilt, 2)
	assert.NotContains(t, rebuilt, 1, "no replica the savepoint did not hold is created")
	for _, number := range []int{2, 3} {
		c, ok := rebuilt[number]
		require.True(t, ok, "replica %d is rebuilt under its recorded number", number)
		want := fmt.Sprintf("replica-%d", number)
		assert.Equal(t, want, env.read(t, c.ID, layerMarker), "replica %d's writable layer", number)
		assert.Equal(t, want, env.read(t, c.ID, cacheMarker), "replica %d's anonymous volume", number)
	}
}

// externalVolume creates a volume the compose project declares external, prepared by script.
// Its cleanup takes the project down first: cleanups run newest first, and the volume is in use.
func (e *imageRollbackEnv) externalVolume(t *testing.T, name, script string) {
	t.Helper()

	_, err := e.daemon.VolumeCreate(e.ctx, volume.CreateOptions{Name: name})
	require.NoError(t, err)
	busybox(t, dockerclient.NewEngine(e.daemon), name, script)
	t.Cleanup(func() {
		removeComposeProject(t, e.daemon, e.project)
		assert.NoError(t, e.daemon.VolumeRemove(context.Background(), name, true), "remove volume %s", name)
	})
}

// containers maps each container of the compose project to its <service>-<replica> name.
func (e *imageRollbackEnv) containers(t *testing.T) map[string]model.Container {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	containers := map[string]model.Container{}
	for _, svc := range res.Project.Services {
		for _, c := range svc.Containers {
			containers[fmt.Sprintf("%s-%d", svc.Name, c.Number)] = c
		}
	}
	return containers
}

// stampAll writes <name>-<content> into every container's writable layer and its own volume.
func (e *imageRollbackEnv) stampAll(t *testing.T, content string) {
	t.Helper()

	for name, c := range e.containers(t) {
		script := fmt.Sprintf("printf %%s %s-%s > %s", name, content, layerMarker)
		if path, ok := endToEndVolumes[name]; ok {
			script += fmt.Sprintf(" && printf %%s %s-%s > %s", name, content, path)
		}
		e.exec(t, c.ID, "sh", "-c", script)
	}
}

// storedUpstreams are the image ids shared/oci/images holds.
func (e *imageRollbackEnv) storedUpstreams(t *testing.T) []model.ImageDigestID {
	t.Helper()

	images, err := e.store.SubStorage("shared/oci/images/sha256", true)
	require.NoError(t, err)
	_, dirs, err := images.ListDir(e.ctx)
	require.NoError(t, err)
	ids := make([]model.ImageDigestID, 0, len(dirs))
	for _, dir := range dirs {
		ids = append(ids, model.ImageDigestID("sha256:"+strings.Trim(dir.Dirname(), "/")))
	}
	return ids
}

// spec is the compose spec a savepoint committed.
func (e *imageRollbackEnv) spec(t *testing.T, id model.SavepointID) string {
	t.Helper()

	reader, err := e.store.GetObject(e.ctx, string(id)+"/project.yaml")
	require.NoError(t, err)
	defer reader.Close()
	spec, err := io.ReadAll(reader)
	require.NoError(t, err)
	return string(spec)
}

func upstreamIDs(savepoint model.Savepoint) []model.ImageDigestID {
	ids := make([]model.ImageDigestID, 0, len(savepoint.Images))
	for _, upstream := range savepoint.Images {
		ids = append(ids, upstream.ID)
	}
	return ids
}
