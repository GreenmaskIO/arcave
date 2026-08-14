package stack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// defaultedCDC is the cdc section as a provider that configures none resolves it.
func defaultedCDC() CDCBackend {
	var c CDCBackend
	c.ApplyDefaults()
	return c
}

// The blob is the provider's, so the whole read — decode, defaults, strictness —
// is exercised here rather than through the control plane's config.
func TestSettings_Decode(t *testing.T) {
	tests := []struct {
		name       string
		raw        model.RawSettings
		wantErrMsg string
		verify     func(t *testing.T, s Settings)
	}{
		{
			name: "an empty section defaults whole",
			raw:  model.RawSettings{},
			verify: func(t *testing.T, s Settings) {
				assert.Equal(t, DefaultWaitTimeout, s.Compose.WaitTimeout)
				assert.Equal(t, DefaultStopTimeout, s.Compose.StopTimeout)
				assert.Equal(t, ProgressLog, s.Compose.Progress)
				assert.Equal(t, storageTypeDirectory, s.Storage.Type)
				assert.Equal(t, SnapshotBackendFullCopy, s.Snapshot.Backend)
			},
		},
		{
			name: "every section reads",
			raw: model.RawSettings{
				"connection": map[string]any{"host": "tcp://remote:2375", "api_version": "1.45"},
				"compose":    map[string]any{"wait_timeout": "30s", "stop_timeout": "5s", "progress": "quiet"},
				"snapshot":   map[string]any{"backend": "cdc"},
				"storage": map[string]any{
					"type":      "directory",
					"directory": map[string]any{"path": "/snaps"},
				},
				"backends": map[string]any{"cdc": map[string]any{"restore_mode": "wipe"}},
			},
			verify: func(t *testing.T, s Settings) {
				assert.Equal(t, "tcp://remote:2375", s.Connection.Host)
				assert.Equal(t, "1.45", s.Connection.APIVersion)
				assert.Equal(t, 30*time.Second, s.Compose.WaitTimeout)
				assert.Equal(t, 5*time.Second, s.Compose.StopTimeout)
				assert.Equal(t, ProgressQuiet, s.Compose.Progress)
				assert.Equal(t, dockermodel.SnapshotBackendCDC, s.Snapshot.BackendKind())
				assert.Equal(t, "/snaps", s.Storage.Directory.Path)
				assert.Equal(t, RestoreModeWipe, s.Backends.CDC.RestoreMode)
				assert.Equal(t, DefaultResticImage, s.Backends.CDC.Image, "the rest of the arm still defaults")
			},
		},
		{
			// A rollback restores through the backend its record names, so cdc is
			// resolved even where the file never mentions it.
			name: "the cdc section resolves for a full-copy provider too",
			raw:  model.RawSettings{"snapshot": map[string]any{"backend": "fullcopy"}},
			verify: func(t *testing.T, s Settings) {
				assert.Equal(t, defaultedCDC(), s.Backends.CDC)
			},
		},
		{
			name:       "a key no setting claims is a typo",
			raw:        model.RawSettings{"sandbox": map[string]any{"image": "x"}},
			wantErrMsg: "sandbox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var s Settings

			// Act
			err := tt.raw.Decode(&s)
			s.ApplyDefaults()

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			tt.verify(t, s)
		})
	}
}

func TestSettings_Validate(t *testing.T) {
	valid := func() Settings {
		s := Settings{
			Storage: Storage{Type: storageTypeDirectory, Directory: &DirectoryStorage{Path: "/snaps"}},
		}
		s.ApplyDefaults()
		return s
	}

	tests := []struct {
		name       string
		mutate     func(s *Settings)
		wantErrMsg string
	}{
		{name: "a defaulted section validates"},
		{
			name:       "a storage arm the tag does not name",
			mutate:     func(s *Settings) { s.Storage.Directory = nil },
			wantErrMsg: "no storage.directory section is present",
		},
		{
			name:       "an empty directory path",
			mutate:     func(s *Settings) { s.Storage.Directory = &DirectoryStorage{} },
			wantErrMsg: "storage.directory.path is required",
		},
		{
			name:       "a storage type arcave has no arm for",
			mutate:     func(s *Settings) { s.Storage.Type = "s3" },
			wantErrMsg: `unsupported storage.type "s3"`,
		},
		{
			name:       "a wait timeout of zero",
			mutate:     func(s *Settings) { s.Compose.WaitTimeout = 0 },
			wantErrMsg: "compose.wait_timeout must be a positive duration",
		},
		{
			name:       "a progress mode compose does not know",
			mutate:     func(s *Settings) { s.Compose.Progress = "bar" },
			wantErrMsg: `unsupported compose.progress "bar"`,
		},
		{
			name:       "a backend that cannot be constructed",
			mutate:     func(s *Settings) { s.Snapshot.Backend = "kopia" },
			wantErrMsg: `unsupported snapshot.backend "kopia"`,
		},
		{
			name:       "a cdc server address that is not host:port",
			mutate:     func(s *Settings) { s.Backends.CDC.ServerAddress = "nope" },
			wantErrMsg: "backends.cdc.server_address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			s := valid()
			if tt.mutate != nil {
				tt.mutate(&s)
			}

			// Act
			err := s.Validate()

			// Assert
			if tt.wantErrMsg == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErrMsg)
		})
	}
}
