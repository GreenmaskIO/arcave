// Package test holds the docker runtime's cross-package integration tests —
// those that need a live daemon and more than one of the packages under
// provider/docker.
package test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/rest/repo"
	dockerclient "github.com/greenmaskio/arcave/internal/provider/docker/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/core"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/lock"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
	inflightrepo "github.com/greenmaskio/arcave/internal/provider/docker/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/provider/docker/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/store"
	"github.com/greenmaskio/arcave/internal/provider/docker/stack"
	stackmodel "github.com/greenmaskio/arcave/internal/provider/docker/stack/model"
)

// This is the cdc backend's proof, wired the way cmd/arcave wires it: real
// stores, the real repository registry behind the real loopback endpoint, the real docker
// engine, and stock restic containers on the far side. Only the two compose
// halves are doubled — starting and stopping a stack proves nothing this test is
// about, and its containers stay created either way, which is the state arcave
// captures in.
//
// It is guarded twice: -short skips it, and so does an unreachable arcaved.

const (
	// cdcProject is the compose project name the container labels carry; arcave
	// finds a volume's mount through them, and it is the stack segment of the
	// store root.
	cdcProject = "arcave-cdc-it"
	// cdcTenant is the storage tenant — cfg.Project.Name, deliberately spelled
	// differently from the stack so the two levels of the store root stay
	// distinguishable in an assertion.
	cdcTenant = "arcave-it-tenant"
	// blobKiB is the payload per volume — small enough to run in seconds, large
	// enough that an increment is visibly smaller than a full capture. Random
	// bytes, because zeros would deduplicate into a repository proving nothing.
	blobKiB = 8192
	// mutateKiB is what changes between the two captures: a few percent.
	mutateKiB = 256
	// busyboxImage fills, mutates and hashes the volumes.
	busyboxImage = "busybox"
	// cdcContainerHost is how a container names the host the restic server listens
	// on — the config default, which the DI root supplies and the transport keeps
	// no copy of.
	cdcContainerHost = "host.docker.internal"
)

func TestCDCBackend_CreateAndRollback(t *testing.T) {
	tests := []struct {
		name string
		cdc  stack.CDCBackend
	}{
		{
			name: "wipe clears the mount and restores it whole",
			cdc: stack.CDCBackend{
				RestoreMode: stack.RestoreModeWipe,
				Overwrite:   stack.OverwriteIfChanged,
				Image:       stack.DefaultResticImage,
			},
		},
		{
			name: "incremental restores over the live data",
			cdc: stack.CDCBackend{
				RestoreMode: stack.RestoreModeIncremental,
				Overwrite:   stack.OverwriteAlways,
				Image:       stack.DefaultResticImage,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: two named volumes, each mounted by its own labelled container.
			env := newCDCEnv(t, tt.cdc)
			before := env.hashAll(t)

			// Act & Assert, one gate at a time.

			// The first capture initialises each volume's repository and ships it whole.
			first, err := env.svc.CreateSavepoint(env.ctx, dockermodel.CreateSavepointRequest{Name: "before-migration"})
			require.NoError(t, err)
			require.Len(t, first.Stats, len(env.volumes))
			for _, stat := range first.Stats {
				payload := resticPayload(t, stat)
				assert.NotEmpty(t, payload.SnapshotObjectID, "%s", stat.Volume.ObjectID)
				assert.Positive(t, payload.TotalBytesProcessed, "%s", stat.Volume.ObjectID)
				// One repository per volume, beside the savepoint directories under
				// the stack root — a real restic repository a user can open with a
				// plain restic CLI.
				exists, err := env.stackStore.Exists(env.ctx, "restic/"+stat.Volume.ObjectID+"/config")
				require.NoError(t, err)
				assert.True(t, exists, "%s has no repository under the stack root", stat.Volume.ObjectID)
			}
			// The savepoint's own directory holds the record only: a cdc capture's
			// bytes live in the repository, never under the savepoint.
			assert.Equal(t, []string{"savepoint.json"}, objectNames(t, env.stackStore, first.ID.String()))

			// A capture after a small change must ship an increment rather than the
			// payload again, which is the whole reason this backend exists.
			env.mutate(t, env.volumes[0])
			second, err := env.svc.CreateSavepoint(env.ctx, dockermodel.CreateSavepointRequest{Name: "after-migration"})
			require.NoError(t, err)
			firstAdded, secondAdded := totalDataAdded(t, first), totalDataAdded(t, second)
			t.Logf("data added: first %d bytes, second %d bytes", firstAdded, secondAdded)
			assert.Less(t, secondAdded, firstAdded/4,
				"a capture after a ~%d KiB change must not reship the payload", mutateKiB)

			// Rolling back to the first savepoint returns every volume byte for byte.
			require.NotEqual(t, before, env.hashAll(t), "the mutation must actually change a volume")
			res, err := env.svc.Rollback(env.ctx, dockermodel.RollbackRequest{ID: &first.ID})
			require.NoError(t, err)
			assert.Len(t, res.Restored, len(env.volumes))
			assert.Equal(t, before, env.hashAll(t),
				"the rolled-back volumes must hash as they did at capture time")
		})
	}
}

