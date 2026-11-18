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
	"fmt"
	"io"
	"slices"
	"strconv"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

const (
	// busyboxDigest pins fidelityBusybox, so compose's image label is one arcave can compare.
	busyboxDigest   = "@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	fidelityBusybox = "busybox:1.37" + busyboxDigest
)

// minComposeWaitVersion is the oldest `docker compose` whose up waits on the services with a
// deadline: --wait-timeout.
const minComposeWaitVersion = "2.17.0"

// composeProject is one compose project the user's own `docker compose` runs, read back through
// the daemon's API.
type composeProject struct {
	ctx    context.Context
	daemon *client.Client
	// project is the compose project's name; dir holds its compose.yaml.
	project string
	dir     string
}

// newComposeProject names a compose project after kind, removed with the test; it skips without a
// daemon or a `docker compose` that waits.
func newComposeProject(t *testing.T, kind string) *composeProject {
	t.Helper()

	daemon := daemontest.NewClient(t)
	requireComposeCLI(t, minComposeWaitVersion)
	project := "arcave-" + kind + "-" + daemontest.RandomSuffix(t)
	t.Cleanup(func() { daemontest.RemoveComposeProject(t, daemon, project) })
	return &composeProject{
		ctx:     context.Background(),
		daemon:  daemon,
		project: project,
		dir:     t.TempDir(),
	}
}

// up runs daemontest.ImageRollbackSpec with replicas of its one service.
func (e *composeProject) up(t *testing.T, replicas int) {
	t.Helper()

	e.upDocument(t, fmt.Sprintf(daemontest.ImageRollbackSpec, e.project, replicas))
}

// upDocument writes document as the compose project's compose.yaml and brings it up, waiting on
// its services.
func (e *composeProject) upDocument(t *testing.T, document string) {
	t.Helper()

	writeComposeFile(t, e.dir, document)
	e.compose(t, "up", "--detach", "--wait", "--wait-timeout", "90")
}

// compose runs `docker compose args...` on the compose project.
func (e *composeProject) compose(t *testing.T, args ...string) {
	t.Helper()

	runComposeCLI(t, e.dir, e.project, args...)
}

// down takes the compose project down as the user's own down does, keeping its named volumes.
func (e *composeProject) down(t *testing.T) {
	t.Helper()

	e.compose(t, "down")
}

