package test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/api/types/container"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// recreateSpec is the stack under test; mode and the published port are the drift.
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
      arcave.it: %[2]s
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

func TestRollback_RebuildsTheStackFromTheSavepointSpec(t *testing.T) {
	// Arrange
	env := newRecreateEnv(t)
	env.up(t, env.spec(t, "original", env.port))
	env.write(t, env.namedVolume(), "marker", "original data")
	env.write(t, env.anonVolume(t), "marker", "original anon")
	require.NoError(t, os.WriteFile(filepath.Join(env.hostDir, "marker"), []byte("original bind"), 0o600))
	before := env.inspect(t)

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "before-drift"})
	require.NoError(t, err)
	require.NotNil(t, savepoint.Spec, "the savepoint records the stack it was taken of")

	// The stack drifts: a new environment, label, alias and published port.
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
	assert.Equal(t, savepoint.ID, res.Savepoint.ID)

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
	assert.Equal(t, before.Config.Labels["arcave.it"], after.Config.Labels["arcave.it"], "labels")
	assert.Equal(t, before.HostConfig.PortBindings, after.HostConfig.PortBindings, "ports")
	assert.Equal(t, mountTargets(before), mountTargets(after), "mounts")
	assert.Equal(t, networkAliases(before), networkAliases(after), "network aliases")
}

// recreateEnv is the one stack this test drives, wired the way cmd/arcave wires it.
type recreateEnv struct {
	ctx       context.Context
	svc       *core.Service
	compose   *composeclient.Client
	engine    *dockerclient.Engine
	inspector *introspect.Inspector
	assembler *composespec.Assembler
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
	composeService := dockercompose.NewComposeService(cli)
	progress.Mode = progress.ModeQuiet

	engine := dockerclient.NewEngine(daemonAPI)
	inspector := introspect.NewInspector(daemonAPI, model.StackName(project))
	runner := composeclient.NewClient(composeService, composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second,
	})
	assembler := composespec.NewAssembler()

	projectRoot := t.TempDir()
	store, err := storefactory.New(config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}, projectRoot, "arcave-recreate-tenant", model.DefaultSession, model.StackName(project))
	require.NoError(t, err)

	svc := core.New(core.Deps{
		SavepointRepo:    sprepo.New(store),
		ProjectLocker:    lock.New(projectRoot),
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		ComposeRunner:    runner,
		VolumeRemover:    engine,
		BackendRegistry:  mustRegistry(t, engine),
		Inflight:         inflightrepo.New(projectRoot),
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})

	env := &recreateEnv{
		ctx:       zerolog.New(io.Discard).WithContext(context.Background()),
		svc:       svc,
		compose:   runner,
		engine:    engine,
		inspector: inspector,
		assembler: assembler,
		project:   project,
		hostDir:   t.TempDir(),
		port:      freePort(t),
	}
	t.Cleanup(func() { env.down(t) })
	return env
}

func mustRegistry(t *testing.T, engine *dockerclient.Engine) model.SnapshotBackendRegistry {
	t.Helper()

	registry, err := core.NewSnapshotBackendRegistry(fullcopy.NewFactory(engine))
	require.NoError(t, err)
	return registry
}

// spec renders the stack's compose document through the savepoint artifact's codec.
func (e *recreateEnv) spec(t *testing.T, mode string, port int) *types.Project {
	t.Helper()

	document := fmt.Sprintf(recreateSpec, e.project, mode, port, e.hostDir)
	project, err := e.assembler.Parse(e.ctx, []byte(document))
	require.NoError(t, err)
	return project
}

func (e *recreateEnv) up(t *testing.T, project *types.Project) {
	t.Helper()

	require.NoError(t, e.compose.Create(e.ctx, project))
	require.NoError(t, e.compose.Start(e.ctx, project, 90*time.Second))
}

func (e *recreateEnv) down(t *testing.T) {
	t.Helper()

	// By name: whatever spec built the stack, this takes it down.
	assert.NoError(t, e.compose.Down(e.ctx, &types.Project{Name: e.project}))
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
		aliases[name] = endpoint.Aliases
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