// A rollback that fails after the restore replays the safety snapshot — which
// lives in the very same repository as the savepoints, as an increment over
// them, and commits its own record in the one savepoint store.
func TestCDCBackend_RevertsFromTheSharedRepository(t *testing.T) {
	// Arrange
	env := newCDCEnv(t, stack.CDCBackend{
		RestoreMode: stack.RestoreModeWipe,
		Overwrite:   stack.OverwriteIfChanged,
		Image:       stack.DefaultResticImage,
	})
	savepoint, err := env.svc.CreateSavepoint(env.ctx, dockermodel.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)

	// The state the safety snapshot will have to bring back.
	env.mutate(t, env.volumes[0])
	preRollback := env.hashAll(t)

	// The stack never comes up healthy after the restore, which is a failed
	// rollback however clean the restore was.
	env.compose.failNextUp = errors.New("unhealthy")

	// Act
	_, err = env.svc.Rollback(env.ctx, dockermodel.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed")
	assert.Equal(t, preRollback, env.hashAll(t),
		"the revert must put back exactly what the safety snapshot captured")
	assert.Contains(t, env.logs.String(), "reverted to the pre-rollback state")

	// A successful revert uncommits the backup, so nothing of it lists — not even
	// with the flag — and its captures are gone: for cdc they were snapshots in the
	// shared repository, dropped once the revert succeeded. The savepoint itself
	// is untouched.
	committed, err := env.svc.ListSavepoints(env.ctx, dockermodel.ListRequest{IncludeSafetySnapshots: true})
	require.NoError(t, err)
	require.Len(t, committed, 1)
	assert.Equal(t, savepoint.ID, committed[0].ID)
	assert.Equal(t, []string{"savepoint.json"}, objectNames(t, env.stackStore, savepoint.ID.String()))
	// Nothing of the backup is left beside the savepoint either: the stack root
	// holds that one savepoint directory and the repositories, and nothing else.
	assert.Equal(t, []string{savepoint.ID.String(), cdc.RepoNamespace}, dirNames(t, env.stackStore),
		"the backup's own directory is swept with it")
}

// cdcEnv is one wired arcave over a live arcaved, plus the volumes it captures.
type cdcEnv struct {
	ctx        context.Context
	svc        *core.Service
	compose    *stubComposeRunner
	logs       *syncBuffer
	engine     *dockerclient.Engine
	stackStore storages.Storager
	volumes    []cdcVolume
}

// cdcVolume is one docker volume and the labelled container that mounts it.
type cdcVolume struct {
	name    string
	service string
	target  string
}

// newCDCEnv builds the project, the arcaved objects and the wiring, and returns
// the service every test in this file drives.
func newCDCEnv(t *testing.T, cdcCfg stack.CDCBackend) *cdcEnv {
	t.Helper()

	cli := newDaemonClient(t)
	suffix := randomSuffix(t)
	volumes := []cdcVolume{
		{name: cdcProject + "-pgdata-" + suffix, service: "db", target: "/var/lib/postgresql/data"},
		{name: cdcProject + "-assets-" + suffix, service: "app", target: "/srv/assets"},
	}

	engine := dockerclient.NewEngine(cli)
	for _, v := range volumes {
		createVolume(t, cli, v.name)
		createLabelledContainer(t, cli, v.service, v)
		fill(t, engine, v.name)
	}

	// The store, exactly as the DI root builds it: one tree per stack inside the
	// session — savepoints, a rollback's undo and the cdc repositories alike. The
	// tenant is named apart from the stack, so an assertion on the tree shows
	// both levels rather than one name standing in for two.
	projectRoot := t.TempDir()
	stackStore, err := store.NewDirectory(filepath.Join(".arcave", "storage"), projectRoot, store.Scope{
		Project: cdcTenant,
		Session: model.DefaultSession,
		Family:  model.ProviderFamilyStack,
		Name:    cdcProject,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, stackStore.Close()) })

	composeRunner := &stubComposeRunner{}
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).Level(zerolog.InfoLevel).WithContext(context.Background())

	resticRoot, err := stackStore.SubStorage(cdc.RepoNamespace, true)
	require.NoError(t, err)
	resticServer := rest.NewEcho(ctx, repo.NewResolver(resticRoot))
	resticPort, err := rest.Serve(resticServer, "")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, resticServer.Close()) })

	registry, err := core.NewSnapshotBackendRegistry(cdc.NewFactory(cdc.Dependencies{
		Runner:         engine,
		RepoURLBase:    rest.RepositoryURLBase("", cdcContainerHost, resticPort),
		RepositoryRoot: resticRoot,
		Options: cdc.Options{
			Image:       cdcCfg.Image,
			Incremental: cdcCfg.RestoreMode == stack.RestoreModeIncremental,
			Overwrite:   cdcCfg.Overwrite,
		},
	}))
	require.NoError(t, err)

	svc := core.New(core.Deps{
		SavepointRepo:    sprepo.New(stackStore),
		ProjectLocker:    lock.New(projectRoot),
		ComposeInspector: stubInspector{result: introspectionOf(volumes)},
		ComposeRunner:    composeRunner,
		BackendRegistry:  registry,
		Inflight:         inflightrepo.New(projectRoot),
		Store:            stackStore,
		DefaultBackend:   dockermodel.SnapshotBackendCDC,
	})

	return &cdcEnv{
		ctx:        ctx,
		svc:        svc,
		compose:    composeRunner,
		logs:       logs,
		engine:     engine,
		stackStore: stackStore,
		volumes:    volumes,
	}
}

