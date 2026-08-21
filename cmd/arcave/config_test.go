package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/utils"
)

// versionedConfig prefixes body with the schema version every v1 file carries,
// so a case only spells out the sections it is about.
func versionedConfig(body string) string {
	return "version: 1\n" + body
}

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
				assert.Equal(t, config.Version, cfg.Version)
				assert.Equal(t, filepath.Base(root), cfg.Project.Name)
				assert.Equal(t, config.StorageTypeDirectory, cfg.Storage.Type)
				// No project segment: storefactory roots the store at the project itself.
				assert.Equal(t, filepath.Join(".arcave", "storage"), cfg.Storage.DirectoryPath())
				assert.Equal(t, config.SnapshotBackendFullCopy, cfg.Snapshot.Backend)
				assert.Equal(t, utils.DefaultLogLevel, cfg.Log.Level)
				assert.Equal(t, utils.DefaultLogFormat, cfg.Log.Format)
			},
		},
		{
			name: "reads the nested sections",
			file: versionedConfig("project:\n  name: proj\n  compose_files:\n    - a.yaml\n    - b.yaml\n" +
				"docker:\n  host: unix:///tmp/d.sock\n" +
				"storage:\n  type: directory\n  directory:\n    path: /snaps\n" +
				"snapshot:\n  backend: fullcopy\n" +
				"log:\n  level: warn\n  format: json\n"),
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "proj", cfg.Project.Name)
				assert.Equal(t, []string{"a.yaml", "b.yaml"}, cfg.Project.ComposeFiles)
				assert.Equal(t, "unix:///tmp/d.sock", cfg.Docker.Host)
				assert.Equal(t, config.StorageTypeDirectory, cfg.Storage.Type)
				assert.Equal(t, "/snaps", cfg.Storage.DirectoryPath())
				assert.Equal(t, config.SnapshotBackendFullCopy, cfg.Snapshot.Backend)
				assert.Equal(t, "warn", cfg.Log.Level)
				assert.Equal(t, "json", cfg.Log.Format)
			},
		},
		{
			name:      "changed flag beats file",
			file:      versionedConfig("project:\n  name: proj\nlog:\n  level: warn\n"),
			flags:     map[string]string{"log-level": "debug"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "debug", cfg.Log.Level)
			},
		},
		{
			name:      "env beats file",
			file:      versionedConfig("log:\n  level: warn\n"),
			env:       map[string]string{"ARCAVE_LOG_LEVEL": "debug"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "debug", cfg.Log.Level)
			},
		},
		{
			name:      "changed flag beats env",
			file:      versionedConfig("log:\n  level: info\n"),
			env:       map[string]string{"ARCAVE_LOG_LEVEL": "debug"},
			flags:     map[string]string{"log-level": "warn"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "warn", cfg.Log.Level)
			},
		},
		{
			name:      "init flags feed the config",
			flags:     map[string]string{"name": "flagproj", "savepoint-dir": "/snaps"},
			wantFound: false,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "flagproj", cfg.Project.Name)
				assert.Equal(t, "/snaps", cfg.Storage.DirectoryPath())
			},
		},
		{
			// The compose bounds are config keys reached through root-persistent
			// flags, so the same precedence has to hold for them as for log.level.
			name:      "the compose section comes from the file",
			file:      versionedConfig("compose:\n  wait_timeout: 5m\n  stop_timeout: 45s\n  progress: tty\n"),
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, 5*time.Minute, cfg.Compose.WaitTimeout)
				assert.Equal(t, 45*time.Second, cfg.Compose.StopTimeout)
				assert.Equal(t, config.ProgressTTY, cfg.Compose.Progress)
			},
		},
		{
			name:      "a changed --wait-timeout beats the file",
			file:      versionedConfig("compose:\n  wait_timeout: 5m\n"),
			flags:     map[string]string{"wait-timeout": "30s"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, 30*time.Second, cfg.Compose.WaitTimeout)
			},
		},
		{
			// The flag's own default is the *lowest* precedence source, so an
			// unchanged --wait-timeout must not overwrite the file's value.
			name:      "an unchanged --progress leaves the file's mode alone",
			file:      versionedConfig("compose:\n  progress: quiet\n"),
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, config.ProgressQuiet, cfg.Compose.Progress)
			},
		},
		{
			name:      "env beats the file for a compose bound too",
			file:      versionedConfig("compose:\n  wait_timeout: 5m\n"),
			env:       map[string]string{"ARCAVE_COMPOSE_WAIT_TIMEOUT": "90s"},
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, 90*time.Second, cfg.Compose.WaitTimeout)
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

