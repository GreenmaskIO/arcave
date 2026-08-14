package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/utils"
)

func TestResolveConfig(t *testing.T) {
	tests := []struct {
		name      string
		file      string            // .arcave/config.yaml body; empty means no file
		env       map[string]string // ARCAVE_* overrides
		flags     map[string]string // flags the user changed
		wantFound bool
		verify    func(t *testing.T, root string, cfg config.Config)
	}{
		{
			name:      "defaults when no file",
			wantFound: false,
			verify: func(t *testing.T, root string, cfg config.Config) {
				assert.Equal(t, filepath.Base(root), cfg.Project.Name)
				assert.Equal(t, config.DefaultControlPlaneAddress, cfg.ControlPlane.Address)
				assert.Equal(t, utils.DefaultLogLevel, cfg.Log.Level)
				assert.Equal(t, utils.DefaultLogFormat, cfg.Log.Format)
			},
		},
		{
			name: "reads the nested sections",
			file: "project:\n  name: proj\n  providers:\n" +
				"    sandbox: my-linux-box\n    stacks: [my-linux-box, ci-2]\n" +
				"control_plane:\n  address: http://10.0.0.1:7654\n  token: t0ken\n" +
				"log:\n  level: warn\n  format: json\n",
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "proj", cfg.Project.Name)
				assert.Equal(t, "my-linux-box", cfg.Project.Providers.Sandbox)
				assert.Equal(t, []string{"my-linux-box", "ci-2"}, cfg.Project.Providers.Stacks)
				assert.Equal(t, "http://10.0.0.1:7654", cfg.ControlPlane.Address)
				assert.Equal(t, "t0ken", cfg.ControlPlane.Token)
				assert.Equal(t, "warn", cfg.Log.Level)
				assert.Equal(t, "json", cfg.Log.Format)
			},
		},
		{
			name:      "changed flag beats file",
			file:      "project:\n  name: proj\nlog:\n  level: warn\n",
			flags:     map[string]string{"log-level": "debug"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "debug", cfg.Log.Level)
			},
		},
		{
			name:      "env beats file",
			file:      "log:\n  level: warn\n",
			env:       map[string]string{"ARCAVE_LOG_LEVEL": "debug"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "debug", cfg.Log.Level)
			},
		},
		{
			name:      "changed flag beats env",
			file:      "log:\n  level: info\n",
			env:       map[string]string{"ARCAVE_LOG_LEVEL": "debug"},
			flags:     map[string]string{"log-level": "warn"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "warn", cfg.Log.Level)
			},
		},
		{
			name:      "init's --name feeds the config",
			flags:     map[string]string{"name": "flagproj"},
			wantFound: false,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "flagproj", cfg.Project.Name)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			root := t.TempDir()
			if tt.file != "" {
				writeConfig(t, root, tt.file)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			v := testViper(root)
			fs := boundFlags(t, v)
			for name, value := range tt.flags {
				require.NoError(t, fs.Set(name, value))
			}

			// Act
			cfg, found, err := resolveConfig(v, root)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
			tt.verify(t, root, cfg)
		})
	}
}

// A file arcave cannot speak is rejected loudly, by the keys it uses: a value
// quietly ignored is a setting the user believes is in force.
func TestResolveConfig_RejectsBadFiles(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantMsg string // substring the error must carry
	}{
		{
			name:    "the old flat layout, by the keys it uses",
			file:    "name: proj\ncompose_files:\n  - a.yaml\nsavepoint_dir: /snaps\n",
			wantMsg: "savepoint_dir",
		},
		{
			name:    "an unknown key",
			file:    "project:\n  name: proj\nsavepoint_dir: /snaps\n",
			wantMsg: "savepoint_dir",
		},
		{
			name:    "a section that moved into the provider's settings",
			file:    "project:\n  name: proj\nstorage:\n  type: directory\n",
			wantMsg: "storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			root := t.TempDir()
			writeConfig(t, root, tt.file)
			v := testViper(root)
			boundFlags(t, v)

			// Act
			_, _, err := resolveConfig(v, root)

			// Assert
			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

// Only bound flags feed the config: an unbound flag, even when changed, must not
// override a lower-precedence source.
func TestResolveConfig_IgnoresUnboundFlags(t *testing.T) {
	// Arrange
	root := t.TempDir()
	writeConfig(t, root, "log:\n  level: warn\n")
	v := testViper(root)
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", utils.DefaultLogLevel, "") // deliberately NOT bound
	require.NoError(t, fs.Set("log-level", "debug"))

	// Act
	cfg, _, err := resolveConfig(v, root)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "warn", cfg.Log.Level)
}

// A refused config file must still leave `arcave init --override` something to
// write, and not one key of it may come from the file being replaced.
func TestFallbackConfig(t *testing.T) {
	// Arrange
	root := t.TempDir()
	writeConfig(t, root, "project:\n  name: fromfile\nstorage:\n  type: directory\n")
	v := testViper(root)
	fs := boundFlags(t, v)
	require.NoError(t, fs.Set("log-level", "debug"))

	_, _, err := resolveConfig(v, root)
	require.Error(t, err, "the file this test is about must be refused")

	// Act
	cfg, err := fallbackConfig(v, root)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, filepath.Base(root), cfg.Project.Name, "the refused file contributes nothing")
	assert.Equal(t, "debug", cfg.Log.Level, "the flags still do")
	assert.Equal(t, config.DefaultControlPlaneAddress, cfg.ControlPlane.Address)
}

// viperBind panics on a missing flag, since that is a wiring bug rather than a
// runtime condition.
func TestViperBind_PanicsOnMissingFlag(t *testing.T) {
	// Arrange
	v := testViper(t.TempDir())
	empty := pflag.NewFlagSet("empty", pflag.ContinueOnError)

	// Act & Assert
	assert.Panics(t, func() {
		viperBind(v, empty, map[string]string{"nope": "nope"})
	})
}

// testViper returns a viper pointed at root's config file. Each case gets a
// fresh one, so no case leaks bindings into another.
func testViper(root string) *viper.Viper {
	v := viper.New()
	setupViper(v, config.Path(root))
	return v
}

// boundFlags returns the flags a command may bind — the log flags plus init's
// --name — each bound to v exactly as the real commands do.
func boundFlags(t *testing.T, v *viper.Viper) *pflag.FlagSet {
	t.Helper()

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", utils.DefaultLogLevel, "")
	fs.String("log-format", utils.DefaultLogFormat, "")
	fs.String("name", "", "")
	viperBind(v, fs, map[string]string{
		"log-level":  "log.level",
		"log-format": "log.format",
		"name":       "project.name",
	})
	return fs
}

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".arcave")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644))
}
