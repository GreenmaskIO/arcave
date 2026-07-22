package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/model"
)

func TestMountsOfClassifiesMounts(t *testing.T) {
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
			},
		}
		insp := &Inspector{load: func(context.Context, ...string) (*types.Project, error) {
			return project, nil
		}}

		// Act
		got, err := insp.Inspect(context.Background())

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "fixture", got.ComposeProject)
		assert.Equal(t, []model.Mount{
			{Service: "db", Target: "/var/lib/postgresql/data", Type: "volume", Source: "fixture_pgdata"},
		}, got.Mounts)
	})

	t.Run("wraps a loader failure", func(t *testing.T) {
		// Arrange
		insp := &Inspector{load: func(context.Context, ...string) (*types.Project, error) {
			return nil, errors.New("bad compose file")
		}}

		// Act
		_, err := insp.Inspect(context.Background())

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad compose file")
	})
}

func TestInspectFromFile(t *testing.T) {
	// Arrange: a real compose file parsed by compose-go (no docker daemon).
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	yaml := `
name: fixture
services:
  db:
    image: postgres
    volumes:
      - pgdata:/var/lib/postgresql/data
  app:
    image: busybox
    volumes:
      - ./cfg:/etc/cfg
      - type: tmpfs
        target: /scratch
volumes:
  pgdata:
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	// Act
	insp, err := NewInspector().Inspect(context.Background(), path)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "fixture", insp.ComposeProject)
	assert.ElementsMatch(t, []model.Mount{
		{Service: "db", Target: "/var/lib/postgresql/data", Type: "volume", Source: "fixture_pgdata"},
		{Service: "app", Target: "/etc/cfg", Type: "bind", Source: filepath.Join(dir, "cfg")},
	}, insp.Mounts)
	require.Len(t, insp.Skipped, 1)
	assert.Equal(t, "app", insp.Skipped[0].Service)
	assert.Contains(t, insp.Skipped[0].Reason, "tmpfs")
}

func TestInspectErrorsWithoutServices(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte("name: empty\nservices: {}\n"), 0o644))

	// Act
	_, err := NewInspector().Inspect(context.Background(), path)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no services")
}
