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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
)

// capturePolicySpec labels out a bind, an anonymous volume and a whole service, and
// puts app's layer on the full-copy layout.
const capturePolicySpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.mounts.exclude: /src
      arcave.writable-layer.backend: fullcopy
    volumes:
      - %[2]s:/src
      - data:/data
      - type: volume
        target: /scratch
        volume:
          labels:
            arcave.capture: "false"
  cache:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.capture: "false"
    volumes:
      - cachedata:/cache
volumes:
  data: {}
  cachedata: {}
`

// absent is what readOrAbsent reads of a file that is not there.
const absent = "<absent>"

func TestRollback_CapturesWhatTheLabelsSay(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	src := t.TempDir()
	srcMarker := filepath.Join(src, "marker")
	env.upDocument(t, fmt.Sprintf(capturePolicySpec, env.project, src))
	stamp := func(content string) {
		require.NoError(t, os.WriteFile(srcMarker, []byte(content), 0o644))
		env.exec(t, env.container(t, "app"), "sh", "-c", fmt.Sprintf(
			"printf %%s %[1]s > %[2]s && printf %%s %[1]s > /data/marker && printf %%s %[1]s > /scratch/marker",
			content, layerMarker))
		env.exec(t, env.container(t, "cache"), "sh", "-c", fmt.Sprintf(
			"printf %%s %[1]s > %[2]s && printf %%s %[1]s > /cache/marker", content, layerMarker))
	}
	stamp("original")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "labelled"})
	require.NoError(t, err)

	require.Len(t, savepoint.Stats, 1, "only the data volume is captured")
	assert.Equal(t, "volume-named-"+env.project+"_data", savepoint.Stats[0].Volume.ObjectID)
	require.Len(t, savepoint.WritableLayers, 1, "only app's layer is captured")
	assert.Equal(t, "app", savepoint.WritableLayers[0].Service)
	assert.Equal(t, model.ImageBackendFullCopy, savepoint.WritableLayers[0].ImageBackend)
	require.NotNil(t, savepoint.NotCovered)
	assert.ElementsMatch(t, []model.SkippedVolume{
		{Volume: env.project + "_cachedata", RawType: "volume", Reason: model.SkipReasonExcluded,
			Detail: "arcave.capture=false on every service mounting it"},
		{Service: "app", Target: "/src", RawType: "bind", Reason: model.SkipReasonExcluded,
			Detail: "arcave.mounts.exclude=/src on service app"},
		{Service: "app", Replica: 1, Target: "/scratch", RawType: "volume", Reason: model.SkipReasonExcluded,
			Detail: "arcave.capture=false on the anonymous volume at /scratch of service app"},
	}, savepoint.NotCovered.Mounts)
	require.Len(t, savepoint.NotCovered.WritableLayers, 1)
	assert.Equal(t, "cache", savepoint.NotCovered.WritableLayers[0].Service)
	assert.Empty(t, savepoint.NotCovered.Images, "app keeps the busybox cache runs on")

	// The recorded spec stamps every decision, with where it came from.
	spec, err := sprepo.New(env.store).LoadSpec(env.ctx, savepoint.ID)
	require.NoError(t, err)
	for _, line := range []string{
		"io.arcave.capture.mount./src: false; origin=label; source=arcave.mounts.exclude=/src on service app",
		"io.arcave.capture.mount./data: true; backend=fullcopy; origin=config",
		"io.arcave.capture.writable-layer: true; backend=fullcopy; origin=config",
	} {
		assert.Contains(t, string(spec), line)
	}

	stamp("drifted")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	app, cache := env.container(t, "app"), env.container(t, "cache")
	assert.Equal(t, "original", env.readOrAbsent(t, app, layerMarker), "app's layer is restored")
	assert.Equal(t, "original", env.readOrAbsent(t, app, "/data/marker"), "the data volume is restored")
	assert.Equal(t, absent, env.readOrAbsent(t, app, "/scratch/marker"), "the excluded anonymous volume comes back empty")
	assert.Equal(t, "drifted", env.readOrAbsent(t, app, "/src/marker"), "the excluded bind is untouched")
	assert.Equal(t, absent, env.readOrAbsent(t, cache, layerMarker), "cache is created fresh from its upstream")
	assert.Equal(t, "drifted", env.readOrAbsent(t, cache, "/cache/marker"), "cache's volume is untouched")

	// The daemon says what the rollback put back: the stamps beside the user's labels.
	labels := env.labels(t, "app")
	assert.Equal(t, "/src", labels[model.LabelMountsExclude])
	assert.Equal(t, "false; origin=label; source=arcave.mounts.exclude=/src on service app",
		labels[model.MountStamp("/src")])
}

// labels are the labels of service's first replica, as the daemon holds them.
func (e *imageRollbackEnv) labels(t *testing.T, service string) map[string]string {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	for _, svc := range res.Project.Services {
		if svc.Name == service {
			require.NotEmpty(t, svc.Containers)
			return svc.Containers[0].Inspect.Config.Labels
		}
	}
	require.Failf(t, "no such service", "%s", service)
	return nil
}

// container is the first replica of service.
func (e *imageRollbackEnv) container(t *testing.T, service string) string {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	for _, svc := range res.Project.Services {
		if svc.Name == service {
			require.NotEmpty(t, svc.Containers)
			return svc.Containers[0].ID
		}
	}
	require.Failf(t, "no such service", "%s", service)
	return ""
}

// readOrAbsent reads a file inside a running container, absent when it is not there.
func (e *imageRollbackEnv) readOrAbsent(t *testing.T, containerID, path string) string {
	t.Helper()

	return e.exec(t, containerID, "sh", "-c", fmt.Sprintf("cat %s 2>/dev/null || printf %%s '%s'", path, absent))
}

// replicaGapNoLayerSpec is replicaGapSpec with the layer left out by label.
const replicaGapNoLayerSpec = `
name: %s
services:
  worker:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.writable-layer.capture: "false"
    deploy:
      replicas: 3
    volumes:
      - /cache
