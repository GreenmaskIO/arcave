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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/composespec"
)

// upDownSpec mounts every kind of volume an up or a down meets: a named one the compose project
// owns, an anonymous one, an external one and a bind.
const upDownSpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    volumes:
      - data:/data
      - /cache
      - shared:/shared
      - %[2]s:/host
volumes:
  data: {}
  shared: {name: %[1]s-shared, external: true}
`

// The environment's cycle through the built binary: up refuses what the daemon still holds and
// --force drops it, a volume the savepoint does not hold included; down takes the session's own
// environment off whole, leaving the external volume and the bind; up brings a savepoint back.
// Another session's environment, and compose's own, refuse a down that --force takes all the same.
func TestUpDown_TheEnvironmentCycle(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	shared := env.project + "-shared"
	data := env.project + "_data"
	extra := env.project + "_extra"
	hostDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "marker"), []byte("host"), 0o600))
	env.externalVolume(t, shared, "printf kept > /data/marker")
	document := fmt.Sprintf(upDownSpec, env.project, hostDir)
	env.upDocument(t, document)
	env.write(t, env.replicas(t)[0], "base")
	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", "arcave-up-down")
	writeStorageConfig(t, projectRoot, config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: t.TempDir()},
	}, config.SnapshotBackendFullCopy)
	run := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	refuse := func(args ...string) string {
		t.Helper()
		return runArcaveRefused(t, projectRoot, append([]string{"-p", env.project}, args...)...)
	}
	run("savepoint", "create", "--name", "base")

	// Act, Assert: what compose's own down leaves refuses an up; --force drops it, the volume the
	// savepoint does not hold too.
	parsed, err := composespec.NewCodec().Parse(env.ctx, []byte(document))
	require.NoError(t, err)
	anonymous := env.anonymousVolumes(t)
	require.NoError(t, env.compose.Down(env.ctx, parsed))
	// compose's own down orphans the anonymous volume, which carries no label of the project.
	for _, name := range anonymous {
		_, err := env.daemon.VolumeRemove(env.ctx, name, client.VolumeRemoveOptions{})
		require.NoError(t, err)
	}
	_, err = env.daemon.VolumeCreate(env.ctx, client.VolumeCreateOptions{
		Name: extra, Labels: map[string]string{api.ProjectLabel: env.project},
	})
	require.NoError(t, err)
	assert.Contains(t, refuse("up"), data)
	forced := decode[model.UpResult](t, run("up", "--force"))
	require.NotNil(t, forced.Discarded)
	assert.ElementsMatch(t, []string{data, extra}, forced.Discarded.Volumes)
	assert.Equal(t, "base", env.read(t, env.replicas(t)[0], volumeMarker))
	_, err = env.daemon.VolumeInspect(env.ctx, extra, client.VolumeInspectOptions{})
	assert.Error(t, err, "the volume the savepoint does not hold is gone")

	// Act, Assert: a running environment refuses an up, untouched.
	replicas := env.replicas(t)
	env.write(t, replicas[0], "x")
	assert.Contains(t, refuse("up"), "the daemon holds the compose project already")
	assert.Equal(t, replicas, env.replicas(t))

	// Act, Assert: down takes the session's own environment off whole.
	run("savepoint", "create", "--name", "x")
	down := decode[model.DownResult](t, run("down"))
	require.NotNil(t, down.Deployment)
	assert.Equal(t, model.DefaultSession, down.Deployment.Session.ID)
	assert.False(t, down.Forced)
	assert.NotEmpty(t, down.RemovedTags, "the restored layer's tag")
	env.requireGone(t)
	assert.Empty(t, env.layerTags(t))
	_, err = env.daemon.VolumeInspect(env.ctx, shared, client.VolumeInspectOptions{})
	assert.NoError(t, err, "the external volume stays")
	assert.FileExists(t, filepath.Join(hostDir, "marker"), "the bind stays")

	// Act, Assert: up brings the savepoint back.
	run("up", "x")
	assert.Equal(t, "x", env.read(t, env.replicas(t)[0], volumeMarker))
	assert.Equal(t, "x", env.read(t, env.replicas(t)[0], layerMarker))

	// Act, Assert: another session's environment refuses the down; --force takes it.
	runArcave(t, projectRoot, nil, "session", "create", "--name", "feat", "--from", model.DefaultSessionName)
	run("--session", "feat", "rollback", "base")
	assert.Contains(t, refuse("down"), "--session feat")
	assert.True(t, decode[model.DownResult](t, run("down", "--force")).Forced)
	env.requireGone(t)

	// Act, Assert: compose's own environment refuses the down; --force takes it.
	env.upDocument(t, document)
	assert.Contains(t, refuse("down"), "no deployment labels on app-1")
	run("down", "--force")
	env.requireGone(t)

	// Act, Assert: a second down finds nothing to do.
	again := decode[model.DownResult](t, run("down"))
	assert.Empty(t, again.Discarded.Containers)
	assert.Empty(t, again.Discarded.Volumes)
}

// compose's down by name alone takes a project down in its dependency order, and one with no
// container left still loses its volumes and networks.
func TestUpDown_DownWithVolumesByName(t *testing.T) {
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
	env.requireGone(t)
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
	env.requireGone(t)
}

// A volume of the compose project something outside it holds survives compose's down, which only
// warns. A forced up refuses to build over it, a down refuses to call it gone; once it is free,
// a forced up creates it anew, its old data gone.
func TestUpDown_ABusyVolumeIsNeverReused(t *testing.T) {
	t.Parallel()

	// Arrange: data is left out of the savepoint, so only a fresh volume can come back empty.
	env := newImageRollbackEnv(t)
	env.upDocument(t, fmt.Sprintf(`
