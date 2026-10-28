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

package cdc

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The plan a capture runs depends on the state of the repository, and every
// branch is decided server-side before a container starts: init only when the
// repository has never been written, locks cleared whenever any are standing.
func TestSnapshotter_Snapshot(t *testing.T) {
	tests := []struct {
		name string
		// plant is what the repository already holds when the capture starts.
		plant    map[string]string
		wantInit bool
	}{
		{
			name:     "an absent repository is initialised first",
			wantInit: true,
		},
		{
			name:  "a healthy repository is backed up straight away",
			plant: map[string]string{"config": "restic-config"},
		},
		{
			name: "a locked repository has its locks cleared, then is backed up",
			plant: map[string]string{
				"config":        "restic-config",
				"locks/abc123":  "orphan",
				"locks/def456":  "orphan",
				"snapshots/ff0": "an earlier capture",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, defaultOptions())
			ctx := zerolog.Nop().WithContext(context.Background())
			for key, body := range tt.plant {
				require.NoError(t, h.repo.PutObject(ctx, key, strings.NewReader(body)))
			}
			if tt.wantInit {
				h.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
			}
			h.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
				Run(respondWith(backupStatusLine, backupSummaryLine)).Return(nil).Once()

			// Act
			stat, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, model.SnapshotBackendCDC, stat.Type)
			assert.Equal(t, Payload{SnapshotObjectID: "3f8a1c0d9b"}, stat.Payload)
			assert.Equal(t, int64(10485760), stat.SizeBytes, "the volume's logical size")
			assert.Equal(t, int64(524288), stat.StoredBytes, "what the repository grew by, packed")

			// Whatever the state, the locks are gone and nothing else was touched.
			assert.Empty(t, objectsUnder(t, h.repo, locksPrefix), "every standing lock is an orphan by construction")
			if _, planted := tt.plant["snapshots/ff0"]; planted {
				assert.NotEmpty(t, objectsUnder(t, h.repo, snapshotsPath), "clearing locks must not touch snapshots")
			}
			h.runner.AssertExpectations(t)
		})
	}
}

// The backup command line is the contract with restic: the tags that make a
// capture findable, the --host and path that make the next one an increment,
// and a read-only mount, since a backup has no business writing.
func TestSnapshotter_Snapshot_CommandLine(t *testing.T) {
	// Arrange
	h := newHarness(t, defaultOptions())
	h.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
	h.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(respondWith(backupSummaryLine)).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{
		SnapshotID: "019e843e-1650-7000-8000-000000000000",
		Labels:     map[string]string{"team": "payments", "arcave-safety-snapshot": "true"},
	})

	// Assert
	require.NoError(t, err)
	specs := h.runner.Specs()
	require.Len(t, specs, 2)

	assert.Equal(t, []string{"init", "--json"}, specs[0].Cmd)
	assert.Empty(t, specs[0].Binds, "init reaches the repository, never the volume")

	backup := specs[1]
	// Labels are rendered in sorted key order, so a command line is the same on
	// every run.
	assert.Equal(t, []string{
		"backup", demoMounted,
		"--host", demoProject,
		"--tag", "arcave-savepoint=019e843e-1650-7000-8000-000000000000",
		"--tag", "arcave-safety-snapshot=true",
		"--tag", "team=payments",
		"--json",
	}, backup.Cmd)
	assert.Equal(t, []string{demoVolName + ":" + demoMounted + ":ro"}, backup.Binds)

	// Every run of one instance addresses one repository, at the volume's own
	// ObjectID under the base the DI root handed down.
	for i, spec := range specs {
		assert.Equal(t, demoImage, spec.Image, "run %d", i)
		assert.ElementsMatch(t,
			[]string{"RESTIC_REPOSITORY=" + demoRepoURL, "RESTIC_PASSWORD=" + resticPassword},
			spec.Env, "run %d", i)
		assert.False(t, spec.HostNetwork, "run %d: the default transport is the bridge", i)
	}
	h.runner.AssertExpectations(t)
}