// syncBuffer collects the run's log. The restic server serves its requests
// concurrently, so a bare bytes.Buffer would be written from several goroutines at
// once — zerolog synchronises nothing on the writer's behalf.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// hashAll is a sorted sha256 listing of every file in every volume — the thing
// a rollback has to reproduce exactly.
func (e *cdcEnv) hashAll(t *testing.T) map[string]string {
	t.Helper()

	sums := make(map[string]string, len(e.volumes))
	for _, v := range e.volumes {
		out := busybox(t, e.engine, v.name, "find /data -type f -exec sha256sum {} + | sort")
		require.NotEmpty(t, out, "volume %s must hold files to hash", v.name)
		sums[v.name] = out
	}
	return sums
}

// mutate overwrites mutateKiB in the middle of a volume's blob, leaving its size
// alone — the shape of a schema migration, not of a rewrite.
func (e *cdcEnv) mutate(t *testing.T, v cdcVolume) {
	t.Helper()

	busybox(t, e.engine, v.name, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/blob bs=1k count=%d seek=%d conv=notrunc", mutateKiB, blobKiB/3))
}

// introspectionOf is what the compose inspector would report for the volumes
// this test created by hand.
func introspectionOf(volumes []cdcVolume) stackmodel.IntrospectionResult {
	named := make([]stackmodel.NamedVolume, 0, len(volumes))
	services := make([]stackmodel.Service, 0, len(volumes))
	for _, v := range volumes {
		named = append(named, stackmodel.NamedVolume{
			Key:    v.service,
			Name:   v.name,
			UsedBy: []dockermodel.ServiceReference{{Service: v.service, Target: v.target}},
		})
		services = append(services, stackmodel.Service{Name: v.service, Image: busyboxImage})
	}
	return stackmodel.IntrospectionResult{Project: stackmodel.ComposeProject{
		Name:     cdcProject,
		Services: services,
		Volumes:  named,
	}}
}

// resticPayload reads a stat's payload as the backend reported it — in memory,
// so it is still the struct rather than the map a stored record decodes to.
func resticPayload(t *testing.T, stat dockermodel.SnapshotStat) cdc.Payload {
	t.Helper()

	payload, ok := stat.Payload.(cdc.Payload)
	require.Truef(t, ok, "stat for %s carries no cdc payload: %T", stat.Volume.ObjectID, stat.Payload)
	return payload
}

func totalDataAdded(t *testing.T, record dockermodel.Savepoint) int64 {
	t.Helper()

	var total int64
	for _, stat := range record.Stats {
		total += resticPayload(t, stat).DataAdded
	}
	return total
}

// stubComposeRunner stands in for the stack's lifecycle: this test keeps its
// containers created and stopped throughout, which is the state a real run
// captures in anyway.
type stubComposeRunner struct {
	// failNextUp is consumed by the next restart and cleared, so the revert's own
	// restart still succeeds — otherwise the test would be asserting on a failed
	// revert, which is a different path.
	failNextUp error
}

func (s *stubComposeRunner) Stop(context.Context, ...string) error { return nil }

func (s *stubComposeRunner) Up(context.Context, time.Duration, ...string) error {
	if err := s.failNextUp; err != nil {
		s.failNextUp = nil
		return err
	}
	return nil
}

// stubInspector reports the project this test built by hand, keeping compose
// file resolution out of a test about the backend.
type stubInspector struct {
	result stackmodel.IntrospectionResult
}

func (s stubInspector) Introspect(context.Context, ...string) (stackmodel.IntrospectionResult, error) {
	return s.result, nil
}

// newDaemonClient skips the test unless a docker arcaved answers.
func newDaemonClient(t *testing.T) *client.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping the cdc backend integration test in short mode")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	if _, err := cli.Ping(context.Background()); err != nil {
		_ = cli.Close()
		t.Skip("skipping the cdc backend integration test: no reachable docker arcaved: " + err.Error())
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// randomSuffix keeps concurrent copies of this test apart on one arcaved.
func randomSuffix(t *testing.T) string {
	t.Helper()

	b := make([]byte, 5)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// createVolume makes an empty named volume, removed with the test.
func createVolume(t *testing.T, cli *client.Client, name string) {
	t.Helper()

	_, err := cli.VolumeCreate(context.Background(), volume.CreateOptions{Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), name, true) })
}