`

// With no layer to rebuild on, a gap in the numbering is still kept: the anonymous
// volumes are restored under the numbers the savepoint recorded them by.
func TestRollback_KeepsAGapWithoutALayer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(replicaGapNoLayerSpec, env.project))
	for number, c := range env.workers(t) {
		env.stamp(t, c.ID, fmt.Sprintf("replica-%d", number))
	}
	require.NoError(t, env.daemon.ContainerRemove(env.ctx, env.workers(t)[2].ID,
		container.RemoveOptions{Force: true, RemoveVolumes: true}))

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "gap-no-layer"})
	require.NoError(t, err)
	require.Empty(t, savepoint.WritableLayers)

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
		assert.Equal(t, fmt.Sprintf("replica-%d", number), env.read(t, c.ID, cacheMarker))
		assert.Equal(t, absent, env.readOrAbsent(t, c.ID, layerMarker), "no layer was held")
	}
}

// A volume a label leaves out now but the savepoint holds is restored, and the safety
// snapshot holds its live bytes first.
func TestRollback_SafetySnapshotKeepsWhatTheRollbackOverwrites(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.up(t, 1)
	env.write(t, env.replicas(t)[0], "original")
	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "before-the-label"})
	require.NoError(t, err)

	labelled := strings.Replace(fmt.Sprintf(imageRollbackSpec, env.project, 1),
		"volumes:\n  data: {}", "volumes:\n  data:\n    labels:\n      arcave.capture: \"false\"", 1)
	// A volume's labels are set at its creation, so the project is made again.
	removeComposeProject(t, env.daemon, env.project)
	env.upDocument(t, labelled)
	env.write(t, env.replicas(t)[0], "live")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "original", env.read(t, env.replicas(t)[0], volumeMarker))
}

// namedByServiceSpec leaves a named volume out through the service mounting it.
const namedByServiceSpec = `
name: %s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.mounts.exclude: /cache
    volumes:
      - data:/data
      - cache:/cache
volumes:
  data: {}
  cache: {}
`

// A named volume carries no label of its own — compose would recreate it to add one —
// and is left out by its service's target list, untouched by the rollback.
func TestRollback_ServiceLabelLeavesANamedVolumeOut(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(namedByServiceSpec, env.project))
	stamp := func(content string) {
		env.exec(t, env.container(t, "app"), "sh", "-c",
			fmt.Sprintf("printf %%s %[1]s > /data/marker && printf %%s %[1]s > /cache/marker", content))
	}
	stamp("original")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "no-cache"})
	require.NoError(t, err)
	require.Len(t, savepoint.Stats, 1)
	assert.Equal(t, "volume-named-"+env.project+"_data", savepoint.Stats[0].Volume.ObjectID)
	require.NotNil(t, savepoint.NotCovered)
	assert.Equal(t, []model.SkippedVolume{{
		Volume: env.project + "_cache", RawType: "volume", Reason: model.SkipReasonExcluded,
		Detail: "arcave.mounts.exclude=/cache on service app",
	}}, savepoint.NotCovered.Mounts)

	stamp("drifted")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	app := env.container(t, "app")
	assert.Equal(t, "original", env.readOrAbsent(t, app, "/data/marker"))
	assert.Equal(t, "drifted", env.readOrAbsent(t, app, "/cache/marker"), "the excluded volume is untouched")
}
