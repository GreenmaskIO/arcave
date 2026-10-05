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

package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newViper returns a viper holding yaml, standing in for the file cmd/arcave reads.
func newViper(t *testing.T, yaml string) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(yaml)))
	return v
}

// Unmarshal owns the config policy, so its rules are tested here on a viper fed
// straight from yaml — no file, no flags. cmd/arcave supplies the merge; Unmarshal
// decides whether the result is acceptable.
func TestUnmarshal(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantErrMsg string // substring the error must carry
		verify     func(t *testing.T, cfg Config)
	}{
		{
			name: "a minimal config defaults and validates",
			yaml: "project:\n  name: myapp\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, StorageTypeDirectory, cfg.Storage.Type)
				// The configured path carries no project segment: storefactory roots
				// the store at the project itself.
				assert.Equal(t, filepath.Join(model.StateDir, "storage"), cfg.Storage.DirectoryPath())
				assert.Equal(t, SnapshotBackendFullCopy, cfg.Snapshot.Backend)
				// A project that names no compose section still gets bounds: an
				// unbounded health-check wait is the failure this config exists to
				// prevent, so it can never be what a bare file means.
				assert.Equal(t, DefaultWaitTimeout, cfg.Compose.WaitTimeout)
				assert.Equal(t, DefaultStopTimeout, cfg.Compose.StopTimeout)
				assert.Equal(t, ProgressLog, cfg.Compose.Progress)
			},
		},
		{
			name: "the compose section is read",
			yaml: "compose:\n  wait_timeout: 5m\n  stop_timeout: 1m30s\n  progress: tty\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, 5*time.Minute, cfg.Compose.WaitTimeout)
				assert.Equal(t, 90*time.Second, cfg.Compose.StopTimeout)
				assert.Equal(t, ProgressTTY, cfg.Compose.Progress)
			},
		},
		{
			// Viper's own duration hook, left in place by rejectUnknownKeys.
			name:       "a unit go does not know is rejected",
			yaml:       "compose:\n  wait_timeout: 5 minutes\n",
			wantErrMsg: "time: unknown unit",
		},
		{
			// "Wait forever" is not on offer: it is exactly the hang the bound
			// exists to turn into an error.
			name:       "a non-positive wait timeout is rejected",
			yaml:       "compose:\n  wait_timeout: -1s\n",
			wantErrMsg: "compose.wait_timeout must be a positive duration, got -1s",
		},
		{
			name:       "a negative stop timeout is rejected",
			yaml:       "compose:\n  stop_timeout: -5s\n",
			wantErrMsg: "compose.stop_timeout must not be negative, got -5s",
		},
		{
			name:       "a progress channel arcave does not offer is rejected",
			yaml:       "compose:\n  progress: verbose\n",
			wantErrMsg: "unsupported compose.progress",
		},
		{
			// compose's own --progress values are no longer arcave's: how a record
			// is rendered is log.format's business now, and this key only picks
			// the channel. The file is refused rather than migrated, which is what
			// the config promises for any value it does not speak.
			name:       "a compose render mode is no longer a progress channel",
			yaml:       "compose:\n  progress: plain\n",
			wantErrMsg: `unsupported compose.progress "plain" (supported: log, tty, quiet)`,
		},
		{
			name:       "a legacy flat file is rejected by its foreign keys",
			yaml:       "name: proj\nsavepoint_dir: /snaps\n",
			wantErrMsg: "savepoint_dir",
		},
		{
			name:       "an unknown key is rejected",
			yaml:       "savepoint_dir: /snaps\n",
			wantErrMsg: "savepoint_dir",
		},
		{
			// The name becomes the tenant segment of every store root, so it is
			// gated here rather than at the syscall that would have used it.
			name:       "a project name outside the store-root charset is rejected",
			yaml:       "project:\n  name: MyApp\n",
			wantErrMsg: `invalid arcave project name "MyApp"`,
		},
		{
			name:       "a project name that is a path is rejected",
			yaml:       "project:\n  name: ../elsewhere\n",
			wantErrMsg: "invalid arcave project name",
		},
		{
			name:       "an unimplemented storage arm is rejected",
			yaml:       "storage:\n  type: s3\n",
			wantErrMsg: "unsupported storage.type",
		},
		{
			name:       "another unknown key is rejected",
			yaml:       "stray: true\n",
			wantErrMsg: "stray",
		},
		{
			// A full-copy project carries no cdc section: a backend it never runs
			// should not be a block in its config file.
			name: "a full-copy project gets no cdc arm",
			yaml: "snapshot:\n  backend: fullcopy\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Nil(t, cfg.Backends.CDC)
			},
		},
		{
			name: "naming cdc fills its section with the defaults",
			yaml: "snapshot:\n  backend: cdc\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, SnapshotBackendCDC, cfg.Snapshot.Backend)
				assert.Equal(t, model.SnapshotBackendCDC, cfg.Snapshot.BackendKind())
				require.NotNil(t, cfg.Backends.CDC)
				assert.Equal(t, RestoreModeIncremental, cfg.Backends.CDC.RestoreMode)
				assert.Equal(t, OverwriteAlways, cfg.Backends.CDC.Overwrite)
				assert.Equal(t, DefaultResticImage, cfg.Backends.CDC.Image)
				// Both ends of the loopback hop default with it, and how a container
				// crosses it stays a probe.
				assert.Equal(t, DefaultCDCServerAddress, cfg.Backends.CDC.ServerAddress)
				assert.Equal(t, DefaultCDCContainerHost, cfg.Backends.CDC.ContainerHost)
				assert.Equal(t, NetworkModeAuto, cfg.Backends.CDC.NetworkMode)
			},
		},
		{
			name: "an explicit network mode is read back",
			yaml: "snapshot:\n  backend: cdc\nbackends:\n  cdc:\n" +
				"    network_mode: host\n",
			verify: func(t *testing.T, cfg Config) {
				require.NotNil(t, cfg.Backends.CDC)
				assert.Equal(t, NetworkModeHost, cfg.Backends.CDC.NetworkMode)
			},
		},
		{
			name:       "a network mode arcave does not offer is rejected",
			yaml:       "backends:\n  cdc:\n    network_mode: vpn\n",
			wantErrMsg: "unsupported backends.cdc.network_mode",
		},
		{
			name: "the server address is read, and the container host still defaults",
			yaml: "snapshot:\n  backend: cdc\nbackends:\n  cdc:\n" +
				"    server_address: 127.0.0.1:18080\n",
			verify: func(t *testing.T, cfg Config) {
				require.NotNil(t, cfg.Backends.CDC)
				assert.Equal(t, "127.0.0.1:18080", cfg.Backends.CDC.ServerAddress)
				assert.Equal(t, DefaultCDCContainerHost, cfg.Backends.CDC.ContainerHost)
			},
		},
		{
			// The address is bound at wiring time, so a value that cannot split
			// fails here rather than halfway through a command.
			name:       "a server address that is not host:port is rejected",
			yaml:       "backends:\n  cdc:\n    server_address: 18080\n",
			wantErrMsg: "is not a host:port address",
		},
		{
			// The transport is tcp and nothing else, so the union that used to
			// carry it is gone: its keys are unknown ones now, and named as such.
			name:       "the withdrawn data_plane block is rejected as an unknown key",
			yaml:       "backends:\n  cdc:\n    data_plane:\n      transport: unix\n",
			wantErrMsg: "data_plane",
		},
		{
			name: "the cdc section is read, and the keys it omits still default",
			yaml: "snapshot:\n  backend: cdc\nbackends:\n  cdc:\n" +
				"    restore_mode: incremental\n    overwrite: always\n",
			verify: func(t *testing.T, cfg Config) {
				require.NotNil(t, cfg.Backends.CDC)
				assert.Equal(t, RestoreModeIncremental, cfg.Backends.CDC.RestoreMode)
				assert.Equal(t, OverwriteAlways, cfg.Backends.CDC.Overwrite)
				assert.Equal(t, DefaultResticImage, cfg.Backends.CDC.Image)
			},
		},
		{
			// The section is honoured wherever it appears: a project may configure
			// the backend before switching to it.
			name: "a cdc section under a full-copy project is still defaulted",
			yaml: "backends:\n  cdc:\n    image: restic/restic:0.18.0\n",
			verify: func(t *testing.T, cfg Config) {
				require.NotNil(t, cfg.Backends.CDC)
				assert.Equal(t, "restic/restic:0.18.0", cfg.Backends.CDC.Image)
				assert.Equal(t, RestoreModeIncremental, cfg.Backends.CDC.RestoreMode)
			},
		},
		{
			name:       "a restore mode arcave does not offer is rejected",
			yaml:       "backends:\n  cdc:\n    restore_mode: patch\n",
			wantErrMsg: "unsupported backends.cdc.restore_mode",
		},
		{
			name:       "an overwrite policy arcave does not offer is rejected",
			yaml:       "backends:\n  cdc:\n    overwrite: never\n",
			wantErrMsg: "unsupported backends.cdc.overwrite",
		},
		{
			name:       "a backend arcave cannot construct is rejected",
			yaml:       "snapshot:\n  backend: rsync\n",
			wantErrMsg: "unsupported snapshot.backend",
		},
		{
			name:       "an unknown key under backends is rejected",
			yaml:       "backends:\n  cdc:\n    password: hunter2\n",
			wantErrMsg: "password",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			v := newViper(t, tt.yaml)

			// Act
			cfg, err := Unmarshal(v, "/proj")

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
			} else {
				require.NoError(t, err)
			}
			if tt.verify != nil {
				tt.verify(t, cfg)
			}
		})
	}
}

// A project that names nothing takes its name from the directory it lives in —
// and that directory is spelled however the user spelled it, while the name is a
// store-root segment. So it is normalized rather than taken verbatim, and a
// directory with nothing left after normalization is an error naming the key to
// set, not a store rooted at "".
func TestUnmarshal_DefaultsTheProjectNameFromTheDirectory(t *testing.T) {
	tests := []struct {
		name        string
		projectRoot string
		wantName    string
		wantErrMsg  string
	}{
		{name: "already in the charset", projectRoot: filepath.Join("/home", "v", "demo"), wantName: "demo"},
		{name: "mixed case", projectRoot: filepath.Join("/home", "v", "MyApp"), wantName: "myapp"},
		{name: "punctuation", projectRoot: filepath.Join("/home", "v", "my.app v2"), wantName: "myappv2"},
		{
			name:        "nothing survives normalization",
			projectRoot: filepath.Join("/home", "v", "日本語"),
			wantErrMsg:  "set project.name explicitly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			v := newViper(t, "")

			// Act
			cfg, err := Unmarshal(v, tt.projectRoot)

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, cfg.Project.Name)
		})
	}
}
