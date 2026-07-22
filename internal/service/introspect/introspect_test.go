package introspect

import (
	"context"
	"errors"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// The loader is a seam: Inspect projects whatever it returns, so its logic runs
// against an in-memory project with no compose file on disk.
func TestInspector_Inspect(t *testing.T) {
	t.Run("projects the loaded project", func(t *testing.T) {
		// Arrange
		project := &types.Project{
			Name: "fixture",
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
		assert.Equal(t, "fixture", got.ComposeProject)
		assert.Equal(t, []model.Mount{
			{Service: "db", Target: "/var/lib/postgresql/data", Type: "volume", Source: "fixture_pgdata"},
		}, got.Mounts)
		assert.Equal(t, []model.SkippedMount{
			{Service: "app", Reason: `tmpfs mount at "/scratch"`},
		}, got.Skipped)
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

func TestMountsOf(t *testing.T) {
	tests := []struct {
		name        string
		project     *types.Project
		wantMounts  []model.Mount
		wantSkipped []model.SkippedMount
	}{
		{
			name: "named volume gets project-prefixed source",
			project: &types.Project{
				Name: "demo",
				Services: types.Services{
					"db": {Volumes: []types.ServiceVolumeConfig{
						{Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data"},
					}},
				},
			},
			wantMounts: []model.Mount{
				{Service: "db", Target: "/var/lib/postgresql/data", Type: "volume", Source: "demo_pgdata"},
			},
		},
		{
			name: "anonymous volume has empty source",
			project: &types.Project{
				Name: "demo",
				Services: types.Services{
					"db": {Volumes: []types.ServiceVolumeConfig{
						{Type: "volume", Target: "/data"},
					}},
				},
			},
			wantMounts: []model.Mount{
				{Service: "db", Target: "/data", Type: "volume", Source: ""},
			},
		},
		{
			name: "bind mount keeps host source verbatim",
			project: &types.Project{
				Name: "demo",
				Services: types.Services{
					"app": {Volumes: []types.ServiceVolumeConfig{
						{Type: "bind", Source: "./cfg", Target: "/etc/cfg"},
					}},
				},
			},
			wantMounts: []model.Mount{
				{Service: "app", Target: "/etc/cfg", Type: "bind", Source: "./cfg"},
			},
		},
		{
			name: "tmpfs is skipped with a reason",
			project: &types.Project{
				Name: "demo",
				Services: types.Services{
					"app": {Volumes: []types.ServiceVolumeConfig{
						{Type: "tmpfs", Target: "/tmp"},
					}},
				},
			},
			wantSkipped: []model.SkippedMount{
				{Service: "app", Reason: `tmpfs mount at "/tmp"`},
			},
		},
		{
			name: "services are visited in sorted order",
			project: &types.Project{
				Name: "demo",
				Services: types.Services{
					"web": {Volumes: []types.ServiceVolumeConfig{{Type: "bind", Source: "./w", Target: "/w"}}},
					"api": {Volumes: []types.ServiceVolumeConfig{{Type: "bind", Source: "./a", Target: "/a"}}},
				},
			},
			wantMounts: []model.Mount{
				{Service: "api", Target: "/a", Type: "bind", Source: "./a"},
				{Service: "web", Target: "/w", Type: "bind", Source: "./w"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			mounts, skipped := mountsOf(tt.project)

			// Assert
			assert.Equal(t, tt.wantMounts, mounts)
			assert.Equal(t, tt.wantSkipped, skipped)
		})
	}
}

func TestVolumeName(t *testing.T) {
	project := &types.Project{
		Name: "demo",
		Volumes: types.Volumes{
			"explicit": types.VolumeConfig{Name: "custom_name"},
			"plain":    types.VolumeConfig{},
		},
	}

	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "explicit name wins over project prefix", key: "explicit", want: "custom_name"},
		{name: "plain key is project-prefixed", key: "plain", want: "demo_plain"},
		{name: "unknown key is project-prefixed", key: "missing", want: "demo_missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, volumeName(project, tt.key))
		})
	}
}

// mockLoader is the composeProjectLoader double. It takes the variadic files as
// a single []string so an expectation can name them.
type mockLoader struct {
	mock.Mock
}

func (m *mockLoader) Load(ctx context.Context, files ...string) (*types.Project, error) {
	args := m.Called(ctx, files)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}
