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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// minComposeCLIVersion is the oldest `docker compose` whose image label matches arcave's own.
const minComposeCLIVersion = "5.6"

// imageLabelSpec is one service on a pinned image with one volume, what a savepoint needs.
const imageLabelSpec = `
name: %s
services:
  app:
    image: ` + fidelityBusybox + `
    command: ["sleep", "infinity"]
    stop_signal: SIGKILL
    volumes: ["data:/data"]
volumes:
  data: {}
`

// volumeDriftSpec mounts a captured named volume and one excluded from capture, both labelled
// with a variant: a different one makes compose ask before it recreates either.
const volumeDriftSpec = `
name: %[1]s
services:
  app:
    image: ` + busyboxImage + `
    command: ["sleep", "infinity"]
    stop_signal: SIGKILL
    volumes: ["data:/data", "kept:/kept"]
volumes:
  data:
    labels: {test.variant: "%[2]s"}
  kept:
    labels: {test.variant: "%[2]s", arcave.capture: "false"}
`

// A container a rollback builds carries the image label the user's own compose writes, so
// `docker compose up` does not read the image as changed.
func TestRollback_KeepsComposeImageLabel(t *testing.T) {
	t.Parallel()

	// Arrange
	daemon := newDaemonClient(t)
	requireComposeCLI(t, minComposeCLIVersion)
	requireContainerdStore(t, daemon)
	project := "arcave-compose-cli-it-" + randomSuffix(t)
	dir := t.TempDir()
	t.Cleanup(func() { removeComposeProject(t, daemon, project) })
	writeComposeFile(t, dir, fmt.Sprintf(imageLabelSpec, project))
	runComposeCLI(t, dir, project, "up", "-d", "--wait")
	container := project + "-app-1"
	want := readContainerLabel(t, daemon, container, api.ImageDigestLabel)
	require.NotEmpty(t, want, "compose labels the container with its image")
	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", project)
	writeStorageConfig(t, projectRoot, startDirectoryStorage(t).file, config.SnapshotBackendFullCopy)
	noLayers := []string{"ARCAVE_CAPTURE_WRITABLE_LAYERS=false"}
	runArcave(t, projectRoot, noLayers, "-p", project, "savepoint", "create", "sp1")

	// Act
	runArcave(t, projectRoot, noLayers, "-p", project, "rollback", "sp1")

	// Assert
	assert.Equal(t, want, readContainerLabel(t, daemon, container, api.ImageDigestLabel))
}

// A drifted volume the savepoint restores is recreated and refilled; one it does not hold keeps its
// config and data with a warning, and compose's question never reaches stdout.
func TestRollback_RecreatesOnlyARestoredVolume(t *testing.T) {
	t.Parallel()

	// Arrange
	daemon := newDaemonClient(t)
	requireComposeCLI(t, minComposeCLIVersion)
	engine := dockerclient.NewEngine(daemon)
	project := "arcave-volume-drift-it-" + randomSuffix(t)
	data, kept := project+"_data", project+"_kept"
	dir := t.TempDir()
	t.Cleanup(func() { removeComposeProject(t, daemon, project) })
	writeComposeFile(t, dir, fmt.Sprintf(volumeDriftSpec, project, "a"))
	runComposeCLI(t, dir, project, "up", "-d", "--wait")
	busybox(t, engine, data, "printf a > /data/marker")
	busybox(t, engine, kept, "printf a > /data/marker")
	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", project)
	writeStorageConfig(t, projectRoot, startDirectoryStorage(t).file, config.SnapshotBackendFullCopy)
	runArcave(t, projectRoot, nil, "-p", project, "savepoint", "create", "sp1")
	runComposeCLI(t, dir, project, "down")
	writeComposeFile(t, dir, fmt.Sprintf(volumeDriftSpec, project, "b"))
	runComposeCLI(t, dir, project, "up", "-d", "--wait", "--yes")
	busybox(t, engine, data, "printf b > /data/marker")
	busybox(t, engine, kept, "printf b > /data/marker")

	// Act
	out := runArcave(t, projectRoot, nil, "-p", project, "--progress", "json", "rollback", "sp1")

	// Assert
	events := readProgressEvents(t, out)
	assert.Equal(t, 1, events.results, "stdout ends on one result")
	assert.Equal(t, map[string]progress.Kind{kept: "compose.create"}, events.warned,
		"the volume left with its config is warned of by name, on compose's create")
	assert.Equal(t, "a", readVolumeLabel(t, daemon, data, "test.variant"), "the restored volume is the savepoint's")
	assert.Equal(t, "a", busybox(t, engine, data, "cat /data/marker"))
	assert.Equal(t, "b", readVolumeLabel(t, daemon, kept, "test.variant"), "the volume not restored is kept")
	assert.Equal(t, "b", busybox(t, engine, kept, "cat /data/marker"))
}

// progressEvents are the resources a --progress json stream warned of, by the kind of the step
// they were warned on, and how many results it carried; its every line is JSON.
type progressEvents struct {
	warned  map[string]progress.Kind
	results int
}

func readProgressEvents(t *testing.T, out []byte) progressEvents {
	t.Helper()

	var (
		kinds   = map[progress.StepID]progress.Kind{}
		warned  = map[string]progress.Kind{}
		results int
	)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		var line progressLine
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line), "stdout carries JSON alone: %s", scanner.Bytes())
		if line.Type == "result" {
			results++
		}
		if line.Type != "progress" {
			continue
		}
		evt := line.Event
		if evt.Open != nil {
			kinds[evt.StepID] = evt.Open.Step.Kind
		}
		if evt.Status == progress.StatusWarned && evt.Resource != nil {
			warned[evt.Resource.ID] = kinds[evt.StepID]
		}
	}
	require.NoError(t, scanner.Err())
	return progressEvents{
		warned:  warned,
		results: results,
	}
}

// requireComposeCLI skips unless the docker CLI's compose plugin is at least minVersion.
func requireComposeCLI(t *testing.T, minVersion string) {
	t.Helper()

	out, err := exec.Command("docker", "compose", "version", "--short").Output()
	if err != nil {
		t.Skipf("skipping: no docker compose CLI: %v", err)
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if versions.LessThan(version, minVersion) {
		t.Skipf("skipping: docker compose %s is older than %s", version, minVersion)
	}
}

// requireContainerdStore skips unless the daemon stores images in containerd, where compose's
// image label and an image id differ.
func requireContainerdStore(t *testing.T, daemon *client.Client) {
	t.Helper()

	info, err := daemon.Info(t.Context(), client.InfoOptions{})
	require.NoError(t, err)
	for _, status := range info.Info.DriverStatus {
		if strings.Contains(status[1], "io.containerd.snapshotter") {
			return
		}
	}
	t.Skip("skipping: the daemon does not store images in containerd")
}

func writeComposeFile(t *testing.T, dir, document string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(document), 0o600))
}

// runComposeCLI runs the user's own `docker compose` on dir's compose.yaml.
func runComposeCLI(t *testing.T, dir, project string, args ...string) {
	t.Helper()

	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", filepath.Join(dir, "compose.yaml")}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker compose %v: %s", args, out)
}

func readContainerLabel(t *testing.T, daemon *client.Client, name, label string) string {
	t.Helper()

	inspect, err := daemon.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
	require.NoError(t, err)
	return inspect.Container.Config.Labels[label]
}

func readVolumeLabel(t *testing.T, daemon *client.Client, name, label string) string {
	t.Helper()

	inspect, err := daemon.VolumeInspect(t.Context(), name, client.VolumeInspectOptions{})
	require.NoError(t, err)
	return inspect.Volume.Labels[label]
}
