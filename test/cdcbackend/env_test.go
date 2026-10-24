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

// Package cdcbackend drives the cdc backend one contract call at a time: one
// volume, one instance, a real daemon, stock restic containers and the real
// endpoint in front of the real repository root. What only this level buys is
// the repository in view — the compose project store is inmemory, so a case lists the
// keys restic actually wrote.
// Guarded twice: -short skips, and so does an unreachable daemon.
package cdcbackend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
)

const (
	// itProject is the compose project name the container labels carry; the
	// backend finds a volume's mount through them.
	itProject = "arcave-cdc-vol-it"
	// busyboxImage fills, mutates and hashes the volumes.
	busyboxImage = "busybox"
	// blobKiB is the payload per volume, in random bytes — zeros would deduplicate
	// into a repository proving nothing. 16 MiB rather than 4: restic averages
	// 1 MiB per chunk, and at 4 MiB a 128 KiB edit reshipped 2.8 of 4.2 MB.
	blobKiB = 16384
	// mutateKiB is what changes between two captures: the shape of a migration.
	mutateKiB = 128
	// resticPassword is the constant every arcave repository uses; a stock container
	// opening one with it is part of the contract.
	resticPassword = "arcave"
)

// The ids the cases capture under — a counter, not the clock, so a failure names
// the same id on every run.
const (
	idFirst  = model.SavepointID("019e7044-8be8-7000-8000-000000000000")
	idSecond = model.SavepointID("019e7044-8fd0-7000-8000-000000000000")
)

// The repository objects the assertions name — restic's own layout.
const (
	configKey     = "config"
	locksPrefix   = "locks"
	snapshotsPath = "snapshots"
	dataPrefix    = "data"
)

// env is one cdc factory over a live daemon and an observable store.
type env struct {
	ctx    context.Context
	cli    *client.Client
	engine *dockerclient.Engine
	// hostNetwork is the transport the probe picked; the oracle's own restic
	// containers must reach the server the same way the backend's do.
	hostNetwork bool
	// repoURLBase is what the DI root hands a backend: the served endpoint as a
	// container names it, with a volume's ObjectID still to append.
	repoURLBase         string
	composeProjectStore storages.Storager
	// resticRoot is the tree the repositories live in — the one the server
	// resolves against and the one the factory writes into, exactly as the DI
	// root wires it.
	resticRoot storages.Storager
	factory    *cdc.Factory
	opts       cdc.Options
}

// newEnv wires the backend as cmd/arcave does, with the compose project store in memory:
// the restic server is started once here and lives for the whole case, exactly as
// the DI root keeps it for the whole process.
func newEnv(t *testing.T, opts cdc.Options) *env {
	t.Helper()

	cli := newDaemonClient(t)
	// restic's own output goes to the logger; it is worth reading only when a case
	// failed, and reading it then is most of the diagnosis.
	logs := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("backend log:\n%s", logs.String())
		}
	})
	composeProjectStore := inmemory.New("compose-project")
	t.Cleanup(func() { assert.NoError(t, composeProjectStore.Close()) })

	ctx := zerolog.New(logs).Level(zerolog.DebugLevel).WithContext(context.Background())
	resticRoot, err := composeProjectStore.SubStorage(cdc.NamespaceRepos, true)
	require.NoError(t, err)
	server := rest.NewEcho(ctx, repo.NewResolver(resticRoot))
	port, err := rest.Serve(server, "")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, server.Close()) })
	engine := dockerclient.NewEngine(cli)
	hostNetwork, containerHost := transport(t, engine)
	opts.HostNetwork = hostNetwork

	e := &env{
		ctx:                 ctx,
		cli:                 cli,
		engine:              engine,
		hostNetwork:         hostNetwork,
		repoURLBase:         rest.RepositoryURLBase("", containerHost, port),
		composeProjectStore: composeProjectStore,
		resticRoot:          resticRoot,
		opts:                opts,
	}
	e.factory = cdc.NewFactory(cdc.Dependencies{
		Runner:         engine,
		RepoURLBase:    e.repoURLBase,
		RepositoryRoot: resticRoot,
		Options:        opts,
	})
	return e
}

// syncBuffer collects the log of one case. The endpoint serves its requests
// concurrently, so a bare bytes.Buffer would be written from several goroutines
// at once — zerolog synchronises nothing on the writer's behalf.
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

// wipeOptions is the default: a restore clears the mount and restores it whole.
func wipeOptions() cdc.Options {
	return cdc.Options{Image: config.DefaultResticImage, Overwrite: config.OverwriteIfChanged}
}

// incrementalOptions restores over the live data instead.
func incrementalOptions() cdc.Options {
	return cdc.Options{
		Image:       config.DefaultResticImage,
		Incremental: true,
		Overwrite:   config.OverwriteIfChanged,
	}
}

// newVolume creates a filled named volume plus the labelled container that mounts
// it. The model.Volume is assembled by hand: this layer sits below the introspector.
func (e *env) newVolume(t *testing.T, service, target string) model.Volume {
	t.Helper()

	name := e.newFilledVolume(t, service)
	createLabelledContainer(t, e.cli, service, name, target)
	return namedVolume(name, model.ServiceReference{Service: service, Target: target})
}

