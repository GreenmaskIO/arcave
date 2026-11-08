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

package cdcbackend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
)

const (
	// slowKiB is a volume large enough that its backup or restore is still
	// running when the test strikes.
	slowKiB = 512 << 10
	// strikeTimeout bounds the wait for the run to strike at.
	strikeTimeout = 2 * time.Minute
	// cancelGrace is how long a cancelled run may take to end.
	cancelGrace = 15 * time.Second
	// inflightPacks is how many packs restic uploads at once (its default
	// rclone.connections); one more listed than that is one finished.
	inflightPacks = 5
)

// A restic killed mid-pack fails the capture with its exit and leaves no
// container; a prune drops the packs it left, and a later capture succeeds.
func TestSnapshotter_Snapshot_Stdio_KilledMidBackup(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newSlowVolume(t, "db")
	backend := env.backend(t, vol)
	repoStore := env.repoStore(t, vol)
	strikeCtx, stopStrike := context.WithTimeout(env.ctx, strikeTimeout)
	defer stopStrike()
	killed := make(chan error, 1)
	go func() {
		id, err := waitForRestic(strikeCtx, env.cli, vol)
		if err == nil {
			err = waitForPacks(strikeCtx, repoStore)
		}
		if err == nil {
			_, err = env.cli.ContainerKill(context.Background(), id, client.ContainerKillOptions{Signal: "KILL"})
		}
		killed <- err
	}()

	// Act
	_, err := backend.Snapshot(env.ctx, model.BackendSnapshotRequest{SnapshotID: idFirst})
	stopStrike()

	// Assert
	require.NoError(t, <-killed, "the backup must have been killed once its first pack landed")
	var exitErr *model.ContainerExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, int64(137), exitErr.StatusCode)
	assert.Empty(t, oneShotsOn(t, env.cli, vol), "a killed run must take its container with it")
	assert.Empty(t, env.oracleSnapshots(t, vol), "a killed backup commits no snapshot")

	require.NotEmpty(t, objectNames(t, repoStore, dataPrefix), "the killed run left packs behind")
	_, err = cdc.NewPruner(env.engine, env.resticRoot, env.transport, env.opts).Forget(env.ctx, vol.ObjectID, nil)
	require.NoError(t, err)
	assert.Empty(t, objectNames(t, repoStore, dataPrefix), "a prune drops the packs no snapshot references")

	env.snapshot(t, backend, idSecond, nil)
	assert.Len(t, env.oracleSnapshots(t, vol), 1, "a later capture must recover the repository")
}

// A cancelled capture ends promptly and removes its container: the exec stream
// and the server on it are torn down with the run.
func TestSnapshotter_Snapshot_Stdio_CancelledMidBackup(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newSlowVolume(t, "db")
	backend := env.backend(t, vol)
	ctx, cancel := context.WithCancel(env.ctx)
	defer cancel()
	cancelled := cancelOnRestic(t, ctx, cancel, env.cli, vol)

	// Act
	_, err := backend.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: idFirst})

	// Assert
	assertCancelledPromptly(t, cancelled, err)
	assert.Empty(t, oneShotsOn(t, env.cli, vol), "a cancelled run must take its container with it")
}

// A cancelled restore ends promptly and removes its container, as a capture does.
func TestSnapshotter_Restore_Stdio_CancelledMidRestore(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newSlowVolume(t, "db")
	backend := env.backend(t, vol)
	payload := env.snapshot(t, backend, idFirst, nil)
	ctx, cancel := context.WithCancel(env.ctx)
	defer cancel()
	cancelled := cancelOnRestic(t, ctx, cancel, env.cli, vol)

	// Act
	err := backend.Restore(ctx, model.BackendRestoreRequest{SnapshotID: idFirst, Payload: payload})

	// Assert
	assertCancelledPromptly(t, cancelled, err)
	assert.Empty(t, oneShotsOn(t, env.cli, vol), "a cancelled run must take its container with it")
}

