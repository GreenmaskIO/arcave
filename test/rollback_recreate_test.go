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
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// recreateSpec is the compose project under test; mode and the published port are the drift.
const recreateSpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sh", "-c", "sleep 3600"]
    environment:
      MODE: %[2]s
      KEEP: constant
    labels:
      test.arcave-it: %[2]s
    working_dir: /data
    hostname: app-host
    stop_signal: SIGKILL
    healthcheck:
      test: ["CMD-SHELL", "true"]
      interval: 1s
      timeout: 1s
      retries: 3
    ports:
      - "127.0.0.1:%[3]d:8080"
    volumes:
      - data:/data
      - /anon
      - %[4]s:/host
    networks:
      back:
        aliases: [%[2]s-alias]
volumes:
  data: {}
networks:
  back: {}
`

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

// A replica a rollback rebuilds by itself, from its own inspect, comes back with the mounts and the
// log size compose created it with.
func TestRollback_RebuiltReplicaKeepsItsMountsAndLogSize(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	skipWithoutVolumeSubpath(t, env.daemon)
	env.externalVolume(t, env.project+"-shared", "mkdir /data/app && printf %s kept > /data/app/marker")
	env.upDocument(t, fmt.Sprintf(rebuiltReplicaSpec, env.project, t.TempDir()))
	containers := env.containers(t)
	require.Len(t, containers, 2)
	require.Contains(t, containers, "app-2")
	before := containers["app-2"]
	storedBefore := env.storedMounts(t, before.ID)
	for name, c := range containers {
		env.exec(t, c.ID, "sh", "-c", fmt.Sprintf("printf %%s %s > %s", name, layerMarker))
	}
	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "two-replicas"})
	require.NoError(t, err)
	require.Len(t, savepoint.WritableLayers, 2, "one layer per replica, so the second has to be rebuilt")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	containers = env.containers(t)
	require.Contains(t, containers, "app-2")
	after := containers["app-2"]
	assert.Equal(t, "app-2", env.read(t, after.ID, layerMarker), "rebuilt on its own layer, not its service's")
	assert.Equal(t, "kept", env.read(t, after.ID, "/sub/marker"), "the subpath, not the volume whole")
	assert.Equal(t, before.Inspect.HostConfig.Binds, after.Inspect.HostConfig.Binds, "binds")
	assert.ElementsMatch(t, before.Inspect.HostConfig.Mounts, after.Inspect.HostConfig.Mounts, "mount requests")
	assert.Equal(t, "1MB", after.Inspect.HostConfig.LogConfig.Config["max-size"], "the log's size")
	assert.ElementsMatch(t, storedBefore, env.storedMounts(t, after.ID), "what the daemon stores of each mount")
}

// The built binary, over the DI root itself and not this package's mirror of it: a replica rebuilt by
// itself keeps its subpath and log size, and a volume comes back with its owner. Each goes wrong on
// Podman alone, should the wiring hand a workflow the wrong engine.
func TestRollback_ThroughTheBuiltBinary(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	skipWithoutVolumeSubpath(t, env.daemon)
	shared := env.project + "-shared"
	env.externalVolume(t, shared, "mkdir /data/app && printf %s kept > /data/app/marker && chown 999:999 /data/app/marker")
	env.upDocument(t, fmt.Sprintf(rebuiltReplicaSpec, env.project, t.TempDir()))
	projectRoot := t.TempDir()
	runArcave(t, projectRoot, nil, "init", "--name", "arcave-binary-rollback")
	writeStorageConfig(t, projectRoot, config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: t.TempDir()},
	}, config.SnapshotBackendFullCopy)
	run := func(args ...string) []byte {
		t.Helper()
		return runArcave(t, projectRoot, nil, append([]string{"-p", env.project}, args...)...)
	}
	savepoint := decode[model.Savepoint](t, run("savepoint", "create", "two-replicas"))
	require.Len(t, savepoint.WritableLayers, 2, "one layer per replica, so the second has to be rebuilt")
	busybox(t, dockerclient.NewEngine(env.daemon), shared, "printf %s drifted > /data/app/marker && chown 0:0 /data/app/marker")

	// Act
	run("rollback", "two-replicas")

	// Assert
	containers := env.containers(t)
	require.Contains(t, containers, "app-2")
	replica := containers["app-2"]
	assert.Equal(t, "kept", env.read(t, replica.ID, "/sub/marker"), "the subpath, not the volume whole, and its data")
	assert.Equal(t, "999:999", strings.TrimSpace(env.exec(t, replica.ID, "stat", "-c", "%u:%g", "/sub/marker")), "its owner")
	assert.Equal(t, "1MB", replica.Inspect.HostConfig.LogConfig.Config["max-size"], "the log's size")
}

// skipWithoutVolumeSubpath skips on a Docker older than the API that mounts a volume at a subpath.
func skipWithoutVolumeSubpath(t *testing.T, daemon *client.Client) {
	t.Helper()

	const volumeSubpathAPI = "1.45"
	if isPodman(t, daemon) {
		return
	}
	version, err := daemon.ServerVersion(context.Background(), client.ServerVersionOptions{})
	require.NoError(t, err)
	if versions.LessThan(version.APIVersion, volumeSubpathAPI) {
		t.Skipf("the daemon's API %s mounts no volume at a subpath", version.APIVersion)
	}
}

// storedMounts are a container's mounts as libpod stores them; none on Docker, which has no libpod.
func (e *imageRollbackEnv) storedMounts(t *testing.T, containerID string) []libpod.Mount {
	t.Helper()

	runtime, err := dockerclient.ReadRuntime(e.ctx, e.daemon)
	require.NoError(t, err)
	if runtime.Kind != model.ContainerRuntimePodman {
		return nil
	}
	stored, err := newLibpodClient(t, e.daemon, runtime.Version).InspectContainer(e.ctx, containerID)
	require.NoError(t, err)
	return stored.Mounts
}

func TestRollback_RebuildsTheComposeProjectFromTheSavepointSpec(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newRecreateEnv(t)
	env.up(t, env.spec(t, "original", env.port))
	env.write(t, env.namedVolume(), "marker", "original data")
	env.write(t, env.anonVolume(t), "marker", "original anon")
	require.NoError(t, os.WriteFile(filepath.Join(env.hostDir, "marker"), []byte("original bind"), 0o600))
	before := env.inspect(t)

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "before-drift"})
	require.NoError(t, err)
	require.NotNil(t, savepoint.Spec, "the savepoint records the compose project it was taken of")

	// The compose project drifts: a new environment, label, alias and published port.
	drifted := env.port + 1
	env.down(t)
	env.up(t, env.spec(t, "drifted", drifted))
	env.write(t, env.namedVolume(), "marker", "drifted data")
	env.write(t, env.anonVolume(t), "marker", "drifted anon")
	require.NoError(t, os.WriteFile(filepath.Join(env.hostDir, "marker"), []byte("drifted bind"), 0o600))

	// Act
	res, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, savepoint.ID, res.RolledBackTo.ID)

	after := env.inspect(t)
	assert.Equal(t, "original data", env.read(t, env.namedVolume(), "marker"))
	assert.Equal(t, "original anon", env.read(t, env.anonVolume(t), "marker"))
	bind, err := os.ReadFile(filepath.Join(env.hostDir, "marker"))
	require.NoError(t, err)
	assert.Equal(t, "original bind", string(bind))

	// The container is a new one, so the fields are compared, not the id.
	assert.NotEqual(t, before.ID, after.ID, "a rollback rebuilds the containers")
	assert.Equal(t, envOf(before), envOf(after), "environment")
	assert.Equal(t, before.Config.Cmd, after.Config.Cmd, "command")
	assert.Equal(t, before.Config.Entrypoint, after.Config.Entrypoint, "entrypoint")
	assert.Equal(t, before.Config.WorkingDir, after.Config.WorkingDir, "working_dir")
	assert.Equal(t, before.Config.Hostname, after.Config.Hostname, "hostname")
	assert.Equal(t, before.Config.StopSignal, after.Config.StopSignal, "stop_signal")
	assert.Equal(t, before.Config.Healthcheck, after.Config.Healthcheck, "healthcheck")
	assert.Equal(t, before.Config.Labels["test.arcave-it"], after.Config.Labels["test.arcave-it"], "labels")
	assert.Equal(t, before.HostConfig.PortBindings, after.HostConfig.PortBindings, "ports")
	assert.Equal(t, mountTargets(before), mountTargets(after), "mounts")
	assert.Equal(t, networkAliases(before), networkAliases(after), "network aliases")
}

// recreateEnv is the one compose project this test drives, wired the way cmd/arcave wires it.
type recreateEnv struct {
	ctx       context.Context
	svc       *core.Service
	compose   *composeclient.Client
	engine    *dockerclient.Engine
	daemon    *client.Client
	inspector *introspect.Inspector
	codec     *composespec.Codec
	project   string
	hostDir   string
	port      int
}

func newRecreateEnv(t *testing.T) *recreateEnv {
	t.Helper()

	daemonAPI := newDaemonClient(t)
	project := "arcave-recreate-it-" + randomSuffix(t)

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	composeService, prompter := newComposeService(t, cli)
	profile := newRuntimeProfile(t, daemonAPI)
	inspector := introspect.NewInspector(profile.api, model.DockerComposeProjectName(project))
	runner := composeclient.NewClient(composeService, composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second, Prompter: prompter,
	})
	codec := composespec.NewCodec()

	projectRoot := t.TempDir()
	store, err := storefactory.New(t.Context(), config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}, projectRoot, "arcave-recreate-tenant", model.DefaultSession, model.DockerComposeProjectName(project))
	require.NoError(t, err)

	// The blob store is a sibling of the savepoints inside the compose project root, so it
	// dies with the session the way everything else there does.
	blobStore, err := store.SubStorage(dedupcopy.NamespaceBlobs, true)
	require.NoError(t, err)

	svc := core.New(core.Deps{
		ArcaveProject:      "arcave-recreate-tenant",
		SessionLocker:      newStoreLocker(store, model.DefaultSession),
		SavepointRepo:      sprepo.New(store),
		ProjectLocker:      lock.New(projectRoot),
		ComposeInspector:   inspector,
		ProjectAssembler:   profile.assembler,
		SpecCodec:          codec,
		ComposeRunner:      runner,
		VolumeRemover:      profile.engine,
		ImageBackend:       newImageCopier(store, newLayerOwner("arcave-recreate-tenant", project), profile.engine, blobStore),
		ContainerRebuilder: profile.engine,
		ImageLookup:        profile.engine,
		BackendRegistry:    mustRegistry(t, profile.volumes),
		Inflight:           inflightrepo.New(projectRoot),
		Store:              store,
		CaptureDefaults:    newCaptureDefaults(model.SnapshotBackendFullCopy),
	})

	env := &recreateEnv{
		ctx:       zerolog.New(io.Discard).WithContext(context.Background()),
		svc:       svc,
		compose:   runner,
		engine:    profile.engine,
		daemon:    daemonAPI,
		inspector: inspector,
		codec:     codec,
		project:   project,
		hostDir:   t.TempDir(),
		port:      freePort(t),
	}
	t.Cleanup(func() { removeComposeProject(t, env.daemon, env.project) })
	return env
}

func mustRegistry(t *testing.T, volumes fullcopy.VolumeStreamer) model.SnapshotBackendRegistry {
	t.Helper()

	registry, err := core.NewSnapshotBackendRegistry(fullcopy.NewFactory(volumes, fullcopy.CompressionZstd))
	require.NoError(t, err)
	return registry
}

// spec renders the compose project's document through the savepoint artifact's codec.
func (e *recreateEnv) spec(t *testing.T, mode string, port int) *types.Project {
	t.Helper()

	document := fmt.Sprintf(recreateSpec, e.project, mode, port, e.hostDir)
	project, err := e.codec.Parse(e.ctx, []byte(document))
	require.NoError(t, err)
	return project
}

func (e *recreateEnv) up(t *testing.T, project *types.Project) {
	t.Helper()

	require.NoError(t, e.compose.Create(e.ctx, project, nil))
	require.NoError(t, e.compose.Start(e.ctx, project, 90*time.Second))
}

func (e *recreateEnv) down(t *testing.T) {
	t.Helper()

	// Down keeps volumes, so the anonymous ones it orphans are named first and dropped after.
	byProject := make(client.Filters).Add("label", api.ProjectLabel+"="+e.project)
	containers, err := e.daemon.ContainerList(e.ctx, client.ContainerListOptions{All: true, Filters: byProject})
	require.NoError(t, err)

	// By name: whatever spec built the compose project, this takes it down.
	assert.NoError(t, e.compose.Down(e.ctx, &types.Project{Name: e.project}))

	for _, c := range containers.Items {
		for _, m := range c.Mounts {
			if m.Type != mount.TypeVolume {
				continue
			}
			v, err := e.daemon.VolumeInspect(e.ctx, m.Name, client.VolumeInspectOptions{})
			if !assert.NoError(t, err, "inspect volume %s", m.Name) {
				continue
			}
			if _, anonymous := v.Volume.Labels[anonymousVolumeLabel]; anonymous {
				_, err := e.daemon.VolumeRemove(e.ctx, m.Name, client.VolumeRemoveOptions{})
				assert.NoError(t, err, "remove volume %s", m.Name)
			}
		}
	}
}

// inspect reports the service's one container as the daemon holds it.
func (e *recreateEnv) inspect(t *testing.T) container.InspectResponse {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	require.Len(t, res.Project.Services, 1)
	require.Len(t, res.Project.Services[0].Containers, 1)
	return res.Project.Services[0].Containers[0].Inspect
}

func (e *recreateEnv) namedVolume() string { return e.project + "_data" }

// anonVolume is the docker name of the service's anonymous mount.
func (e *recreateEnv) anonVolume(t *testing.T) string {
	t.Helper()

	for _, mount := range e.inspect(t).Mounts {
		if mount.Destination == "/anon" {
			return mount.Name
		}
	}
	require.FailNow(t, "the service mounts no anonymous volume at /anon")
	return ""
}

func (e *recreateEnv) write(t *testing.T, volumeName, file, content string) {
	t.Helper()

	busybox(t, e.engine, volumeName, fmt.Sprintf("printf %%s %q > /data/%s", content, file))
}

func (e *recreateEnv) read(t *testing.T, volumeName, file string) string {
	t.Helper()

	return busybox(t, e.engine, volumeName, "cat /data/"+file)
}

// envOf is the container's environment as a comparable set.
func envOf(c container.InspectResponse) map[string]string {
	env := map[string]string{}
	for _, entry := range c.Config.Env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}

// mountTargets is every mount the container carries, sorted: the daemon's order is unstable.
func mountTargets(c container.InspectResponse) []string {
	targets := make([]string, 0, len(c.Mounts))
	for _, mount := range c.Mounts {
		targets = append(targets, string(mount.Type)+" "+mount.Destination)
	}
	sort.Strings(targets)
	return targets
}

// networkAliases is the aliases the container carries, keyed by docker network name.
func networkAliases(c container.InspectResponse) map[string][]string {
	aliases := map[string][]string{}
	for name, endpoint := range c.NetworkSettings.Networks {
		// The container's short id is no declared alias: Podman lists it, and it is another
		// container's after a rebuild.
		aliases[name] = slices.DeleteFunc(slices.Clone(endpoint.Aliases), func(alias string) bool {
			return alias != "" && strings.HasPrefix(c.ID, alias)
		})
	}
	return aliases
}

// freePort takes a free port, so two copies of this test do not collide.
func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}