// newBareVolume creates a filled named volume with no container mounting it at
// all — what a `docker compose down` leaves behind, and what a backend
// addressing the volume by name still reaches.
func (e *env) newBareVolume(t *testing.T, service, target string) model.Volume {
	t.Helper()

	name := e.newFilledVolume(t, service)
	return namedVolume(name, model.ServiceReference{Service: service, Target: target})
}

// newSubpathVolume creates a filled named volume whose only container sees a
// subdirectory of it. The subdirectory has to exist before the container is
// created — the daemon refuses a subpath that is not there — so the fill runs
// first.
func (e *env) newSubpathVolume(t *testing.T, service, target, subpath string) model.Volume {
	t.Helper()

	name := e.newFilledVolume(t, service)
	createSubpathContainer(t, e.cli, service, name, target, subpath)
	return namedVolume(name, model.ServiceReference{Service: service, Target: target, Subpath: subpath})
}

// newFilledVolume creates one named volume and seeds it.
func (e *env) newFilledVolume(t *testing.T, service string) string {
	t.Helper()

	name := itProject + "-" + service + "-" + randomSuffix(t)
	createVolume(t, e.cli, name)
	e.fill(t, name)
	return name
}

// namedVolume is the unit the backend is built for, assembled by hand.
func namedVolume(name string, usedBy ...model.ServiceReference) model.Volume {
	return model.Volume{
		ObjectID:       "volume-named-" + name,
		ComposeProject: itProject,
		Type:           model.VolumeTypeNamed,
		Backend:        model.SnapshotBackendCDC,
		Named:          &model.VolumePropertiesNamed{VolumeName: name, UsedBy: usedBy},
	}
}

// backend builds the instance under test, as the registry does. The spec carries
// no store on purpose: a cdc repository lives outside whatever tree a workflow
// hands over, so a backend that starts reading spec.Store fails here loudly.
func (e *env) backend(t *testing.T, vol model.Volume) model.VolumeSnapshotBackend {
	t.Helper()

	backend, err := e.factory.New(model.BackendSpec{Volume: vol})
	require.NoError(t, err)
	return backend
}

// snapshot captures the volume and returns the payload the backend reported.
func (e *env) snapshot(
	t *testing.T, backend model.VolumeSnapshotBackend, id model.SavepointID, labels map[string]string,
) cdc.Payload {
	t.Helper()

	stat, err := backend.Snapshot(e.ctx, model.BackendSnapshotRequest{SnapshotID: id, Labels: labels})
	require.NoError(t, err)
	require.Equal(t, model.SnapshotBackendCDC, stat.Type)

	payload, ok := stat.Payload.(cdc.Payload)
	require.Truef(t, ok, "stat for %s carries no cdc payload: %T", stat.Volume.ObjectID, stat.Payload)
	return payload
}

// repoStore re-roots the repository root at one volume's repository, as the
// factory does — and as the server resolves a name on the wire.
func (e *env) repoStore(t *testing.T, vol model.Volume) storages.Storager {
	t.Helper()

	store, err := e.resticRoot.SubStorage(vol.ObjectID, true)
	require.NoError(t, err)
	return store
}

// repoKeys lists a repository's whole tree, relative to its root.
func (e *env) repoKeys(t *testing.T, vol model.Volume) []string {
	t.Helper()

	return objectNames(t, e.repoStore(t, vol), "")
}

// snapshotObjectIDs is the snapshot objects the repository holds, one per capture.
func (e *env) snapshotObjectIDs(t *testing.T, vol model.Volume) []string {
	t.Helper()

	return objectNames(t, e.repoStore(t, vol), snapshotsPath)
}

// oracleSnapshot is what a stock restic container reports about a snapshot.
// Snapshot objects are encrypted, so this is the only view of the tags.
type oracleSnapshot struct {
	ID       string   `json:"id"`
	Hostname string   `json:"hostname"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags"`
}

