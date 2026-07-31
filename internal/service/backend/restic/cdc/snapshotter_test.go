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
			h := newHarness(t, model.BackendTierSavepoint, defaultOptions())
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
			stat, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "1780335122"})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, model.SnapshotBackendCDC, stat.Type)
			assert.Equal(t, Payload{
				SnapshotRef:         "3f8a1c0d9b",
				DataAdded:           1048576,
				TotalBytesProcessed: 10485760,
			}, stat.Payload)

			// Whatever the state, the locks are gone and nothing else was touched.
			assert.Empty(t, objectsUnder(t, h.repo, locksPrefix), "every standing lock is an orphan by construction")
			if _, planted := tt.plant["snapshots/ff0"]; planted {
				assert.NotEmpty(t, objectsUnder(t, h.repo, snapshotsPath), "clearing locks must not touch snapshots")
			}
			h.publisher.AssertNumberOfCalls(t, "Unregister", 1) // published for the operation only
			h.runner.AssertExpectations(t)
			h.publisher.AssertExpectations(t)
		})
	}
}

// The backup command line is the contract with restic: the tags that make a
// capture findable, the --host and path that make the next one an increment,
// and a read-only mount, since a backup has no business writing.
func TestSnapshotter_Snapshot_CommandLine(t *testing.T) {
	// Arrange
	h := newHarness(t, model.BackendTierSafety, defaultOptions())
	h.runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
	h.runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(respondWith(backupSummaryLine)).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{
		SnapshotID: "1780335122",
		Labels:     map[string]string{"team": "payments", "arcave-tier": "safety"},
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
		"--tag", "arcave-savepoint=1780335122",
		"--tag", "arcave-tier=safety",
		"--tag", "team=payments",
		"--json",
	}, backup.Cmd)
	assert.Equal(t, []string{demoVolName + ":" + demoMounted + ":ro"}, backup.Binds)

	// This harness is the safety tier, so the repository URL carries that tier's
	// publish prefix — the base is the same one every instance is handed.
	wantRepoURL := demoRepoURLBase + "/" + repositoryPrefix(model.BackendTierSafety, demoObjectID) + "/"
	for i, spec := range specs {
		assert.Equal(t, demoImage, spec.Image, "run %d", i)
		assert.ElementsMatch(t,
			[]string{"RESTIC_REPOSITORY=" + wantRepoURL, "RESTIC_PASSWORD=" + resticPassword},
			spec.Env, "run %d", i)
	}
	h.runner.AssertExpectations(t)
	h.publisher.AssertExpectations(t)
}

// Both tiers back the same volume up the same way, into the same repository:
// that is what makes a safety backup an increment over the savepoints, since
// restic picks a parent by host and paths. Only the publish prefix differs.
func TestSnapshotter_Snapshot_TiersShareTheRepository(t *testing.T) {
	tests := []struct {
		name       string
		tier       model.BackendTier
		wantPrefix string
	}{
		{name: "savepoint", tier: model.BackendTierSavepoint, wantPrefix: "savepoint-volume-demo_pgdata"},
		{name: "safety", tier: model.BackendTierSafety, wantPrefix: "safety-volume-demo_pgdata"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			shared := newStore(t)
			runner := &mockRunner{}
			runner.On("ResolveMountSource", mock.Anything, demoProject, demoService, demoTarget).
				Return(demoVolName, nil).Once()
			runner.On("RunOneShot", mock.Anything, matchRestic("init")).Return(nil).Once()
			runner.On("RunOneShot", mock.Anything, matchRestic("backup")).
				Run(respondWith(backupSummaryLine)).Return(nil).Once()

			// The repository is the volume's, not the tier's: both tiers register the
			// same root under different prefixes.
			wantRepo, err := shared.SubStorage(repoNamespace+"/"+demoObjectID, true)
			require.NoError(t, err)
			publisher := &mockPublisher{}
			publisher.On("Register", tt.wantPrefix, matchRepository(t, wantRepo)).Return(nil).Once()
			publisher.On("Unregister", tt.wantPrefix).Return().Once()

			snapshotter, err := NewFactory(Deps{
				Runner: runner, Publisher: publisher, RepoURLBase: demoRepoURLBase,
				SharedStore: shared, Options: defaultOptions(),
			}).New(model.BackendSpec{Volume: demoVolume(), Tier: tt.tier})
			require.NoError(t, err)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err = snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "1780335122"})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []string{"backup", demoMounted, "--host", demoProject,
				"--tag", "arcave-savepoint=1780335122", "--json"}, runner.Specs()[1].Cmd,
				"host and path are what restic picks a parent by, so both tiers name the same ones")
			// Only the publish prefix tells the two instances apart, and it is the
			// only part of the URL that differs.
			assert.Contains(t, runner.Specs()[1].Env, "RESTIC_REPOSITORY="+demoRepoURLBase+"/"+tt.wantPrefix+"/")
			runner.AssertExpectations(t)
			publisher.AssertExpectations(t)
		})
	}
}

