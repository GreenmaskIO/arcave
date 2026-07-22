package config

import (
	"strings"
	"testing"

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
		wantErrIs  error  // sentinel the error must match
		wantErrMsg string // substring the error must carry
		verify     func(t *testing.T, cfg Config)
	}{
		{
			name: "a current config defaults and validates",
			yaml: "version: 1\nproject:\n  name: myapp\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, Version, cfg.Version)
				assert.Equal(t, StorageTypeDirectory, cfg.Storage.Type)
				assert.Equal(t, SnapshotBackendFullCopy, cfg.Snapshot.Backend)
			},
		},
		{
			// arcave speaks one version and names no other, so a source that carries
			// none reads as current: the pre-v1 flat layout falls to the
			// unknown-key check below, and is named by the keys it uses.
			name:       "a pre-v1 flat file is rejected by its foreign keys",
			yaml:       "name: proj\nsavepoint_dir: /snaps\n",
			wantErrMsg: "savepoint_dir",
		},
		{
			name:      "a version from the future is stale",
			yaml:      "version: 2\nproject:\n  name: p\n",
			wantErrIs: model.ErrUnsupportedConfigVersion,
		},
		{
			name:      "an explicit version zero is stale",
			yaml:      "version: 0\nproject:\n  name: p\n",
			wantErrIs: model.ErrUnsupportedConfigVersion,
		},
		{
			// A schema arcave does not speak may spell a key with another type. The
			// version probe reads only "version", so this is reported as the stale
			// file it is, never as a decode error about storage.
			name:      "a future version whose keys changed type is still stale",
			yaml:      "version: 2\nstorage: s3://bucket/prefix\n",
			wantErrIs: model.ErrUnsupportedConfigVersion,
		},
		{
			name:       "an unknown key is rejected once the version is current",
			yaml:       "version: 1\nsavepoint_dir: /snaps\n",
			wantErrMsg: "savepoint_dir",
		},
		{
			name:       "an unimplemented storage arm is rejected",
			yaml:       "version: 1\nstorage:\n  type: s3\n",
			wantErrMsg: "unsupported storage.type",
		},
		{
			name: "a versionless fresh project reads as current",
			yaml: "project:\n  name: p\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, Version, cfg.Version, "init stamps the current version")
				assert.Equal(t, "p", cfg.Project.Name)
			},
		},
		{
			name:       "an unknown key is rejected without a version too",
			yaml:       "stray: true\n",
			wantErrMsg: "stray",
		},
		{
			name: "compose_files decode from a comma-joined value",
			yaml: "version: 1\nproject:\n  compose_files: a.yaml,b.yaml\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, []string{"a.yaml", "b.yaml"}, cfg.Project.ComposeFiles)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			v := newViper(t, tt.yaml)

			// Act
			cfg, err := Unmarshal(v, "/proj")

			// Assert
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
			}
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
			}
			if tt.wantErrIs == nil && tt.wantErrMsg == "" {
				require.NoError(t, err)
			}
			if tt.verify != nil {
				tt.verify(t, cfg)
			}
		})
	}
}
