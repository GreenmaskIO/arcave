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

// Package tarcopybackend drives the fullcopy backend against a real daemon: one
// volume, one instance, real containers, a real store. It is the only thing
// that proves the whole-volume archive is round: that docker cp against a
// container of arcave's own reads the volume, and that extracting the archive
// back at the container's root lands the data where it came from.
//
// Guarded twice: -short skips, and so does an unreachable daemon.
package tarcopybackend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
)

const (
	// itProject is the compose project name the container labels carry.
	itProject = "arcave-fullcopy-vol-it"
	// busyboxImage fills, mutates and hashes the volumes.
	busyboxImage = "busybox"
	// idFirst is the id every case captures under — a constant, not the clock, so
	// a failure names the same id on every run.
	idFirst = model.SavepointID("019e7044-8be8-7000-8000-000000000000")
)

// A capture is round: the volume comes back byte for byte, ownership and
// permissions included, and what was written after the capture is gone.
func TestSnapshotter_RoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		volume func(t *testing.T, e *env) model.Volume
	}{
		{
			name:   "a volume its service still mounts",
			volume: func(t *testing.T, e *env) model.Volume { return e.newVolume(t, "db", "/var/lib/postgresql/data") },
		},
		{
			// The case the volume-addressed engine exists for: no container to read
			// the volume through, and the capture works anyway.
			name:   "a volume no container mounts at all",
			volume: func(t *testing.T, e *env) model.Volume { return e.newBareVolume(t, "db") },
		},
		{
			// The subpath belongs to that one container and never to the volume, so
			// the archive holds the volume whole.
			name: "a volume whose only mount exposes a subdirectory",
			volume: func(t *testing.T, e *env) model.Volume {
				return e.newSubpathVolume(t, "db", "/var/lib/postgresql/data", "nested")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			e := newEnv(t)
			vol := tt.volume(t, e)
			backend := e.backend(t, vol)

			stat, err := backend.Snapshot(e.ctx, model.BackendSnapshotRequest{SnapshotID: idFirst})
			require.NoError(t, err)
			captured := e.hash(t, vol)
			require.Contains(t, captured, "/data/blob", "the volume must hold a file outside any subpath")

			payload, ok := stat.Payload.(fullcopy.Payload)
			require.True(t, ok, "the stat carries no fullcopy payload: %T", stat.Payload)
			assert.Positive(t, payload.SizeBytes, "an archive of a filled volume cannot be empty")

			e.mutate(t, vol)
			e.addFile(t, vol, "extra")
			require.NotEqual(t, captured, e.hash(t, vol), "the volume must actually diverge first")

			// Act
			err = backend.Restore(e.ctx, model.BackendRestoreRequest{SnapshotID: idFirst})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, captured, e.hash(t, vol),
				"the volume must hash as it did at capture time — content, file set and modes")
			assert.NotContains(t, e.hash(t, vol), "/data/extra", "the restore takes the added file away")
		})
	}
}

// A capture of a volume the daemon does not have is refused before anything
// creates it: an empty capture would wipe live data at the next rollback.
func TestSnapshotter_Snapshot_MissingVolume(t *testing.T) {
	// Arrange
	e := newEnv(t)
	vol := namedVolume(itProject + "-never-created-" + randomSuffix(t))

	// Act
	_, err := e.backend(t, vol).Snapshot(e.ctx, model.BackendSnapshotRequest{SnapshotID: idFirst})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Empty(t, objectNames(t, e.store), "nothing may be stored for a volume that is not there")
	_, inspectErr := e.cli.VolumeInspect(context.Background(), vol.Named.VolumeName)
	assert.Error(t, inspectErr, "the refused capture must not have created the volume it refused")
}

// env is one fullcopy factory over a live daemon and an observable store.
type env struct {
	ctx     context.Context
	cli     *client.Client
	engine  *dockerclient.Engine
	store   storages.Storager
	factory *fullcopy.Factory
}

func newEnv(t *testing.T) *env {
	t.Helper()

	cli := newDaemonClient(t)
	store := inmemory.New("session")
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	engine := dockerclient.NewEngine(cli)

	return &env{
		ctx:     zerolog.Nop().WithContext(context.Background()),
		cli:     cli,
		engine:  engine,
		store:   store,
		factory: fullcopy.NewFactory(engine),
	}
}

// backend builds the instance under test, as the registry does.
func (e *env) backend(t *testing.T, vol model.Volume) model.VolumeSnapshotBackend {
	t.Helper()

	backend, err := e.factory.New(model.BackendSpec{Store: e.store, Volume: vol})
	require.NoError(t, err)
	return backend
}

// newVolume creates a filled named volume plus the labelled container that
// mounts it.
func (e *env) newVolume(t *testing.T, service, target string) model.Volume {
	t.Helper()

	name := e.newFilledVolume(t, service)
	createLabelledContainer(t, e.cli, service, name, target)
	return namedVolume(name, model.ServiceReference{Service: service, Target: target})
}

