package core

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/snapshot"
	"github.com/greenmaskio/arcave/internal/storages/directory"
)

// The two WriteMount call sites are now distinguished by the bytes that flow
// into it: a savepoint restore streams the stored savepoint archive,
// while a revert streams back the live-state safety backup (which is exactly the
// bytes OpenMount produced during the backup).
const (
	savepointArchive = "savepoint-archive-bytes"
	liveArchive      = "live-state-bytes"
)

// The stores the two backends are bound to, and the fixed clock the safety
// backup's id is minted from — so its directory is an exact path, not a glob.
const (
	savepointDir = ".arcave/savepoints/demo"
	tmpDir       = ".arcave/tmp"
)

var (
	fixedNow = time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	// The safety backup lives in its own store (.arcave/tmp), so its directory is the
	// bare id — no "rollback-" prefix to distinguish it from a savepoint.
	wantSafetyBackupDir = strconv.FormatInt(fixedNow.Unix(), 10)
)

// rollbackFixture wires a Service with mock docker clients over a temp project
// root whose real directory Storager already holds the savepoint archive
// rollback's pre-flight expects.
type rollbackFixture struct {
	svc         *Service
	dc          *mockComposeRunner
	dockerVol   *mockDockerVolume
	projectRoot string
	cfg         config.Config
}

func newRollbackFixture(t *testing.T) rollbackFixture {
	t.Helper()

	projectRoot := t.TempDir()
	// The one savepoint volume's archive must exist for the pre-flight check.
	// Its key is relative to the savepoint store's root.
	const snapshotKey = "100/db/data/data.tar"
	archiveAbs := filepath.Join(projectRoot, filepath.FromSlash(savepointDir), filepath.FromSlash(snapshotKey))
	require.NoError(t, os.MkdirAll(filepath.Dir(archiveAbs), 0o755))
	require.NoError(t, os.WriteFile(archiveAbs, []byte(savepointArchive), 0o644))

	rec := model.SavepointRecord{
		Savepoint: model.Savepoint{ID: "100", Name: "sp", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Volumes: []model.VolumeRef{{
			Service: "db", MountPath: "/data", Type: "volume", Source: "demo_db",
		}},
	}
	insp := model.Inspection{
		ComposeProject: "demo",
		Mounts:         []model.Mount{{Service: "db", Target: "/data", Type: "volume", Source: "demo_db"}},
	}

	repo := &mockSavepointRepo{}
	repo.On("List").Return([]model.SavepointRecord{rec}, nil)
	inspector := &mockInspector{}
	inspector.On("Inspect", mock.Anything, mock.Anything).Return(insp, nil)

	dc := &mockComposeRunner{}
	dockerVol := &mockDockerVolume{}

	st, err := directory.NewStorage(&directory.Config{Path: projectRoot})
	require.NoError(t, err)

	// Two store-bound backends, exactly as the DI root builds them.
	savepointStore := st.SubStorage(savepointDir, true)
	safetyBackupStore := st.SubStorage(tmpDir, true)
	svc := New(
		repo,
		freeLocker(),
		inspector,
		dc,
		snapshot.NewCopyBackend(dockerVol, savepointStore),
		snapshot.NewCopyBackend(dockerVol, safetyBackupStore),
	)
	// Fix the clock so the safety backup's directory (its bare id) is an exact
	// path rather than a glob.
	svc.now = func() time.Time { return fixedNow }

	return rollbackFixture{svc: svc, dc: dc, dockerVol: dockerVol, projectRoot: projectRoot, cfg: config.Config{Project: config.Project{ComposeFiles: []string{"docker-compose.yml"}}}}
}

// safetyBackupRetained reports whether the safety backup survived. The clock is
// fixed, so its directory is known exactly.
func safetyBackupRetained(t *testing.T, projectRoot string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(tmpDir), wantSafetyBackupDir))
	return err == nil
}

// tmpParentExists reports whether the shared backup store was ever created, so a
// test can assert an aborted rollback touched nothing at all.
func tmpParentExists(t *testing.T, projectRoot string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(tmpDir)))
	return err == nil
}

// liveStream returns a fresh backup export stream for OpenMount to hand out.
func liveStream() io.ReadCloser { return io.NopCloser(strings.NewReader(liveArchive)) }

