package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/greenmaskio/arcave/internal/config"
)

func TestRepository_Path(t *testing.T) {
	// Arrange
	root := t.TempDir()

	// Act
	got := New(root).Path()

	// Assert
	assert.Equal(t, filepath.Join(root, ".arcave", "config.yaml"), got)
}

func TestRepository_Exists(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, root string)
		want    bool
		wantErr bool
	}{
		{
			name:    "no .arcave directory",
			arrange: func(*testing.T, string) {},
			want:    false,
		},
		{
			name: "directory without config file",
			arrange: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".arcave"), 0o755))
			},
			want: false,
		},
		{
			name: "config file present",
			arrange: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".arcave"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(root, ".arcave", "config.yaml"), []byte("name: x\n"), 0o644))
			},
			want: true,
		},
		{
			name: "stat fails because .arcave is a regular file",
			arrange: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".arcave"), []byte("not a dir"), 0o644))
			},
			want:    false,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			root := t.TempDir()
			tt.arrange(t, root)

			// Act
			got, err := New(root).Exists()

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				assert.NotErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRepository_Save(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
	}{
		{
			name: "full config",
			cfg: config.Config{
				Project: config.Project{
					Name: "shop",
					Providers: config.Providers{
						Sandbox: "my-linux-box",
						Stacks:  []string{"my-linux-box", "ci-2"},
					},
				},
				ControlPlane: config.ControlPlane{
					Address: config.DefaultControlPlaneAddress,
					Token:   "t0ken",
				},
				Log: config.Log{Level: "debug", Format: "json"},
			},
		},
		{
			name: "zero log settings are omitted",
			cfg: config.Config{
				Project:      config.Project{Name: "shop"},
				ControlPlane: config.ControlPlane{Address: config.DefaultControlPlaneAddress},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			root := t.TempDir()
			repo := New(root)

			// Act
			err := repo.Save(tt.cfg)

			// Assert
			require.NoError(t, err)

			data, err := os.ReadFile(repo.Path())
			require.NoError(t, err)

			var got config.Config
			require.NoError(t, yaml.Unmarshal(data, &got))
			assert.Equal(t, tt.cfg, got)

			exists, err := repo.Exists()
			require.NoError(t, err)
			assert.True(t, exists)
		})
	}
}

func TestRepository_Save_CreatesMissingDirectory(t *testing.T) {
	// Arrange
	root := filepath.Join(t.TempDir(), "nested", "project")
	repo := New(root)

	// Act
	err := repo.Save(config.Config{Project: config.Project{Name: "shop"}})

	// Assert
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(root, ".arcave"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestRepository_Save_Overwrites(t *testing.T) {
	// Arrange
	root := t.TempDir()
	repo := New(root)
	require.NoError(t, repo.Save(savedConfig("old")))

	// Act
	err := repo.Save(savedConfig("new"))

	// Assert
	require.NoError(t, err)
	data, err := os.ReadFile(repo.Path())
	require.NoError(t, err)

	var got config.Config
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, "new", got.Project.Name)
	assert.Equal(t, []string{"new-box"}, got.Project.Providers.Stacks)
}

// savedConfig returns a minimal v1 config for the project named name.
func savedConfig(name string) config.Config {
	return config.Config{
		Project:      config.Project{Name: name, Providers: config.Providers{Stacks: []string{name + "-box"}}},
		ControlPlane: config.ControlPlane{Address: config.DefaultControlPlaneAddress},
	}
}

func TestRepository_Save_WriteFails(t *testing.T) {
	// Arrange
	root := t.TempDir()
	repo := New(root)
	require.NoError(t, os.MkdirAll(repo.Path(), 0o755)) // config.yaml is a directory

	// Act
	err := repo.Save(config.Config{Project: config.Project{Name: "shop"}})

	// Assert
	require.ErrorContains(t, err, "write config")
}

func TestRepository_Save_MkdirFails(t *testing.T) {
	// Arrange
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".arcave"), []byte("not a dir"), 0o644))

	// Act
	err := New(root).Save(config.Config{Project: config.Project{Name: "shop"}})

	// Assert
	require.ErrorContains(t, err, "create config directory")
	assert.NoFileExists(t, filepath.Join(root, ".arcave", "config.yaml"))
}