// oracleSnapshots reads the repository with `restic snapshots --json` — an
// independent reader of what arcave believes it wrote. It addresses the very
// name the backend does: with no registration to make, "reachable" is a
// property of the tree the server was given.
func (e *env) oracleSnapshots(t *testing.T, vol model.Volume) []oracleSnapshot {
	t.Helper()

	var stdout, stderr bytes.Buffer
	err := e.engine.RunOneShot(e.ctx, model.OneShotSpec{
		Image: e.opts.Image,
		Cmd:   []string{"snapshots", "--json"},
		Env: []string{
			"RESTIC_REPOSITORY=" + e.repoURLBase + "/" + vol.ObjectID + "/",
			"RESTIC_PASSWORD=" + resticPassword,
		},
		HostNetwork: e.hostNetwork,
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	require.NoErrorf(t, err, "restic snapshots failed:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())

	var snapshots []oracleSnapshot
	require.NoErrorf(t, json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &snapshots),
		"restic snapshots --json printed %q", stdout.String())
	return snapshots
}

// hash is a sorted sha256 listing of every file in the volume: content and file
// set in one string, so a leftover file changes it.
func (e *env) hash(t *testing.T, vol model.Volume) string {
	t.Helper()

	out := busybox(t, e.engine, vol.Named.VolumeName, "find /data -type f -exec sha256sum {} + | sort")
	require.NotEmpty(t, out, "volume %s must hold files to hash", vol.Named.VolumeName)
	return out
}

// fill seeds a volume with random bytes in more than one shape.
func (e *env) fill(t *testing.T, name string) {
	t.Helper()

	busybox(t, e.engine, name, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/blob bs=1k count=%d "+
			"&& mkdir -p /data/nested && dd if=/dev/urandom of=/data/nested/inner bs=1k count=64 "+
			"&& dd if=/dev/urandom of=/data/small bs=1k count=4", blobKiB))
}

// mutate overwrites mutateKiB mid-blob, leaving its size alone.
func (e *env) mutate(t *testing.T, vol model.Volume) {
	t.Helper()

	busybox(t, e.engine, vol.Named.VolumeName, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/blob bs=1k count=%d seek=%d conv=notrunc", mutateKiB, blobKiB/3))
}

// addFile writes a file no snapshot carries — what a restore has to take away.
func (e *env) addFile(t *testing.T, vol model.Volume, name string) {
	t.Helper()

	busybox(t, e.engine, vol.Named.VolumeName, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/%s bs=1k count=32", name))
}

// putObject plants a precondition the backend would never write itself.
func putObject(t *testing.T, store storages.Storager, key, body string) {
	t.Helper()

	require.NoError(t, store.PutObject(context.Background(), key, bytes.NewBufferString(body)))
}

// objectNames lists a prefix's tree, sorted, with names relative to it.
func objectNames(t *testing.T, store storages.Storager, prefix string) []string {
	t.Helper()

	objects, err := store.List(context.Background(), prefix)
	require.NoError(t, err)
	names := make([]string, 0, len(objects))
	for _, obj := range objects {
		names = append(names, obj.Name)
	}
	sort.Strings(names)
	return names
}

// newDaemonClient skips the test unless a docker daemon answers.
func newDaemonClient(t *testing.T) *client.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping the cdc backend volume-level test in short mode")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	if _, err := cli.Ping(context.Background()); err != nil {
		_ = cli.Close()
		t.Skip("skipping the cdc backend volume-level test: no reachable docker daemon: " + err.Error())
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// transport resolves how this daemon's containers reach the test's server,
// exactly as the DI root's auto mode does.
func transport(t *testing.T, engine *dockerclient.Engine) (hostNetwork bool, containerHost string) {
	t.Helper()

	shared, err := engine.HostLoopbackShared(context.Background())
	require.NoError(t, err)
	if shared {
		return true, "127.0.0.1"
	}
	return false, "host.docker.internal"
}

// randomSuffix keeps concurrent copies of these tests apart on one daemon.
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

// createLabelledContainer creates — never starts — a container carrying compose's
// own labels, which is how the backend locates the mount.
func createLabelledContainer(t *testing.T, cli *client.Client, service, name, target string) {
	t.Helper()

	created, err := cli.ContainerCreate(context.Background(),
		&container.Config{
			Image: busyboxImage,
			Cmd:   []string{"sh", "-c", "sleep 3600"},
			Labels: map[string]string{
				"com.docker.compose.project": itProject,
				"com.docker.compose.service": service,
			},
		},
		&container.HostConfig{Binds: []string{name + ":" + target}},
		nil, nil, name+"-holder")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	})
}

// createSubpathContainer is createLabelledContainer for a container that sees
// only a subdirectory of the volume. Subpath lives in HostConfig.Mounts and
// nowhere else: the daemon does not report it back on the container's .Mounts,
// so nothing but the compose file ever knows about it.
func createSubpathContainer(t *testing.T, cli *client.Client, service, name, target, subpath string) {
	t.Helper()

	created, err := cli.ContainerCreate(context.Background(),
		&container.Config{
			Image: busyboxImage,
			Cmd:   []string{"sh", "-c", "sleep 3600"},
			Labels: map[string]string{
				"com.docker.compose.project": itProject,
				"com.docker.compose.service": service,
			},
		},
		&container.HostConfig{Mounts: []mount.Mount{{
			Type:          mount.TypeVolume,
			Source:        name,
			Target:        target,
			VolumeOptions: &mount.VolumeOptions{Subpath: subpath},
		}}},
		nil, nil, name+"-holder")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	})
}

// busybox runs one shell line with the volume at /data and returns its stdout.
func busybox(t *testing.T, engine *dockerclient.Engine, name, script string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	err := engine.RunOneShot(context.Background(), model.OneShotSpec{
		Image:  busyboxImage,
		Cmd:    []string{"sh", "-c", script},
		Binds:  []string{name + ":/data"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	require.NoErrorf(t, err, "busybox %q failed:\nstdout:\n%s\nstderr:\n%s", script, stdout.String(), stderr.String())
	return stdout.String()
}