// The backend is handed a URL base and appends its own prefix — no path and no
// server object cross the seam. The trailing slash is the restic protocol's, and
// its loss is the failure this test exists for: nothing would report it, restic
// would simply talk to the wrong path.
func TestSnapshotter_repositoryURL(t *testing.T) {
	// Arrange
	backend, err := NewFactory(Deps{RepoURLBase: demoRepoURLBase, SharedStore: newStore(t)}).
		New(model.BackendSpec{Volume: demoVolume(), Tier: model.BackendTierSavepoint})
	require.NoError(t, err)
	snapshotter, ok := backend.(*Snapshotter)
	require.True(t, ok)

	// Act
	got := snapshotter.repositoryURL()

	// Assert
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
			h := newHarness(t, model.BackendTierSavepoint, tt.opts)
			restore := h.runner.On("RunOneShot", mock.Anything, matchRestic("restore")).
				Run(respondWith(restoreSummaryLine)).Return(nil).Once()
			if tt.wantWipe {
				// The wipe is a container of its own and must finish before restic
				// writes a byte; reversed, the restore would be deleted by its own wipe.
				wipe := h.runner.On("WipeMount", mock.Anything, demoProject, demoService, demoTarget).
					Return(nil).Once()
				mock.InOrder(wipe, restore)
			}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
				SnapshotID: "1780335122",
				Payload:    Payload{SnapshotRef: "3f8a1c0d9b"},
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
			h.publisher.AssertNumberOfCalls(t, "Unregister", 1)
			h.runner.AssertExpectations(t)
			h.publisher.AssertExpectations(t)
		})
	}
}

// A record that has been through the store carries its payload back as a
// map[string]any — SnapshotStat.Payload is `any` — so the restore has to read
// that shape as readily as the struct it wrote.
func TestSnapshotter_Restore_ReadsARoundTrippedPayload(t *testing.T) {
	// Arrange
	h := newHarness(t, model.BackendTierSavepoint, Options{Image: demoImage, Incremental: true, Overwrite: "always"})
	h.runner.On("RunOneShot", mock.Anything, matchRestic("restore")).
		Run(respondWith(restoreSummaryLine)).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	err := h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
		SnapshotID: "1780335122",
		Payload: map[string]any{
			"restic_snapshot_id":    "3f8a1c0d9b",
			"data_added":            float64(1048576),
			"total_bytes_processed": float64(10485760),
		},
	})

	// Assert
	require.NoError(t, err)
	require.Len(t, h.runner.Specs(), 1)
	assert.Contains(t, h.runner.Specs()[0].Cmd, "3f8a1c0d9b")
	h.runner.AssertExpectations(t)
	h.publisher.AssertExpectations(t)
}

// Exists trusts the commit marker: the record is written last, after every
// backend reported success, so a record naming this capture already says the
// capture completed. Nothing is asked of the store or of a container — the
// empty mocks are the assertion.
func TestSnapshotter_Exists(t *testing.T) {
	// Arrange
	h := newHarness(t, model.BackendTierSavepoint, defaultOptions())
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	ok, err := h.snapshotter.Exists(ctx, model.BackendExistsRequest{SnapshotID: "1780335122"})

	// Assert
	require.NoError(t, err)
	assert.True(t, ok)
	h.publisher.AssertNumberOfCalls(t, "Register", 0) // a pre-flight publishes nothing
	h.runner.AssertExpectations(t)
	h.publisher.AssertExpectations(t)
}