// replicas are the containers of the compose project's one service, sorted by replica number.
func (e *composeProject) replicas(t *testing.T) []string {
	t.Helper()

	byService := e.listByService(t)
	require.Len(t, byService, 1)
	var ids []string
	for _, service := range byService {
		for _, c := range service {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// containers inspects every container of the compose project, by <service>-<replica>.
func (e *composeProject) containers(t *testing.T) map[string]container.InspectResponse {
	t.Helper()

	containers := map[string]container.InspectResponse{}
	for service, replicas := range e.listByService(t) {
		for _, c := range replicas {
			inspected, err := e.daemon.ContainerInspect(e.ctx, c.ID, client.ContainerInspectOptions{})
			require.NoError(t, err)
			containers[service+"-"+c.Labels[api.ContainerNumberLabel]] = inspected.Container
		}
	}
	return containers
}

// listByService lists the compose project's containers by the service compose labelled them with,
// each service's sorted by replica number.
func (e *composeProject) listByService(t *testing.T) map[string][]container.Summary {
	t.Helper()

	listed, err := e.daemon.ContainerList(e.ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+e.project),
	})
	require.NoError(t, err)
	byService := map[string][]container.Summary{}
	for _, c := range listed.Items {
		service := c.Labels[api.ServiceLabel]
		byService[service] = append(byService[service], c)
	}
	for _, replicas := range byService {
		slices.SortFunc(replicas, func(a, b container.Summary) int {
			return replicaNumber(t, a) - replicaNumber(t, b)
		})
	}
	return byService
}

// write stamps content into both markers of one replica, past its mounts and into
// the volume, so one call drifts everything a rollback has to put back.
func (e *composeProject) write(t *testing.T, containerID, content string) {
	t.Helper()

	daemontest.WriteMarkers(t, e.daemon, containerID, content)
}

func (e *composeProject) read(t *testing.T, containerID, path string) string {
	t.Helper()

	return e.exec(t, containerID, "cat", path)
}

// exec runs one command inside a running container and returns its output.
func (e *composeProject) exec(t *testing.T, containerID string, cmd ...string) string {
	t.Helper()

	return daemontest.Exec(t, e.daemon, containerID, cmd...)
}

// externalVolume creates a volume outside the compose project, filled by script with the volume at
// /data, removed with the test after the compose project.
func (e *composeProject) externalVolume(t *testing.T, name, script string) {
	t.Helper()

	_, err := e.daemon.VolumeCreate(e.ctx, client.VolumeCreateOptions{Name: name})
	require.NoError(t, err)
	busybox(t, e.daemon, name, script)
	t.Cleanup(func() {
		daemontest.RemoveComposeProject(t, e.daemon, e.project)
		_, err := e.daemon.VolumeRemove(context.Background(), name, client.VolumeRemoveOptions{Force: true})
		assert.NoError(t, err, "remove volume %s", name)
	})
}

// container is the first replica of service.
func (e *composeProject) container(t *testing.T, service string) string {
	t.Helper()

	replicas := e.listByService(t)[service]
	require.NotEmpty(t, replicas, "no replica of service %s", service)
	return replicas[0].ID
}

// inspect is what the daemon reports of one container.
func (e *composeProject) inspect(t *testing.T, containerID string) container.InspectResponse {
	t.Helper()

	res, err := e.daemon.ContainerInspect(e.ctx, containerID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	return res.Container
}

// replicaNumber is the replica compose numbered c.
func replicaNumber(t *testing.T, c container.Summary) int {
	t.Helper()

	number, err := strconv.Atoi(c.Labels[api.ContainerNumberLabel])
	require.NoError(t, err, "container %s", c.ID)
	return number
}

// busybox runs script in a throwaway busybox with vol at /data and returns its stdout; a script
// that fails fails the test.
func busybox(t *testing.T, daemon *client.Client, vol, script string) string {
	t.Helper()

	ctx := context.Background()
	pullMissing(t, daemon, daemontest.BusyboxImage)
	created, err := daemon.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: daemontest.BusyboxImage, Cmd: []string{"sh", "-c", script}},
		HostConfig: &container.HostConfig{Binds: []string{vol + ":/data"}},
	})
	require.NoError(t, err)
	// Removed at once, not with the test: an exited container still holds vol.
	defer func() {
		_, _ = daemon.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	_, err = daemon.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
	waited := daemon.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var exit int64
	select {
	case res := <-waited.Result:
		exit = res.StatusCode
	case err := <-waited.Error:
		require.NoError(t, err)
	}
	logs, err := daemon.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	require.NoError(t, err)
	defer func() { _ = logs.Close() }()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, logs)
	require.NoError(t, err)
	require.Zerof(t, exit, "busybox %q failed:\nstdout:\n%s\nstderr:\n%s", script, stdout.String(), stderr.String())
	return stdout.String()
}

// pullMissing pulls ref unless the daemon holds it.
func pullMissing(t *testing.T, daemon *client.Client, ref string) {
	t.Helper()

	_, err := daemon.ImageInspect(context.Background(), ref)
	if err == nil {
		return
	}
	require.True(t, cerrdefs.IsNotFound(err), "inspect %s: %v", ref, err)
	pulled, err := daemon.ImagePull(context.Background(), ref, client.ImagePullOptions{})
	require.NoError(t, err)
	defer func() { _ = pulled.Close() }()
	_, err = io.Copy(io.Discard, pulled)
	require.NoError(t, err)
}