name: %s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    labels:
      arcave.mounts.exclude: /data
    volumes: ["data:/data", "state:/state"]
volumes:
  data: {}
  state: {}
`, env.project))
	data := env.project + "_data"
	env.write(t, env.replicas(t)[0], "base")
	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", "arcave-up-down-busy")
	writeStorageConfig(t, projectRoot, config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: t.TempDir()},
	}, config.SnapshotBackendFullCopy)
	run := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	refuse := func(args ...string) string {
		t.Helper()
		return runArcaveRefused(t, projectRoot, append([]string{"-p", env.project}, args...)...)
	}
	run("savepoint", "create", "--name", "base")
	env.write(t, env.replicas(t)[0], "stale")
	release := env.holdVolume(t, data)

	// Act, Assert: the forced up refuses before it builds anything.
	assert.Contains(t, refuse("up", "--force", "base"), data)
	resources, err := env.inspector.ReadResources(env.ctx)
	require.NoError(t, err)
	assert.Empty(t, resources.Containers, "nothing was built over the busy volume")
	assert.Equal(t, []string{data}, resources.Volumes)

	// Act, Assert: once it is free, the forced up comes up on an empty one.
	release()
	run("up", "--force", "base")
	assert.Equal(t, absent, env.readOrAbsent(t, env.replicas(t)[0], volumeMarker), "the old data is gone")
	assert.Equal(t, "base", env.read(t, env.replicas(t)[0], layerMarker))

	// Act, Assert: the down refuses to call a busy volume gone.
	release = env.holdVolume(t, data)
	assert.Contains(t, refuse("down"), "the daemon still holds the compose project after its drop")
	release()
	run("down", "--force")
	env.requireGone(t)
}

// compose's down keys the volumes it removes by their logical name, so of two that share one, or
// both have none, it removes one; a down removes both all the same.
func TestUpDown_VolumesSharingALogicalName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels map[string]string
	}{
		{name: "no logical name"},
		{name: "the same logical name", labels: map[string]string{api.VolumeLabel: "data"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			env := newImageRollbackEnv(t)
			for _, suffix := range []string{"_extra_a", "_extra_b"} {
				labels := map[string]string{api.ProjectLabel: env.project}
				maps.Copy(labels, tt.labels)
				_, err := env.daemon.VolumeCreate(env.ctx, client.VolumeCreateOptions{Name: env.project + suffix, Labels: labels})
				require.NoError(t, err)
			}
			projectRoot := t.TempDir()
			runArcave(t, projectRoot, nil, "init", "--name", "arcave-up-down-volumes")
			writeStorageConfig(t, projectRoot, config.Storage{
				Type:      config.StorageTypeDirectory,
				Directory: &config.DirectoryStorage{Path: t.TempDir()},
			}, config.SnapshotBackendFullCopy)

			// Act
			runArcave(t, projectRoot, nil, "-p", env.project, "down", "--force")

			// Assert
			env.requireGone(t)
		})
	}
}

// A container compose runs beside the replicas — a one-off, a hook's runner left behind — is no
// replica, but it is the compose project's: a forced down removes one left alone, with the network
// it held and the anonymous volume it mounted, which compose's own down would orphan for a finished
// one-off.
func TestUpDown_ContainersBesideTheReplicas(t *testing.T) {
	t.Parallel()

	oneoff := map[string]string{api.OneoffLabel: "True"}
	tests := []struct {
		name     string
		labels   map[string]string
		network  bool // on a network of the compose project rather than none
		cache    bool // with an anonymous volume at /cache
		finished bool // stopped before the down
	}{
		{name: "a one-off", labels: oneoff},
		{name: "a hook's runner", labels: map[string]string{api.HookLabel: "pre_start", api.HookIndexLabel: "0"}},
		{name: "a one-off on the compose project's network", labels: oneoff, network: true},
		{name: "a finished one-off with an anonymous volume", labels: oneoff, cache: true, finished: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			env := newImageRollbackEnv(t)
			networkMode := container.NetworkMode("none")
			if tt.network {
				networkMode = container.NetworkMode(env.createProjectNetwork(t))
			}
			labels := map[string]string{api.ProjectLabel: env.project, api.ServiceLabel: "app", api.ConfigHashLabel: "x"}
			maps.Copy(labels, tt.labels)
			containerConfig := &container.Config{Image: busyboxImage, Cmd: []string{"sleep", "3600"}, Labels: labels}
			if tt.cache {
				containerConfig.Volumes = map[string]struct{}{"/cache": {}}
			}
			created, err := env.daemon.ContainerCreate(env.ctx, client.ContainerCreateOptions{
				Name:       env.project + "-app-run",
				Config:     containerConfig,
				HostConfig: &container.HostConfig{NetworkMode: networkMode},
			})
			require.NoError(t, err)
			_, err = env.daemon.ContainerStart(env.ctx, created.ID, client.ContainerStartOptions{})
			require.NoError(t, err)
			if tt.finished {
				zero := 0
				_, err = env.daemon.ContainerStop(env.ctx, created.ID, client.ContainerStopOptions{Timeout: &zero})
				require.NoError(t, err)
			}
			anonymous := env.mountedVolumes(t, created.ID)
			if tt.cache {
				require.Len(t, anonymous, 1, "the anonymous volume to look for")
			}
			projectRoot := initDownProject(t)

			// Act, Assert: no replica names whose it is.
			assert.Contains(t, runArcaveRefused(t, projectRoot, "-p", env.project, "down"), env.project+"-app-run")
			down := decode[model.DownResult](t, runArcave(t, projectRoot, nil, "-p", env.project, "down", "--force"))

			// Assert
			assert.Equal(t, []string{env.project + "-app-run"}, down.Discarded.Containers)
			assert.Empty(t, down.Notices, "nothing was left to explain")
			env.requireGone(t)
			for _, name := range anonymous {
				_, err := env.daemon.VolumeInspect(env.ctx, name, client.VolumeInspectOptions{})
				assert.True(t, cerrdefs.IsNotFound(err), "the anonymous volume %s went with its container: %v", name, err)
			}
		})
	}
}

// A network of the compose project that a container outside it uses survives the down, which
// says so with the daemon's own reason; whatever else the down removes goes.
func TestUpDown_ANetworkInUseOutsideStays(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	network := env.createProjectNetwork(t)
	user, err := env.daemon.ContainerCreate(env.ctx, client.ContainerCreateOptions{
		Name:       env.project + "-outside-" + randomSuffix(t),
		Config:     &container.Config{Image: busyboxImage, Cmd: []string{"sleep", "3600"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := env.daemon.ContainerRemove(context.Background(), user.ID, client.ContainerRemoveOptions{Force: true})
		assert.NoError(t, err)
	})
	_, err = env.daemon.ContainerStart(env.ctx, user.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
	projectRoot := initDownProject(t)

	// Act
	down := decode[model.DownResult](t, runArcave(t, projectRoot, nil, "-p", env.project, "down", "--force"))

	// Assert
	require.Len(t, down.Notices, 1)
	assert.Contains(t, down.Notices[0].Message, network)
	assert.Contains(t, down.Notices[0].Message, "active endpoints", "the daemon's reason, not a guess")
	resources, err := env.inspector.ReadResources(env.ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{network}, resources.Networks)
}

// initDownProject is an arcave project over a directory store, for a down of a compose project
// arcave did not build.
func initDownProject(t *testing.T) string {
	t.Helper()

	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", "arcave-up-down-beside")
	writeStorageConfig(t, projectRoot, config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: t.TempDir()},
	}, config.SnapshotBackendFullCopy)
	return projectRoot
}

// runArcaveRefused runs the binary expecting it to fail, and returns what it wrote to stderr.
func runArcaveRefused(t *testing.T, projectRoot string, args ...string) string {
	t.Helper()

	cmd := arcaveCommandIn(t, projectRoot, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.Error(t, err, "arcave %v succeeded: %s", args, out)
	return stderr.String()
}

// requireGone checks the daemon holds nothing of the compose project by its label: no container,
// network or volume.
func (e *imageRollbackEnv) requireGone(t *testing.T) {
	t.Helper()

	byProject := make(client.Filters).Add("label", api.ProjectLabel+"="+e.project)
	containers, err := e.daemon.ContainerList(e.ctx, client.ContainerListOptions{All: true, Filters: byProject})
	require.NoError(t, err)
	assert.Empty(t, containers.Items, "containers")
	networks, err := e.daemon.NetworkList(e.ctx, client.NetworkListOptions{Filters: byProject})
	require.NoError(t, err)
	assert.Empty(t, networks.Items, "networks")
	volumes, err := e.daemon.VolumeList(e.ctx, client.VolumeListOptions{Filters: byProject})
	require.NoError(t, err)
	assert.Empty(t, volumes.Items, "volumes")
}

// layerTags are the restored layer tags of the compose project the daemon holds.
func (e *imageRollbackEnv) layerTags(t *testing.T) []string {
	t.Helper()

	images, err := e.daemon.ImageList(e.ctx, client.ImageListOptions{
		Filters: make(client.Filters).Add("reference", "arcave/"+e.project+"-*"),
	})
	require.NoError(t, err)
	var tags []string
	for _, image := range images.Items {
		tags = append(tags, image.RepoTags...)
	}
	return tags
}

// anonymousVolumes are the docker names of the compose project's anonymous volumes, read off its
// containers' mounts: no label of the project marks them.
func (e *imageRollbackEnv) anonymousVolumes(t *testing.T) []string {
	t.Helper()

	introspection, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	var names []string
	for _, service := range introspection.Project.Services {
		for _, mount := range service.Volumes {
			if mount.Type == model.VolumeTypeAnon && mount.VolumeName != "" {
				names = append(names, mount.VolumeName)
			}
		}
	}
	return names
}

// holdVolume mounts volume into a stopped container outside the compose project, so the daemon
// refuses to remove it; the returned func removes that container, at most once.
func (e *imageRollbackEnv) holdVolume(t *testing.T, volume string) func() {
	t.Helper()

	created, err := e.daemon.ContainerCreate(e.ctx, client.ContainerCreateOptions{
		Name:       e.project + "-holder-" + randomSuffix(t),
		Config:     &container.Config{Image: busyboxImage, Cmd: []string{"true"}},
		HostConfig: &container.HostConfig{NetworkMode: "none", Binds: []string{volume + ":/data"}},
	})
	require.NoError(t, err)
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, err := e.daemon.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
			assert.NoError(t, err, "remove the holder of %s", volume)
		})
	}
	t.Cleanup(release)
	return release
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

// createProjectNetwork creates the compose project's default network as compose labels it, and
// returns its name.
func (e *imageRollbackEnv) createProjectNetwork(t *testing.T) string {
	t.Helper()

	name := e.project + "_default"
	_, err := e.daemon.NetworkCreate(e.ctx, name, client.NetworkCreateOptions{
		Labels: map[string]string{api.ProjectLabel: e.project, api.NetworkLabel: "default"},
	})
	require.NoError(t, err)
	return name
}

// mountedVolumes are the names of the volumes a container mounts.
func (e *imageRollbackEnv) mountedVolumes(t *testing.T, containerID string) []string {
	t.Helper()

	inspected, err := e.daemon.ContainerInspect(e.ctx, containerID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	var names []string
	for _, mount := range inspected.Container.Mounts {
		if mount.Name != "" {
			names = append(names, mount.Name)
			// A failed run must not leak it.
			t.Cleanup(func() {
				_, _ = e.daemon.VolumeRemove(context.Background(), mount.Name, client.VolumeRemoveOptions{Force: true})
			})
		}
	}
	return names
}
