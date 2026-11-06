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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/greenmaskio/arcave/internal/config"
)

// containerdSnapshotter is how `docker info` names the containerd image store's driver type.
const containerdSnapshotter = "io.containerd.snapshotter.v1"

// imageLabelArcaveProject is the arcave project the image label test inits.
const imageLabelArcaveProject = "label-it"

// compose 5.5 labels a container with its platform manifest on the containerd store, where
// earlier versions labelled the image id. A container arcave recreates must carry the label the
// user's own compose writes, or `docker compose up` recreates it, and arcave its in turn.
func TestRollback_ComposeImageLabelMatchesInstalledCompose(t *testing.T) {
	// Arrange
	daemon := newDaemonClient(t)
	requireContainerdStore(t, daemon)
	requireInstalledCompose(t, 5, 5)
	project := "arcave-label-it-" + randomSuffix(t)
	projectRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "compose.yaml"),
		[]byte("services:\n  app: {image: "+busyboxImage+", command: [sleep, infinity]}\n"), 0o600))
	t.Cleanup(func() { removeComposeProject(t, daemon, project) })

	dockerCompose(t, projectRoot, project, "up", "-d")
	installedID, installedLabel := appContainer(t, daemon, project)
	runArcave(t, projectRoot, nil, "init", "--name", imageLabelArcaveProject)
	disableLayerCapture(t, projectRoot)

	// Act: without a layer, the rollback recreates the container from the image alone.
	runArcave(t, projectRoot, nil, "-p", project, "savepoint", "create", "sp1")
	runArcave(t, projectRoot, nil, "-p", project, "rollback", "latest")
	rolledBackID, rolledBackLabel := appContainer(t, daemon, project)
	dockerCompose(t, projectRoot, project, "up", "-d")
	upAgainID, _ := appContainer(t, daemon, project)

	// Assert
	assert.NotEqual(t, installedID, rolledBackID, "the rollback recreated the container")
	assert.Equal(t, installedLabel, rolledBackLabel, "arcave labels the image as the installed compose does")
	assert.Equal(t, rolledBackID, upAgainID, "docker compose up leaves the container arcave recreated")
}

// requireContainerdStore skips unless the daemon keeps its images in the containerd store.
func requireContainerdStore(t *testing.T, daemon *client.Client) {
	t.Helper()

	info, err := daemon.Info(context.Background(), client.InfoOptions{})
	require.NoError(t, err)
	if !slices.ContainsFunc(info.Info.DriverStatus, func(status [2]string) bool {
		return status[1] == containerdSnapshotter
	}) {
		t.Skip("the daemon does not use the containerd image store")
	}
}

// requireInstalledCompose skips unless the host's `docker compose` is major.minor or newer.
func requireInstalledCompose(t *testing.T, major, minor int) {
	t.Helper()

	out, err := exec.Command("docker", "compose", "version", "--short").Output()
	if err != nil {
		t.Skip("no docker compose on the host: " + err.Error())
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		t.Skipf("unrecognised docker compose version %q", version)
	}
	gotMajor, errMajor := strconv.Atoi(parts[0])
	gotMinor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		t.Skipf("unrecognised docker compose version %q", version)
	}
	if gotMajor < major || gotMajor == major && gotMinor < minor {
		t.Skipf("docker compose %s is older than %d.%d", version, major, minor)
	}
}

// dockerCompose runs the host's own `docker compose` on the project in projectRoot.
func dockerCompose(t *testing.T, projectRoot, project string, args ...string) {
	t.Helper()

	cmd := exec.Command("docker", append([]string{"compose", "-p", project}, args...)...)
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker compose %v: %s", args, out)
}

// appContainer is the id and image label of the project's one container.
func appContainer(t *testing.T, daemon *client.Client, project string) (string, string) {
	t.Helper()

	listed, err := daemon.ContainerList(context.Background(), client.ContainerListOptions{
		All: true, Filters: client.Filters{}.Add("label", api.ProjectLabel+"="+project),
	})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	return listed.Items[0].ID, listed.Items[0].Labels[api.ImageDigestLabel]
}

// disableLayerCapture turns writable-layer capture off in the project's config.yaml.
func disableLayerCapture(t *testing.T, projectRoot string) {
	t.Helper()

	file := config.Path(projectRoot)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	off := false
	cfg.Capture.WritableLayers = &off
	data, err = yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, data, 0o600))
}
