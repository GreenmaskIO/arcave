package introspect

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The loader is a seam: Inspect projects whatever it returns, so it runs against
// an in-memory project with no compose file on disk.
func TestInspector_Inspect(t *testing.T) {
	t.Run("projects the loaded project", func(t *testing.T) {
		// Arrange
		project := &types.Project{
			Name: "fixture",
			// compose-go resolves every volume's Name at load; introspect reads it
			// rather than re-deriving the project prefix.
			Volumes: types.Volumes{"pgdata": types.VolumeConfig{Name: "fixture_pgdata"}},
			Services: types.Services{
				"db": {Volumes: []types.ServiceVolumeConfig{
					{Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data"},
				}},
				"app": {Volumes: []types.ServiceVolumeConfig{
					{Type: "tmpfs", Target: "/scratch"},
				}},
			},
		}
		loader := &mockLoader{}
		loader.On("Load", mock.Anything, []string{"a.yml"}).Return(project, nil).Once()

		// Act
		got, err := NewInspector(loader).Inspect(context.Background(), "a.yml")

		// Assert
		require.NoError(t, err)
		loader.AssertExpectations(t)
		assert.Equal(t, model.Project{
			Name: "fixture",
			Services: []model.Service{
				{Name: "app", Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeUnsupported, RawType: "tmpfs", Target: "/scratch", Backend: model.SnapshotBackendUnknown},
				}},
				{Name: "db", Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "fixture_pgdata"},
				}},
			},
			Volumes: []model.NamedVolume{
				{Key: "pgdata", Name: "fixture_pgdata", UsedBy: []string{"db"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
			},
		}, got.Project)
	})

	t.Run("wraps a loader failure", func(t *testing.T) {
		// Arrange
		loader := &mockLoader{}
		loader.On("Load", mock.Anything, mock.Anything).Return(nil, errors.New("bad compose file")).Once()

		// Act
		_, err := NewInspector(loader).Inspect(context.Background())

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading docker compose file(s)")
		assert.Contains(t, err.Error(), "bad compose file")
	})
}

// Real compose files, not hand-built *types.Project values: what compose-go
// resolves — volume names, bind paths, external flags — is half of what
// introspect reports.
func TestIntrospect(t *testing.T) {
	// compose-go resolves bind sources to absolute paths.
	bindCfg := abs(t, "cfg")
	bindLocal := abs(t, "local")

	tests := []struct {
		name string
		file string
		want model.Project
	}{
		{
			name: "named volume from the volumes section",
			file: "named.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "db", Image: "postgres:16", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "demo_pgdata"},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "pgdata", Name: "demo_pgdata", UsedBy: []string{"db"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
				},
			},
		},
		{
			name: "named volume shared by two services is one project volume",
			file: "shared.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "api", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets", Target: "/srv/assets", VolumeName: "demo_assets"},
					}},
					{Name: "web", Image: "nginx:1", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets", Target: "/usr/share/nginx/html", VolumeName: "demo_assets"},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "assets", Name: "demo_assets", UsedBy: []string{"api", "web"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
				},
			},
		},
		{
			name: "explicit name wins over the project prefix",
			file: "explicit_name.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "db", Image: "postgres:16", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "custom_pgdata"},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "pgdata", Name: "custom_pgdata", UsedBy: []string{"db"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
				},
			},
		},
		{
			name: "declared but unmounted volume is reported with no users",
			file: "unmounted.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{}},
				},
				Volumes: []model.NamedVolume{
					{Key: "orphan", Name: "demo_orphan", UsedBy: nil, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
				},
			},
		},
		{
			name: "external volume keeps its own name and is flagged",
			file: "external.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "db", Image: "postgres:16", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "shared_pgdata", Target: "/var/lib/postgresql/data", VolumeName: "shared_pgdata"},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "shared_pgdata", Name: "shared_pgdata", External: true, UsedBy: []string{"db"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
				},
			},
		},
		{
			name: "anonymous volume has no source and no project entry",
			file: "anonymous.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "db", Image: "postgres:16", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/var/lib/postgresql/data", NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			name: "bind mount carries the resolved host path",
			file: "bind.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeBind, RawType: "bind", Source: bindCfg, Target: "/etc/cfg", NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			name: "tmpfs is unsupported, not dropped",
			file: "tmpfs.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeUnsupported, RawType: "tmpfs", Target: "/scratch", Backend: model.SnapshotBackendUnknown},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			name: "services and volumes come out sorted",
			file: "ordering.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "api", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "adata", Target: "/a", VolumeName: "demo_adata"},
					}},
					{Name: "web", Image: "nginx:1", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "wdata", Target: "/w", VolumeName: "demo_wdata"},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "adata", Name: "demo_adata", UsedBy: []string{"api"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
					{Key: "wdata", Name: "demo_wdata", UsedBy: []string{"web"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
				},
			},
		},
		{
			name: "a volume and a service of the same name stay apart",
			file: "collision.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "db", Image: "postgres:16", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "db", Target: "/var/lib/postgresql/data", VolumeName: "demo_db"},
						{Type: model.VolumeTypeBind, RawType: "bind", Source: bindLocal, Target: "/db", NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "db", Name: "demo_db", UsedBy: []string{"db"}, NeedToBackup: true, Backend: model.SnapshotBackendFullCopy},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := loadProject(t, tt.file)

			// Act
			got := introspect(project)

			// Assert
			assert.Equal(t, tt.want, got.Project)
		})
	}
}