// Remove is server-side: dropping a snapshot object is byte for byte what
// `restic forget <id>` does on the wire, so no container and no exclusive lock
// are involved. A capture that reported nothing has nothing to drop.
func TestSnapshotter_Remove(t *testing.T) {
	tests := []struct {
		name        string
		payload     any
		wantDeleted bool
	}{
		{
			name:        "a capture's snapshot is dropped",
			payload:     Payload{SnapshotRef: "3f8a1c0d9b"},
			wantDeleted: true,
		},
		{
			name:    "a snapshot already gone is not an error",
			payload: Payload{SnapshotRef: "deadbeef"},
		},
		{
			name:    "a failed capture reported no payload, so there is nothing to drop",
			payload: nil,
		},
		{
			name:    "a payload naming no snapshot is likewise a no-op",
			payload: Payload{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, model.BackendTierSavepoint, defaultOptions())
			ctx := zerolog.Nop().WithContext(context.Background())
			require.NoError(t, h.repo.PutObject(ctx, "snapshots/3f8a1c0d9b", strings.NewReader("snapshot")))
			require.NoError(t, h.repo.PutObject(ctx, "snapshots/0011", strings.NewReader("another")))

			// Act
			err := h.snapshotter.Remove(ctx, model.BackendRemoveRequest{
				SnapshotID: "1780335122",
				Payload:    tt.payload,
			})

			// Assert
			require.NoError(t, err)
			remaining := objectsUnder(t, h.repo, snapshotsPath)
			if tt.wantDeleted {
				assert.Equal(t, []string{"0011"}, remaining, "only the named snapshot goes")
			} else {
				assert.ElementsMatch(t, []string{"0011", "3f8a1c0d9b"}, remaining)
			}
			h.publisher.AssertNumberOfCalls(t, "Register", 0) // a remove never publishes the repository
			h.runner.AssertExpectations(t)
			h.publisher.AssertExpectations(t)
		})
	}
}

// Whatever fails, the repository stops being served: a prefix left registered
// would collide with the next operation that wants it.
func TestSnapshotter_UnpublishesOnFailure(t *testing.T) {
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
				_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "1780335122"})
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
				_, err := h.snapshotter.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: "1780335122"})
				return err
			},
			wantErr: "reported no usable snapshot id",
		},
		{
			name: "a failed restore",
			arrange: func(h *harness) {
				h.runner.On("WipeMount", mock.Anything, demoProject, demoService, demoTarget).Return(nil).Once()
				h.runner.On("RunOneShot", mock.Anything, matchRestic("restore")).
					Return(errors.New("restore boom")).Once()
			},
			act: func(ctx context.Context, h *harness) error {
				return h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
					SnapshotID: "1780335122",
					Payload:    Payload{SnapshotRef: "3f8a1c0d9b"},
				})
			},
			wantErr: "restore boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, model.BackendTierSavepoint, defaultOptions())
			tt.arrange(h)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.act(ctx, h)

			// Assert
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			h.publisher.AssertNumberOfCalls(t, "Unregister", 1) // a prefix left registered collides with the next run
			h.runner.AssertExpectations(t)
			h.publisher.AssertExpectations(t)
		})
	}
}

// A request whose ids cannot be trusted reaches neither a storage key nor a
// container command line. Both gates are allowlists, and both stand ahead of
// every side effect — the empty runner and publisher are the assertion.
func TestSnapshotter_RejectsUnsafeIDs(t *testing.T) {
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
			name: "a snapshot ref that is not hex never reaches a restore",
			act: func(ctx context.Context, h *harness) error {
				return h.snapshotter.Restore(ctx, model.BackendRestoreRequest{
					SnapshotID: "1780335122",
					Payload:    Payload{SnapshotRef: "../../config"},
				})
			},
		},
		{
			name: "a snapshot ref that is not hex never reaches a remove",
			act: func(ctx context.Context, h *harness) error {
				return h.snapshotter.Remove(ctx, model.BackendRemoveRequest{
					SnapshotID: "1780335122",
					Payload:    Payload{SnapshotRef: "../../config"},
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newHarness(t, model.BackendTierSavepoint, defaultOptions())
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.act(ctx, h)

			// Assert
			require.Error(t, err)
			h.publisher.AssertNumberOfCalls(t, "Register", 0) // the gate stands in front of every side effect
			h.runner.AssertExpectations(t)
			h.publisher.AssertExpectations(t)
		})
	}
}