// createLabelledContainer creates — never starts — a container carrying
// compose's own labels and mounting v, which is how arcave locates a volume's
// mount. Compose does not add those labels when it is called as a library
// either; arcave stamps them itself.
func createLabelledContainer(t *testing.T, cli *client.Client, service string, v cdcVolume) {
	t.Helper()

	created, err := cli.ContainerCreate(context.Background(),
		&container.Config{
			Image: busyboxImage,
			Cmd:   []string{"sh", "-c", "sleep 3600"},
			Labels: map[string]string{
				"com.docker.compose.project": cdcProject,
				"com.docker.compose.service": service,
			},
		},
		&container.HostConfig{Binds: []string{v.name + ":" + v.target}},
		nil, nil, v.name+"-holder")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	})
}

// fill seeds a volume with random bytes in more than one shape, so more than one
// kind of entry crosses the boundary.
func fill(t *testing.T, engine *dockerclient.Engine, vol string) {
	t.Helper()

	busybox(t, engine, vol, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/blob bs=1k count=%d "+
			"&& mkdir -p /data/nested && dd if=/dev/urandom of=/data/nested/inner bs=1k count=64 "+
			"&& dd if=/dev/urandom of=/data/small bs=1k count=4", blobKiB))
}

// objectNames lists the tree under prefix, relative to it.
func objectNames(t *testing.T, store storages.Storager, prefix string) []string {
	t.Helper()

	objects, err := store.List(context.Background(), prefix)
	require.NoError(t, err)
	names := make([]string, 0, len(objects))
	for _, obj := range objects {
		names = append(names, obj.Name)
	}
	return names
}

// dirNames lists a store's immediate sub-directories — for the stack root, the
// savepoint directories plus the reserved neighbour cdc keeps its repositories
// in.
func dirNames(t *testing.T, store storages.Storager) []string {
	t.Helper()

	_, dirs, err := store.ListDir(context.Background())
	require.NoError(t, err)
	names := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		names = append(names, dir.Dirname())
	}
	return names
}

// busybox runs one shell line with vol at /data and returns its stdout.
func busybox(t *testing.T, engine *dockerclient.Engine, vol, script string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	err := engine.RunOneShot(context.Background(), dockermodel.OneShotSpec{
		Image:  busyboxImage,
		Cmd:    []string{"sh", "-c", script},
		Binds:  []string{vol + ":/data"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	require.NoErrorf(t, err, "busybox %q failed:\nstdout:\n%s\nstderr:\n%s", script, stdout.String(), stderr.String())
	return stdout.String()
}
