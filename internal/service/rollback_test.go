package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/storages/directory"
)

// The two WriteMount call sites are now distinguished by the bytes that flow
// into it: a savepoint restore streams the stored savepoint archive,
// while a revert streams back the live-state safety backup (which is exactly the
// bytes OpenMount produced during the backup).
const (
	savepointArchive = "savepoint-archive-bytes"
	liveArchive      = "live-state-bytes"
)

// rollbackFixture wires a Service with mock docker clients over a temp project
// root whose real directory Storager already holds the savepoint archive
// rollback's pre-flight expects.
type rollbackFixture struct {
	svc         *Service
	dc          *mockCompose
	dockerVol   *mockDockerVolume
	projectRoot string
	cfg         config.Config
}

func newRollbackFixture(t *testing.T) rollbackFixture {
	t.Helper()

	projectRoot := t.TempDir()
	// The one savepoint volume's archive must exist for the pre-flight check.
	// Its key is relative to the .gm/snapshots Storager root.
	const snapshotKey = "100/db/data/data.tar"
	archiveAbs := filepath.Join(projectRoot, ".gm", "snapshots", filepath.FromSlash(snapshotKey))
	require.NoError(t, os.MkdirAll(filepath.Dir(archiveAbs), 0o755))
	require.NoError(t, os.WriteFile(archiveAbs, []byte(savepointArchive), 0o644))

	rec := model.SavepointRecord{
		Savepoint: model.Savepoint{ID: "100", Name: "sp", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Volumes: []model.VolumeRef{{
			Service: "db", MountPath: "/data", Type: "volume", Source: "demo_db",
			Snapshot: model.SnapshotRef{Backend: "copy", Opaque: snapshotKey},
		}},
	}
	insp := compose.Inspection{
		ProjectName: "demo",
		Mounts:      []model.Mount{{Service: "db", Target: "/data", Type: "volume", Source: "demo_db"}},
	}

	mf := &mockManifest{}
	mf.On("Load").Return([]model.SavepointRecord{rec}, nil)
	inspector := &mockInspector{}
	inspector.On("Inspect", mock.Anything, mock.Anything).Return(insp, nil)

	dc := &mockCompose{}
	dockerVol := &mockDockerVolume{}
	dockerVol.On("Close").Return(nil)

	st, err := directory.NewStorage(&directory.Config{Path: projectRoot})
	require.NoError(t, err)

	svc := New(projectRoot, &mockConfig{}, mf, inspector, st)
	svc.newCompose = func(files ...string) composeRunner { return dc }
	svc.newDockerVolume = func() (dockerVolumeClient, error) { return dockerVol, nil }

	return rollbackFixture{svc: svc, dc: dc, dockerVol: dockerVol, projectRoot: projectRoot, cfg: config.Config{ComposeFiles: []string{"docker-compose.yml"}}}
}

// backupRetained reports whether a safety-backup directory survived under .gm/tmp.
func backupRetained(t *testing.T, projectRoot string) bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(projectRoot, ".gm", "tmp", "rollback-*"))
	require.NoError(t, err)
	return len(matches) > 0
}

// liveStream returns a fresh backup export stream for OpenMount to hand out.
func liveStream() io.ReadCloser { return io.NopCloser(strings.NewReader(liveArchive)) }

func TestRollback(t *testing.T) {
	tests := []struct {
		name         string
		wire         func(dc *mockCompose, dockerVol *mockDockerVolume)
		wantErr      string
		wantRetained bool
		wantRestored bool
	}{
		{
			name: "happy path restores and drops the backup",
			wire: func(dc *mockCompose, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything).Return(nil).Once()
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(liveStream(), nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", savepointArchive).Return(nil)
				dc.On("Up", mock.Anything, mock.Anything).Return(nil).Once()
			},
			wantRestored: true,
		},
		{
			name: "restore failure reverts to the backup and retains it",
			wire: func(dc *mockCompose, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything).Return(nil)
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(liveStream(), nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", savepointArchive).Return(errors.New("restore boom"))
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", liveArchive).Return(nil)
				dc.On("Up", mock.Anything, mock.Anything).Return(nil)
			},
			wantErr:      "backup retained at",
			wantRetained: true,
		},
		{
			name: "backup failure changes nothing and restarts services",
			wire: func(dc *mockCompose, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything).Return(nil).Once()
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(nil, errors.New("export boom"))
				dc.On("Up", mock.Anything, mock.Anything).Return(nil).Once()
			},
			wantErr:      "nothing changed",
			wantRetained: false,
		},
		{
			name: "unhealthy start after restore reverts to the backup",
			wire: func(dc *mockCompose, dockerVol *mockDockerVolume) {
				dc.On("Stop", mock.Anything).Return(nil)
				dockerVol.On("OpenMount", mock.Anything, "demo", "db", "/data").Return(liveStream(), nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", savepointArchive).Return(nil)
				dockerVol.On("WriteMount", mock.Anything, "demo", "db", "/data", liveArchive).Return(nil)
				dc.On("Up", mock.Anything, mock.Anything).Return(errors.New("unhealthy")).Once()
				dc.On("Up", mock.Anything, mock.Anything).Return(nil).Once()
			},
			wantErr:      "reverted to the pre-rollback state",
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
			res, err := f.svc.Rollback(ctx, f.cfg, "100", 60)

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
			assert.Equal(t, tt.wantRetained, backupRetained(t, f.projectRoot))
			f.dc.AssertExpectations(t)
			f.dockerVol.AssertExpectations(t)
		})
	}
}