// The two levels join on StorageID; a volume sharing a name with a service must
// not collide with it.
func TestIntrospect_StorageID(t *testing.T) {
	// Arrange
	project := loadProject(t, "collision.yml")

	// Act
	got := introspect(project).Project

	// Assert
	require.Len(t, got.Services, 1)
	require.Len(t, got.Services[0].Volumes, 2)
	require.Len(t, got.Volumes, 1)

	svc := got.Services[0]
	assert.Equal(t, "volume/demo_db", svc.Volumes[0].StorageID(svc.Name))
	assert.Equal(t, "service/db/db", svc.Volumes[1].StorageID(svc.Name))
	assert.Equal(t, "volume/demo_db", got.Volumes[0].StorageID(), "service and project entry must address the same data")
}

func TestVolumeUsers(t *testing.T) {
	named := func(key string) model.ServiceVolume {
		return model.ServiceVolume{Type: model.VolumeTypeNamed, Source: key}
	}

	tests := []struct {
		name     string
		services []model.Service
		want     map[string][]string
	}{
		{
			name: "one volume shared by two services",
			services: []model.Service{
				{Name: "api", Volumes: []model.ServiceVolume{named("assets")}},
				{Name: "web", Volumes: []model.ServiceVolume{named("assets")}},
			},
			want: map[string][]string{"assets": {"api", "web"}},
		},
		{
			name: "a service mounting one volume twice is one user",
			services: []model.Service{
				{Name: "db", Volumes: []model.ServiceVolume{named("data"), named("data")}},
			},
			want: map[string][]string{"data": {"db"}},
		},
		{
			name: "bind, anonymous and unsupported mounts have no volume key",
			services: []model.Service{
				{Name: "app", Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeBind, Source: "/host/cfg"},
					{Type: model.VolumeTypeAnon},
					{Type: model.VolumeTypeUnsupported, RawType: "tmpfs"},
				}},
			},
			want: map[string][]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := volumeUsers(tt.services)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// loadProject loads a fixture without the environment (no .env, no COMPOSE_FILE),
// so it reads the same on any machine.
func loadProject(t *testing.T, file string) *types.Project {
	t.Helper()

	opts, err := cli.NewProjectOptions([]string{filepath.Join("testdata", file)})
	require.NoError(t, err)

	project, err := opts.LoadProject(context.Background())
	require.NoError(t, err)
	return project
}

// abs mirrors how compose-go resolves a relative bind source: against the
// compose file's directory.
func abs(t *testing.T, path string) string {
	t.Helper()

	got, err := filepath.Abs(filepath.Join("testdata", path))
	require.NoError(t, err)
	return got
}

// mockLoader takes the variadic files as one []string so an expectation can name
// them.
type mockLoader struct {
	mock.Mock
}

func (m *mockLoader) Load(ctx context.Context, files ...string) (*types.Project, error) {
	args := m.Called(ctx, files)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}