// The transport the DI root resolved reaches every restic container.
func TestSnapshotter_Snapshot_HostNetwork(t *testing.T) {
	// Arrange
	opts := defaultOptions()
	opts.HostNetwork = true
	h := newHarness(t, opts)
	h.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
	h.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(respondWith(backupSummaryLine)).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})

	// Assert
	require.NoError(t, err)
	specs := h.runner.Specs()
	require.Len(t, specs, 2)
	for i, spec := range specs {
		assert.True(t, spec.HostNetwork, "run %d", i)
	}
	h.runner.AssertExpectations(t)
}

// A safety snapshot differs from a savepoint by its label and by nothing else:
// same repository, same --host, same path. That is what makes it an increment
// over the savepoints of that volume, since restic picks a parent by host and
// paths — and the label is what makes one a killed rollback abandoned findable
// with a plain `restic snapshots`.
func TestSnapshotter_Snapshot_SafetySnapshotDiffersOnlyByItsLabel(t *testing.T) {
	// Arrange
	savepoint := newHarness(t, defaultOptions())
	savepoint.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
	savepoint.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(respondWith(backupSummaryLine)).Return(nil).Once()
	safety := newHarness(t, defaultOptions())
	safety.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
	safety.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(respondWith(backupSummaryLine)).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := savepoint.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})
	require.NoError(t, err)
	_, err = safety.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{
		SnapshotID: "019e843e-1a38-7000-8000-000000000000",
		Labels:     map[string]string{"arcave-safety-snapshot": "true"},
	})
	require.NoError(t, err)

	// Assert
	assert.Equal(t, []string{"backup", demoMounted, "--host", demoProject,
		"--tag", "arcave-savepoint=019e843e-1650-7000-8000-000000000000", "--json"}, savepoint.runner.Specs()[1].Cmd)
	assert.Equal(t, []string{"backup", demoMounted, "--host", demoProject,
		"--tag", "arcave-savepoint=019e843e-1a38-7000-8000-000000000000", "--tag", "arcave-safety-snapshot=true", "--json"},
		safety.runner.Specs()[1].Cmd,
		"host and path are what restic picks a parent by, so the two name the same ones")
	assert.Contains(t, savepoint.runner.Specs()[1].Env, "RESTIC_REPOSITORY="+demoRepoURL)
	assert.Contains(t, safety.runner.Specs()[1].Env, "RESTIC_REPOSITORY="+demoRepoURL,
		"a safety snapshot addresses the very repository the savepoints live in")
	savepoint.runner.AssertExpectations(t)
	safety.runner.AssertExpectations(t)
}

// A volume no service mounts in full is captured in full anyway: the backend
// binds the volume itself, so a service's subpath never reaches the bind, the
// snapshot path, or the --include a restore is scoped by. This is what the
// container-addressed backend could not do, and why it stopped being one.
func TestSnapshotter_Snapshot_CapturesASubpathMountedVolumeWhole(t *testing.T) {
	// Arrange
	vol := demoVolume()
	vol.Named.UsedBy = []model.ServiceReference{
		{Service: demoService, Target: demoTarget, Subpath: "pgdata"},
	}
	runner := &mockRunner{}
	runner.On("ResolveMountSource", mock.Anything, vol).Return(demoAddress, nil).Once()
	runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
	runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(respondWith(backupSummaryLine)).Return(nil).Once()

	snapshotter, err := NewFactory(Dependencies{
		Runner: runner, RepoURLBase: demoRepoURLBase,
		RepositoryRoot: newStore(t), Options: defaultOptions(),
	}).New(model.BackendSpec{Volume: vol})
	require.NoError(t, err)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err = snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})

	// Assert
	require.NoError(t, err)
	backup := runner.Specs()[1]
	assert.Equal(t, []string{demoVolName + ":" + demoMounted + ":ro"}, backup.Binds,
		"the bind is the volume, never the subpath a service happens to see")
	assert.Equal(t, []string{"backup", demoMounted, "--host", demoProject,
		"--tag", "arcave-savepoint=019e843e-1650-7000-8000-000000000000", "--json"}, backup.Cmd)
	for _, arg := range backup.Cmd {
		assert.NotContains(t, arg, "pgdata/", "no subpath may narrow what the capture covers")
	}
	runner.AssertExpectations(t)
}