// Volumes captured at once share one stream server, each over its own stream.
func TestSnapshotter_Snapshot_Stdio_Concurrent(t *testing.T) {
	t.Parallel()

	// Arrange
	const volumes = 5
	env := newEnv(t, wipeOptions())
	vols := make([]model.Volume, volumes)
	for i := range vols {
		vols[i] = env.newVolume(t, fmt.Sprintf("db%d", i), "/var/lib/postgresql/data")
	}

	// Act
	var eg errgroup.Group
	for _, vol := range vols {
		backend := env.backend(t, vol)
		eg.Go(func() error {
			_, err := backend.Snapshot(env.ctx, model.BackendSnapshotRequest{SnapshotID: idFirst})
			return err
		})
	}
	err := eg.Wait()

	// Assert
	require.NoError(t, err)
	for _, vol := range vols {
		assert.Len(t, env.oracleSnapshots(t, vol), 1, "volume %s", vol.Named.VolumeName)
	}
}

// newSlowVolume is a named volume holding slowKiB of incompressible data.
func (e *env) newSlowVolume(t *testing.T, service string) model.Volume {
	t.Helper()

	name := itProject + "-" + service + "-" + randomSuffix(t)
	createVolume(t, e.cli, name)
	busybox(t, e.engine, name, fmt.Sprintf("dd if=/dev/urandom of=/data/blob bs=1k count=%d 2>/dev/null", slowKiB))
	return namedVolume(name, model.ServiceReference{Service: "db", Target: "/data"})
}

// cancelOnRestic cancels once a restic container mounting vol runs with its bridge
// exec started, so the cancel always meets a live stream, and delivers when it
// did; a run that never got there fails the test.
func cancelOnRestic(
	t *testing.T, ctx context.Context, cancel context.CancelFunc, cli *client.Client, vol model.Volume,
) <-chan time.Time {
	t.Helper()

	strikeCtx, stopStrike := context.WithTimeout(ctx, strikeTimeout)
	cancelled := make(chan time.Time, 1)
	go func() {
		defer stopStrike()
		id, err := waitForRestic(strikeCtx, cli, vol)
		if err == nil {
			err = waitForExec(strikeCtx, cli, id)
		}
		at := time.Now()
		cancel()
		if err != nil {
			t.Errorf("no restic container to cancel: %v", err)
		}
		cancelled <- at
	}()
	return cancelled
}

// assertCancelledPromptly asserts the run ended in the cancellation within
// cancelGrace of it.
func assertCancelledPromptly(t *testing.T, cancelled <-chan time.Time, err error) {
	t.Helper()

	ended := time.Now()
	at := <-cancelled
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, ended.Sub(at), cancelGrace, "a cancelled run must not wait out its restic")
}

// waitForRestic returns the running restic container mounting vol — a backup or
// a restore, since init mounts nothing and a wipe runs busybox.
func waitForRestic(ctx context.Context, cli *client.Client, vol model.Volume) (string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		running, err := cli.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).
			Add("label", "arcave.oneshot").
			Add("ancestor", wipeOptions().Image).
			Add("volume", vol.Named.VolumeName).
			Add("status", "running"),
		})
		if err != nil {
			return "", err
		}
		if len(running.Items) > 0 {
			return running.Items[0].ID, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// waitForExec returns once containerID has an exec started in it — the bridge.
func waitForExec(ctx context.Context, cli *client.Client, containerID string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspect, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if len(inspect.Container.ExecIDs) > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// waitForPacks returns once the repository holds a finished data pack: more
// than restic can have in flight.
func waitForPacks(ctx context.Context, repoStore storages.Storager) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		packs, err := repoStore.List(ctx, dataPrefix)
		if err != nil {
			return err
		}
		if len(packs) > inflightPacks {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// oneShotsOn lists every one-shot container, running or not, mounting vol.
func oneShotsOn(t *testing.T, cli *client.Client, vol model.Volume) []string {
	t.Helper()

	containers, err := cli.ContainerList(context.Background(), client.ContainerListOptions{
		All: true,
		Filters: make(client.Filters).
			Add("label", "arcave.oneshot").
			Add("volume", vol.Named.VolumeName),
	})
	require.NoError(t, err)
	ids := make([]string, 0, len(containers.Items))
	for _, c := range containers.Items {
		ids = append(ids, c.ID)
	}
	return ids
}
