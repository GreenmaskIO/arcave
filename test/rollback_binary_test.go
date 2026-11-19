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
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

// rebuiltReplicaSpec is two replicas on what compat's inspect drops: a volume at a subpath, a bind
// of the mount API and a log size. Compose creates both; a rollback rebuilds the second by itself.
const rebuiltReplicaSpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    deploy: {replicas: 2}
    logging:
      driver: json-file
      options: {max-size: 1MB}
    volumes:
      - {type: volume, source: shared, target: /sub, read_only: true, volume: {subpath: app}}
      - {type: bind, source: %[2]s, target: /host, read_only: true, bind: {create_host_path: false}}
volumes:
  shared: {name: %[1]s-shared, external: true}
`

// The built binary, as the user runs it: a replica rebuilt by itself keeps its subpath and log size,
// and a volume comes back with its owner. Each goes wrong on Podman alone, should the wiring hand a
// workflow the wrong engine.
func TestRollback_ThroughTheBuiltBinary(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newComposeProject(t, "binary-rollback-it")
	daemontest.SkipWithoutVolumeSubpath(t, env.daemon)
	shared := env.project + "-shared"
	env.externalVolume(t, shared, "mkdir /data/app && printf %s kept > /data/app/marker && chown 999:999 /data/app/marker")
	env.upDocument(t, fmt.Sprintf(rebuiltReplicaSpec, env.project, t.TempDir()))
	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", "arcave-binary-rollback-"+daemontest.RandomSuffix(t))
	writeStorageConfig(t, projectRoot, config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: t.TempDir()},
	}, composeconfig.SnapshotBackendFullCopy)
	run := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	savepoint := decode[model.Savepoint](t, run("savepoint", "create", "two-replicas"))
	require.Len(t, savepoint.WritableLayers, 2, "one layer per replica, so the second has to be rebuilt")
	busybox(t, env.daemon, shared, "printf %s drifted > /data/app/marker && chown 0:0 /data/app/marker")

	// Act
	run("rollback", "two-replicas")

	// Assert
	containers := env.containers(t)
	require.Contains(t, containers, "app-2")
	replica := containers["app-2"]
	assert.Equal(t, "kept", env.read(t, replica.ID, "/sub/marker"), "the subpath, not the volume whole, and its data")
	assert.Equal(t, "999:999", strings.TrimSpace(env.exec(t, replica.ID, "stat", "-c", "%u:%g", "/sub/marker")), "its owner")
	assert.Equal(t, "1MB", readLogMaxSize(t, env.daemon, replica), "the log's size")
}

// readLogMaxSize is the log's max-size as the engine stores it: Podman's compat inspect drops it,
// so on Podman it is libpod's own inspect that says.
func readLogMaxSize(t *testing.T, daemon *client.Client, c container.InspectResponse) string {
	t.Helper()

	podman, err := daemontest.IsPodman(context.Background(), daemon)
	require.NoError(t, err)
	if !podman {
		return c.HostConfig.LogConfig.Config["max-size"]
	}
	// Any host: the transport dials the daemon's own socket.
	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return daemon.Dialer()(ctx) },
	}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://libpod/v4.0.0/libpod/containers/"+c.ID+"/json", nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var inspected struct {
		HostConfig struct {
			LogConfig struct{ Size string }
		}
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inspected))
	return inspected.HostConfig.LogConfig.Size
}