// newBareVolume creates a filled named volume no container mounts.
func (e *env) newBareVolume(t *testing.T, service string) model.Volume {
	t.Helper()

	return namedVolume(e.newFilledVolume(t, service))
}

// newSubpathVolume creates a filled named volume whose only container sees a
// subdirectory of it. The subdirectory must exist before the container is
// created, so the fill runs first.
func (e *env) newSubpathVolume(t *testing.T, service, target, subpath string) model.Volume {
	t.Helper()

	name := e.newFilledVolume(t, service)
	createSubpathContainer(t, e.cli, service, name, target, subpath)
	return namedVolume(name, model.ServiceReference{Service: service, Target: target, Subpath: subpath})
}

func (e *env) newFilledVolume(t *testing.T, service string) string {
	t.Helper()

	name := itProject + "-" + service + "-" + randomSuffix(t)
	createVolume(t, e.cli, name)
	e.fill(t, name)
	return name
}

// namedVolume is the unit the backend is built for, assembled by hand: this
// layer sits below the introspector.
func namedVolume(name string, usedBy ...model.ServiceReference) model.Volume {
	return model.Volume{
		ObjectID:       "volume-named-" + name,
		ComposeProject: itProject,
		Type:           model.VolumeTypeNamed,
		Backend:        model.SnapshotBackendCopy,
		Named:          &model.VolumePropertiesNamed{VolumeName: name, UsedBy: usedBy},
	}
}

// fill seeds a volume in more than one shape, including a mode and an ownership
// an unprivileged host process could not reapply — which is the whole reason
// the archive is stored verbatim rather than extracted.
func (e *env) fill(t *testing.T, name string) {
	t.Helper()

	busybox(t, e.engine, name,
		"dd if=/dev/urandom of=/data/blob bs=1k count=256 "+
			"&& mkdir -p /data/nested && dd if=/dev/urandom of=/data/nested/inner bs=1k count=64 "+
			"&& echo secret > /data/nested/keyfile && chmod 600 /data/nested/keyfile "+
			"&& chown 999:999 /data/nested/keyfile "+
			"&& dd if=/dev/urandom of=/data/small bs=1k count=4")
}

// mutate overwrites part of the blob, leaving its size alone.
func (e *env) mutate(t *testing.T, vol model.Volume) {
	t.Helper()

	busybox(t, e.engine, vol.Named.VolumeName,
		"dd if=/dev/urandom of=/data/blob bs=1k count=32 seek=64 conv=notrunc")
}

// addFile writes a file no snapshot carries — what a restore has to take away.
func (e *env) addFile(t *testing.T, vol model.Volume, name string) {
	t.Helper()

	busybox(t, e.engine, vol.Named.VolumeName, fmt.Sprintf("dd if=/dev/urandom of=/data/%s bs=1k count=8", name))
}

// hash lists every file in the volume with its digest, mode and owner: content,
// file set and metadata in one string, so a leftover file or a lost mode
// changes it.
func (e *env) hash(t *testing.T, vol model.Volume) string {
	t.Helper()

	out := busybox(t, e.engine, vol.Named.VolumeName,
		"find /data -type f -exec sha256sum {} + | sort; "+
			`find /data -exec stat -c '%n %a %u:%g' {} + | sort`)
	require.NotEmpty(t, out, "volume %s must hold files to hash", vol.Named.VolumeName)
	return out
}

// objectNames lists a store's whole tree.
func objectNames(t *testing.T, store storages.Storager) []string {
	t.Helper()

	objects, err := store.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(objects))
	for _, obj := range objects {
		names = append(names, obj.Name)
	}
	return names
}

// newDaemonClient skips the test unless a docker daemon answers.
func newDaemonClient(t *testing.T) *client.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping the fullcopy backend volume-level test in short mode")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	if _, err := cli.Ping(context.Background()); err != nil {
		_ = cli.Close()
		t.Skip("skipping the fullcopy backend volume-level test: no reachable docker daemon: " + err.Error())
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
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

// createLabelledContainer creates — never starts — a container carrying
// compose's own labels.
func createLabelledContainer(t *testing.T, cli *client.Client, service, name, target string) {
	t.Helper()

	createHolder(t, cli, service, name, &container.HostConfig{Binds: []string{name + ":" + target}})
}

// createSubpathContainer is the same holder seeing only a subdirectory. Subpath
// lives in HostConfig.Mounts and nowhere else: the daemon does not report it
// back on the container's .Mounts.
func createSubpathContainer(t *testing.T, cli *client.Client, service, name, target, subpath string) {
	t.Helper()

	createHolder(t, cli, service, name, &container.HostConfig{Mounts: []mount.Mount{{
		Type:          mount.TypeVolume,
		Source:        name,
		Target:        target,
		VolumeOptions: &mount.VolumeOptions{Subpath: subpath},
	}}})
}

func createHolder(t *testing.T, cli *client.Client, service, name string, host *container.HostConfig) {
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
		host, nil, nil, name+"-holder")
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
