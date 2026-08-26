// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
			file: "project:\n  name: proj\n" +
				"docker:\n  host: unix:///tmp/d.sock\n" +
				"storage:\n  type: directory\n  directory:\n    path: /snaps\n" +
				"snapshot:\n  backend: fullcopy\n" +
				"log:\n  level: warn\n  format: json\n",
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, "proj", cfg.Project.Name)
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
			file:      "compose:\n  wait_timeout: 5m\n  stop_timeout: 45s\n  progress: tty\n",
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, 5*time.Minute, cfg.Compose.WaitTimeout)
				assert.Equal(t, 45*time.Second, cfg.Compose.StopTimeout)
				assert.Equal(t, config.ProgressTTY, cfg.Compose.Progress)
			},
		},
		{
			name:      "a changed --wait-timeout beats the file",
			file:      "compose:\n  wait_timeout: 5m\n",
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
			file:      "compose:\n  progress: quiet\n",
			wantFound: true,
			verify: func(t *testing.T, _ string, cfg config.Config) {
				assert.Equal(t, config.ProgressQuiet, cfg.Compose.Progress)
			},
		},
		{
			name:      "env beats the file for a compose bound too",
			file:      "compose:\n  wait_timeout: 5m\n",
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

// A file arcave cannot speak is rejected by the keys it uses — the legacy flat
// layout included.
func TestResolveConfig_RejectsBadFiles(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantMsg string // substring the error must carry
	}{
		{
			name:    "the legacy flat layout, by the keys it uses",
			file:    "name: proj\nsavepoint_dir: /snaps\n",
			wantMsg: "savepoint_dir",
		},
		{
			name:    "an unknown key",
			file:    "project:\n  name: proj\nsavepoint_dir: /snaps\n",
			wantMsg: "savepoint_dir",
		},
		{
			name:    "a storage tag naming an arm arcave does not implement",
			file:    "storage:\n  type: s3\n",
			wantMsg: "unsupported storage.type",
		},
		{
			name:    "a snapshot backend arcave cannot construct",
			file:    "snapshot:\n  backend: btrfs\n",
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
			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

// Only bound flags feed the config: an unbound flag, even when changed, must not
// override a lower-precedence source. This is what keeps a command's transient
// flags (--override, --dry-run) out of the persisted config.
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
	fs.String("savepoint-dir", "", "")
	fs.Duration("wait-timeout", config.DefaultWaitTimeout, "")
	fs.Duration("stop-timeout", config.DefaultStopTimeout, "")
	fs.String("progress", config.ProgressLog, "")
	viperBind(v, fs, map[string]string{
		"log-level":     "log.level",
		"log-format":    "log.format",
		"name":          "project.name",
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