// A file arcave cannot speak is rejected loudly. arcave speaks exactly one schema
// version, so a file naming another is stale; a file naming none is taken to be
// that version and is judged by its keys, which is what rejects the pre-v1 flat
// layout.
func TestResolveConfig_RejectsBadFiles(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantIs  error  // sentinel the error must match
		wantMsg string // substring the error must carry
	}{
		{
			name:    "the pre-v1 flat layout, by the keys it uses",
			file:    "name: proj\ncompose_files:\n  - a.yaml\nsavepoint_dir: /snaps\n",
			wantMsg: "savepoint_dir",
		},
		{
			name:   "an explicit version zero",
			file:   "version: 0\nproject:\n  name: proj\n",
			wantIs: model.ErrUnsupportedConfigVersion,
		},
		{
			name:   "a version from the future",
			file:   "version: 2\nproject:\n  name: proj\n",
			wantIs: model.ErrUnsupportedConfigVersion,
		},
		{
			name:    "an unknown key",
			file:    versionedConfig("project:\n  name: proj\nsavepoint_dir: /snaps\n"),
			wantMsg: "savepoint_dir",
		},
		{
			name:    "a storage tag naming an arm arcave does not implement",
			file:    versionedConfig("storage:\n  type: s3\n"),
			wantMsg: "unsupported storage.type",
		},
		{
			name:    "a snapshot backend arcave cannot construct",
			file:    versionedConfig("snapshot:\n  backend: btrfs\n"),
			wantMsg: "unsupported snapshot.backend",
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
			require.Error(t, err)
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			}
			if tt.wantMsg != "" {
				assert.Contains(t, err.Error(), tt.wantMsg)
			}
		})
	}
}

// Only bound flags feed the config: an unbound flag, even when changed, must not
// override a lower-precedence source. This is what keeps a command's transient
// flags (--override, --dry-run) out of the persisted config.
func TestResolveConfig_IgnoresUnboundFlags(t *testing.T) {
	// Arrange
	root := t.TempDir()
	writeConfig(t, root, versionedConfig("log:\n  level: warn\n"))
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

// testViper returns a viper pointed at root's config file. Each case gets a fresh
// one, so no case can leak bindings into another and none of them touches the
// package-level cfgViper the real commands bound their flags to.
func testViper(root string) *viper.Viper {
	v := viper.New()
	setupViper(v, config.Path(root))
	return v
}

// boundFlags returns a flag set carrying the flags a command may bind — the
// root-persistent log flags plus init's flags — each bound to v exactly as the
// real commands do. Only changed flags override lower-precedence sources.
func boundFlags(t *testing.T, v *viper.Viper) *pflag.FlagSet {
	t.Helper()

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", utils.DefaultLogLevel, "")
	fs.String("log-format", utils.DefaultLogFormat, "")
	fs.String("name", "", "")
	fs.StringSlice("file", []string{"docker-compose.yaml"}, "")
	fs.String("savepoint-dir", "", "")
	fs.Duration("wait-timeout", config.DefaultWaitTimeout, "")
	fs.Duration("stop-timeout", config.DefaultStopTimeout, "")
	fs.String("progress", config.ProgressLog, "")
	viperBind(v, fs, map[string]string{
		"log-level":     "log.level",
		"log-format":    "log.format",
		"name":          "project.name",
		"file":          "project.compose_files",
		"savepoint-dir": "storage.directory.path",
		"wait-timeout":  "compose.wait_timeout",
		"stop-timeout":  "compose.stop_timeout",
		"progress":      "compose.progress",
	})
	return fs
}

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".arcave")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644))
}
