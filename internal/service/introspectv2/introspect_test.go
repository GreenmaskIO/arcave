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

// The loader is a seam: Introspect projects whatever it returns, so it runs
// against an in-memory project with no compose file on disk.
func TestInspector_Introspect(t *testing.T) {
	tests := []struct {
		name    string
		project *types.Project
		loadErr error
		want    model.Project
		wantErr string
	}{
		{
			name: "projects the loaded project",
			project: &types.Project{
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
			},
			want: model.Project{
				Name: "fixture",
				Services: []model.Service{
					{Name: "app", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
					}},
					{Name: "db", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "fixture_pgdata"},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "pgdata", Name: "fixture_pgdata", UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}}},
				},
			},
		},
		{
			// compose-go rejects such a project at load, so it reaches introspect
			// only through the loader seam.
			name: "refuses a mount whose volume the project does not declare",
			project: &types.Project{
				Name: "fixture",
				Services: types.Services{
					"db": {Volumes: []types.ServiceVolumeConfig{
						{Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data"},
					}},
				},
			},
			wantErr: `service "db" mounts undeclared volume "pgdata"`,
		},
		{
			// An empty VolumeName would be an identity colliding with every other
			// empty one.
			name: "refuses a declared volume with no resolved docker name",
			project: &types.Project{
				Name:    "fixture",
				Volumes: types.Volumes{"pgdata": types.VolumeConfig{}},
				Services: types.Services{
					"db": {Volumes: []types.ServiceVolumeConfig{
						{Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data"},
					}},
				},
			},
			wantErr: `service "db" mounts volume "pgdata" with no resolved docker name`,
		},
		{
			name:    "wraps a loader failure",
			loadErr: errors.New("bad compose file"),
			wantErr: "loading docker compose file(s): bad compose file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			loader := &mockLoader{}
			loader.On("Load", mock.Anything, []string{"a.yml"}).Return(tt.project, tt.loadErr).Once()

			// Act
			got, err := NewInspector(loader).Introspect(context.Background(), "a.yml")

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got.Project)
			}
			loader.AssertExpectations(t)
		})
	}
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
			name: "named volume carries its labels, driver and driver options",
			file: "named.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "db", Image: "postgres:16", Labels: map[string]string{"io.arcave.role": "database"}, Volumes: []model.ServiceVolume{
						// compose fills an empty options struct for the short syntax.
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "demo_pgdata", Volume: &model.VolumeMount{}},
					}},
				},
				Volumes: []model.NamedVolume{
					{
						Key:    "pgdata",
						Name:   "demo_pgdata",
						UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
						Labels: map[string]string{"io.arcave.backend": "cdc"},
						Driver: "local",
						DriverOpts: map[string]string{
							"type":   "none",
							"device": "/srv/pgdata",
							"o":      "bind",
						},
					},
				},
			},
		},
		{
			// The web mount is read-only and exposes a subpath; both reach its
			// reference, so the two mounts are not interchangeable.
			name: "named volume shared by two services is one project volume, mounted twice",
			file: "shared.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "api", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets", Target: "/srv/assets", VolumeName: "demo_assets", Volume: &model.VolumeMount{}},
					}},
					{Name: "web", Image: "nginx:1", Volumes: []model.ServiceVolume{
						{
							Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets",
							Target: "/usr/share/nginx/html", VolumeName: "demo_assets",
							ReadOnly: true, Volume: &model.VolumeMount{Subpath: "public"},
						},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "assets", Name: "demo_assets", UsedBy: []model.ServiceReference{
						{Service: "api", Target: "/srv/assets"},
						{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true, Subpath: "public"},
					}},
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
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "custom_pgdata", Volume: &model.VolumeMount{}},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "pgdata", Name: "custom_pgdata", UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}}},
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
					{Key: "orphan", Name: "demo_orphan", UsedBy: nil},
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
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "shared_pgdata", Target: "/var/lib/postgresql/data", VolumeName: "shared_pgdata", Volume: &model.VolumeMount{}},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "shared_pgdata", Name: "shared_pgdata", External: true, UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}}},
				},
			},
		},
		{
			name: "anonymous volume has no source, no project entry, and carries its per-mount labels",
			file: "anonymous.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "db", Image: "postgres:16", Volumes: []model.ServiceVolume{
						{
							Type: model.VolumeTypeAnon, RawType: "volume", Target: "/var/lib/postgresql/data",
							Volume: &model.VolumeMount{Labels: map[string]string{"io.arcave.backend": "cdc"}},
						},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			name: "bind mount carries the resolved host path and its bind options",
			file: "bind.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{
							Type: model.VolumeTypeBind, RawType: "bind", Source: bindCfg, Target: "/etc/cfg",
							ReadOnly: true,
							Bind:     &model.BindMount{SELinux: "z", Propagation: "rprivate", CreateHostPath: true},
						},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			name: "tmpfs is classified and reported, not dropped",
			file: "tmpfs.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{
							Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch",
							Tmpfs: &model.TmpfsMount{SizeBytes: 64 << 20, Mode: 0o1777},
						},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			name: "npipe is classified and reported",
			file: "npipe.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNPipe, RawType: "npipe", Source: `\\.\pipe\docker_engine`, Target: `\\.\pipe\docker_engine`},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			// A CSI source is not a volumes: key, so there is no project entry.
			name: "cluster volume is classified and reported",
			file: "cluster.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeCluster, RawType: "cluster", Source: "csi-data", Target: "/data"},
					}},
				},
				Volumes: []model.NamedVolume{},
			},
		},
		{
			name: "image mount is classified and carries its subpath",
			file: "image.yml",
			want: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "app", Image: "alpine:3", Volumes: []model.ServiceVolume{
						{
							Type: model.VolumeTypeImage, RawType: "image", Source: "assets-image:1", Target: "/srv/assets",
							Image: &model.ImageMount{Subpath: "public"},
						},
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
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "adata", Target: "/a", VolumeName: "demo_adata", Volume: &model.VolumeMount{}},
					}},
					{Name: "web", Image: "nginx:1", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "wdata", Target: "/w", VolumeName: "demo_wdata", Volume: &model.VolumeMount{}},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "adata", Name: "demo_adata", UsedBy: []model.ServiceReference{{Service: "api", Target: "/a"}}},
					{Key: "wdata", Name: "demo_wdata", UsedBy: []model.ServiceReference{{Service: "web", Target: "/w"}}},
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
						{Type: model.VolumeTypeNamed, RawType: "volume", Source: "db", Target: "/var/lib/postgresql/data", VolumeName: "demo_db", Volume: &model.VolumeMount{}},
						{Type: model.VolumeTypeBind, RawType: "bind", Source: bindLocal, Target: "/db", Bind: &model.BindMount{CreateHostPath: true}},
					}},
				},
				Volumes: []model.NamedVolume{
					{Key: "db", Name: "demo_db", UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := loadProject(t, tt.file)

			// Act
			got, err := introspect(project)

			// Assert
			require.NoError(t, err)
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
	result, err := introspect(project)

	// Assert
	require.NoError(t, err)
	got := result.Project
	require.Len(t, got.Services, 1)
	require.Len(t, got.Services[0].Volumes, 2)
	require.Len(t, got.Volumes, 1)

	svc := got.Services[0]
	assert.Equal(t, "volume-named-demo_db", svc.Volumes[0].ObjectID(svc.Name))
	assert.Equal(t, "volume-service-db-db", svc.Volumes[1].ObjectID(svc.Name))
	assert.Equal(t, "volume-named-demo_db", got.Volumes[0].ObjectID(), "service and project entry must address the same data")
}

// volumeUsers inverts the services' mounts into each named volume's UsedBy,
// deduplicated and sorted by (service, target).
func TestVolumeUsers(t *testing.T) {
	named := func(key, target string) model.ServiceVolume {
		return model.ServiceVolume{Type: model.VolumeTypeNamed, Source: key, Target: target}
	}

	tests := []struct {
		name     string
		services []model.Service
		want     map[string][]model.ServiceReference
	}{
		{
			name: "one volume shared by two services, each with its own mount path",
			services: []model.Service{
				{Name: "api", Volumes: []model.ServiceVolume{named("assets", "/srv/assets")}},
				{Name: "web", Volumes: []model.ServiceVolume{named("assets", "/usr/share/nginx/html")}},
			},
			want: map[string][]model.ServiceReference{"assets": {
				{Service: "api", Target: "/srv/assets"},
				{Service: "web", Target: "/usr/share/nginx/html"},
			}},
		},
		{
			name: "a service mounting one volume at two paths is two references",
			services: []model.Service{
				{Name: "db", Volumes: []model.ServiceVolume{named("data", "/data"), named("data", "/backup")}},
			},
			want: map[string][]model.ServiceReference{"data": {
				{Service: "db", Target: "/backup"},
				{Service: "db", Target: "/data"},
			}},
		},
		{
			name: "the same service and target declared twice is one reference",
			services: []model.Service{
				{Name: "db", Volumes: []model.ServiceVolume{named("data", "/data"), named("data", "/data")}},
			},
			want: map[string][]model.ServiceReference{"data": {{Service: "db", Target: "/data"}}},
		},
		{
			name: "two mounts of one volume differing only in read_only or subpath are two references",
			services: []model.Service{
				{Name: "web", Volumes: []model.ServiceVolume{
					named("assets", "/srv/assets"),
					{Type: model.VolumeTypeNamed, Source: "assets", Target: "/srv/assets", ReadOnly: true},
					{Type: model.VolumeTypeNamed, Source: "assets", Target: "/srv/assets", Volume: &model.VolumeMount{Subpath: "public"}},
				}},
			},
			want: map[string][]model.ServiceReference{"assets": {
				{Service: "web", Target: "/srv/assets"},
				{Service: "web", Target: "/srv/assets", ReadOnly: true},
				{Service: "web", Target: "/srv/assets", Subpath: "public"},
			}},
		},
		{
			name: "no mount type other than named has a volume key",
			services: []model.Service{
				{Name: "app", Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeBind, Source: "/host/cfg"},
					{Type: model.VolumeTypeAnon},
					{Type: model.VolumeTypeTmpfs, RawType: "tmpfs"},
					{Type: model.VolumeTypeNPipe, RawType: "npipe"},
					{Type: model.VolumeTypeCluster, RawType: "cluster", Source: "csi-data"},
					{Type: model.VolumeTypeImage, RawType: "image", Source: "assets:1"},
					{Type: model.VolumeTypeUnknown, RawType: "something-new"},
				}},
			},
			want: map[string][]model.ServiceReference{},
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