func TestRollback(t *testing.T) {
	tests := []struct {
		name         string
		wire         func(dc *mockComposeRunner, dockerVol *mockDockerVolume)
		wantErr      string
		wantRetained bool
		wantRestored bool
	}{
		{
			name: "happy path restores and drops the backup",
			wire: func(dc *mockComposeRunner, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything, mock.Anything).Return(nil).Once()
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(liveStream(), nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", savepointArchive).Return(nil)
				dc.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			},
			wantRestored: true,
		},
		{
			name: "restore failure reverts to the backup and retains it",
			wire: func(dc *mockComposeRunner, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything, mock.Anything).Return(nil)
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(liveStream(), nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", savepointArchive).Return(errors.New("restore boom"))
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", liveArchive).Return(nil)
				dc.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			wantErr:      "rollback failed: restore db /data: write docker mount: restore boom",
			wantRetained: true,
		},
		{
			name: "backup failure changes nothing and restarts services",
			wire: func(dc *mockComposeRunner, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything, mock.Anything).Return(nil).Once()
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(nil, errors.New("export boom"))
				dc.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			},
			wantErr:      "nothing changed",
			wantRetained: false,
		},
		{
			name: "unhealthy start after restore reverts to the backup",
			wire: func(dc *mockComposeRunner, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything, mock.Anything).Return(nil)
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(liveStream(), nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", savepointArchive).Return(nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", liveArchive).Return(nil)
				dc.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(errors.New("unhealthy")).Once()
				dc.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			},
			wantErr:      "rollback failed: services did not become healthy after restore: unhealthy",
			wantRetained: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			f := newRollbackFixture(t)
			tt.wire(f.dc, f.dockerVol)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			res, err := f.svc.Rollback(ctx, model.RollbackRequest{
				ComposeFiles: f.cfg.Project.ComposeFiles,
				Ref:          "100",
				WaitTimeout:  60,
			})

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.wantRestored {
				assert.Equal(t, []model.Mount{{Service: "db", Target: "/data"}}, res.Restored)
			}
			assert.Equal(t, tt.wantRetained, safetyBackupRetained(t, f.projectRoot))
			f.dc.AssertExpectations(t)
			f.dockerVol.AssertExpectations(t)
		})
	}
}

// TestService_Rollback_RevertRestartsWithLiveContext guards the seam where revert
// fans its restores out over an errgroup: errgroup cancels its derived context as
// soon as Wait returns, so restarting the services with that context would fail
// with context.Canceled on every revert. The table above cannot catch this — it
// matches the Up context with mock.Anything.
func TestService_Rollback_RevertRestartsWithLiveContext(t *testing.T) {
	// Arrange
	f := newRollbackFixture(t)
	var upCtxs []context.Context
	captureCtx := func(args mock.Arguments) { upCtxs = append(upCtxs, args.Get(0).(context.Context)) }

	f.dc.On("Stop", mock.Anything, mock.Anything).Return(nil)
	f.dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(liveStream(), nil)
	f.dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", savepointArchive).Return(nil)
	f.dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", liveArchive).Return(nil)
	// The first Up fails the health check, which drives Rollback into revert; the
	// second Up is revert's own restart, the one under test.
	f.dc.On("Up", mock.Anything, mock.Anything, mock.Anything).Run(captureCtx).Return(errors.New("unhealthy")).Once()
	f.dc.On("Up", mock.Anything, mock.Anything, mock.Anything).Run(captureCtx).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := f.svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: f.cfg.Project.ComposeFiles,
		Ref:          "100",
		WaitTimeout:  60,
	})

	// Assert
	require.Error(t, err)
	require.Len(t, upCtxs, 2)
	assert.NoError(t, upCtxs[1].Err(), "revert must restart services with a live context, not the cancelled errgroup one")
	f.dc.AssertExpectations(t)
	f.dockerVol.AssertExpectations(t)
}

// TestService_Rollback_MissingArchive asserts the pre-flight check aborts before
// anything is touched: no service is stopped and no backup is taken.
func TestService_Rollback_MissingArchive(t *testing.T) {
	// Arrange
	f := newRollbackFixture(t)
	require.NoError(t, os.RemoveAll(filepath.Join(f.projectRoot, filepath.FromSlash(savepointDir), "100")))
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := f.svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: f.cfg.Project.ComposeFiles,
		Ref:          "100",
		WaitTimeout:  60,
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "savepoint archive missing for db /data")
	f.dc.AssertNotCalled(t, "Stop")
	f.dockerVol.AssertNotCalled(t, "OpenMount")
	assert.False(t, tmpParentExists(t, f.projectRoot))
}