// The backend is handed a URL base and appends the volume's own ObjectID — no
// path and no server object cross the seam. The trailing slash is the restic
// protocol's, and its loss is the failure this test exists for: nothing would
// report it, restic would simply talk to the wrong path.
func TestSnapshotter_newRunner(t *testing.T) {
	// Arrange
	backend, err := NewFactory(Dependencies{RepoURLBase: demoRepoURLBase, RepositoryRoot: newStore(t)}).
		New(model.BackendSpec{Volume: demoVolume()})
	require.NoError(t, err)
	snapshotter, ok := backend.(*Snapshotter)
	require.True(t, ok)

	// Act
	resticRunner, err := snapshotter.newRunner()

	// Assert
	require.NoError(t, err)
	got := resticRunner.repoURL
	assert.Equal(t, demoRepoURL, got)
	parsed, err := url.Parse(strings.TrimPrefix(got, "rest:"))
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(parsed.Path, "/"), "path = %q", parsed.Path)
}

// Restore is one pass in incremental mode and two in wipe mode; either way the
// mount is writable and the restore is scoped to it, since --target / is what
// puts the snapshot's absolute paths back where they came from.
func TestSnapshotter_Restore(t *testing.T) {
	tests := []struct {
		name     string
		opts     Options
		wantWipe bool
		wantCmd  []string
	}{
		{
			name:     "wipe clears the mount, then restores it whole",
			opts:     defaultOptions(),
			wantWipe: true,
			wantCmd: []string{"restore", "3f8a1c0d9b", "--target", "/",
				"--include", demoMounted, "--json"},
		},
		{
			name: "incremental restores over the live data in one pass",
			opts: Options{Image: demoImage, Incremental: true, Overwrite: "always"},
			wantCmd: []string{"restore", "3f8a1c0d9b", "--target", "/",
				"--include", demoMounted, "--delete", "--overwrite", "always", "--json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, tt.opts)
			restore := h.runner.On("RunOneShot", mock.Anything, matchRestic("restore")).
				Run(respondWith(restoreSummaryLine)).Return(nil).Once()
			if tt.wantWipe {
				// The wipe is a container of its own and must finish before restic
				// writes a byte; reversed, the restore would be deleted by its own wipe.
				wipe := h.runner.On("WipeVolume", mock.Anything, demoAddress).Return(nil).Once()
				mock.InOrder(wipe, restore)
			}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
				SnapshotID: "019e843e-1650-7000-8000-000000000000",
				Payload:    Payload{SnapshotObjectID: "3f8a1c0d9b"},
			})

			// Assert
			require.NoError(t, err)
			specs := h.runner.Specs()
			require.Len(t, specs, 1)
			assert.Equal(t, tt.wantCmd, specs[0].Cmd)
			assert.Equal(t, []string{demoVolName + ":" + demoMounted}, specs[0].Binds,
				"a restore writes, so the mount is not read-only")
			// In incremental mode no WipeMount expectation is declared, so a wipe
			// would fail the run where it happened.
			h.runner.AssertExpectations(t)
		})
	}
}

// A record that has been through the store carries its payload back as a
// map[string]any — SnapshotStat.Payload is `any` — so the restore has to read
// that shape as readily as the struct it wrote.
func TestSnapshotter_Restore_ReadsARoundTrippedPayload(t *testing.T) {
	// Arrange
	h := newHarness(t, Options{Image: demoImage, Incremental: true, Overwrite: "always"})
	h.runner.On("RunOneShot", mock.Anything, matchRestic("restore")).
		Run(respondWith(restoreSummaryLine)).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	err := h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
		SnapshotID: "019e843e-1650-7000-8000-000000000000",
		Payload:    map[string]any{"restic_snapshot_id": "3f8a1c0d9b"},
	})

	// Assert
	require.NoError(t, err)
	require.Len(t, h.runner.Specs(), 1)
	assert.Contains(t, h.runner.Specs()[0].Cmd, "3f8a1c0d9b")
	h.runner.AssertExpectations(t)
}

// Remove leaves the snapshot to a gc: nothing in the repository goes, and no container runs.
func TestSnapshotter_Remove(t *testing.T) {
	// Arrange
	h := newHarness(t, defaultOptions())
	ctx := zerolog.Nop().WithContext(context.Background())
	require.NoError(t, h.repo.PutObject(ctx, "snapshots/3f8a1c0d9b", strings.NewReader("snapshot")))

	// Act
	err := h.snapshotter.Remove(ctx, model.BackendRemoveRequest{
		SnapshotID: "019e843e-1650-7000-8000-000000000000",
		Payload:    Payload{SnapshotObjectID: "3f8a1c0d9b"},
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{"3f8a1c0d9b"}, objectsUnder(t, h.repo, snapshotsPath))
	h.runner.AssertExpectations(t)
}

// A failure of the run reaches the caller with its cause: the operations are
// what a whole workflow's error is attributed by, and a restic run that printed
// no summary is as much a failure as one that exited non-zero.
func TestSnapshotter_ReportsRunFailures(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(h *harness)
		act     func(ctx context.Context, h *harness) error
		wantErr string
	}{
		{
			name: "a failed backup",
			arrange: func(h *harness) {
				h.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
				h.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
					Return(errors.New("backup boom")).Once()
			},
			act: func(ctx context.Context, h *harness) error {
				_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})
				return err
			},
			wantErr: "backup boom",
		},
		{
			name: "a backup that printed no summary",
			arrange: func(h *harness) {
				h.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
				h.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
					Run(respondWith(backupStatusLine)).Return(nil).Once()
			},
			act: func(ctx context.Context, h *harness) error {
				_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "019e843e-1650-7000-8000-000000000000"})
				return err
			},
			wantErr: "no restic snapshot id",
		},
		{
			name: "a failed restore",
			arrange: func(h *harness) {
				h.runner.On("WipeVolume", mock.Anything, demoAddress).Return(nil).Once()
				h.runner.On("RunOneShot", mock.Anything, matchRestic("restore")).
					Return(errors.New("restore boom")).Once()
			},
			act: func(ctx context.Context, h *harness) error {
				return h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
					SnapshotID: "019e843e-1650-7000-8000-000000000000",
					Payload:    Payload{SnapshotObjectID: "3f8a1c0d9b"},
				})
			},
			wantErr: "restore boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, defaultOptions())
			tt.arrange(h)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.act(ctx, h)

			// Assert
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			h.runner.AssertExpectations(t)
		})
	}
}

// The savepoint id becomes a storage key, so it is gated by an allowlist ahead
// of every side effect; a payload naming no snapshot has nothing to restore or
// drop. The empty runner is the assertion: neither reaches a container.
func TestSnapshotter_RejectsUnusableIDs(t *testing.T) {
	tests := []struct {
		name string
		act  func(ctx context.Context, h *harness) error
	}{
		{
			name: "a savepoint id that is not digits never reaches a capture",
			act: func(ctx context.Context, h *harness) error {
				_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "../../etc"})
				return err
			},
		},
		{
			name: "a payload naming no snapshot never reaches a restore",
			act: func(ctx context.Context, h *harness) error {
				return h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
					SnapshotID: "019e843e-1650-7000-8000-000000000000",
					Payload:    Payload{SnapshotObjectID: ""},
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, defaultOptions())
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.act(ctx, h)

			// Assert
			require.Error(t, err)
			h.runner.AssertExpectations(t)
		})
	}
}
