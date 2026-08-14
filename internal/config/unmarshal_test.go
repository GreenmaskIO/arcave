package config

import (
	"path/filepath"
	"strings"
	"testing"

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

// Unmarshal owns the config policy, so its rules are tested on a viper fed
// straight from yaml — no file, no flags.
func TestUnmarshal(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantErrMsg string // substring the error must carry
		verify     func(t *testing.T, cfg Config)
	}{
		{
			name: "a config defaults and validates",
			yaml: "project:\n  name: myapp\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, "myapp", cfg.Project.Name)
				assert.Empty(t, cfg.Project.Providers.Stacks)
				assert.Equal(t, DefaultControlPlaneAddress, cfg.ControlPlane.Address)
			},
		},
		{
			name: "the providers section is read",
			yaml: "project:\n  name: myapp\n  providers:\n" +
				"    sandbox: my-linux-box\n    stacks:\n      - my-linux-box\n      - ci-2\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, "my-linux-box", cfg.Project.Providers.Sandbox)
				assert.Equal(t, []string{"my-linux-box", "ci-2"}, cfg.Project.Providers.Stacks)
			},
		},
		{
			// The client judges no provider id — that is the control plane's
			// answer. A non-empty string is all there is to check.
			name: "an arbitrary provider id is accepted",
			yaml: "project:\n  name: myapp\n  providers:\n    stacks: [Not-A-Type_42]\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, []string{"Not-A-Type_42"}, cfg.Project.Providers.Stacks)
			},
		},
		{
			name: "the control plane section is read",
			yaml: "project:\n  name: myapp\ncontrol_plane:\n" +
				"  address: https://arcaved.internal:8443\n  token: t0ken\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, "https://arcaved.internal:8443", cfg.ControlPlane.Address)
				assert.Equal(t, "t0ken", cfg.ControlPlane.Token)
			},
		},
		{
			// ApplyDefaults runs before Validate, so an emptied address reads as
			// "not set": a project should never fail for not naming the endpoint.
			name: "an emptied control plane address takes the default",
			yaml: "project:\n  name: myapp\ncontrol_plane:\n  address: \"\"\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, DefaultControlPlaneAddress, cfg.ControlPlane.Address)
			},
		},
		{
			name: "the log section is read",
			yaml: "project:\n  name: myapp\nlog:\n  level: warn\n  format: json\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, "warn", cfg.Log.Level)
				assert.Equal(t, "json", cfg.Log.Format)
			},
		},
		{
			name:       "the old flat file is rejected by its foreign keys",
			yaml:       "name: proj\nsavepoint_dir: /snaps\n",
			wantErrMsg: "savepoint_dir",
		},
		{
			// The sections that moved into the provider's settings are unknown keys
			// now, and the file is named by them rather than migrated.
			name: "a config carrying the withdrawn sections is rejected by their names",
			yaml: "project:\n  name: myapp\ndocker:\n  host: unix:///tmp/d.sock\n" +
				"storage:\n  type: directory\n",
			wantErrMsg: "docker",
		},
		{
			name:       "compose_files is no longer a key of the project section",
			yaml:       "project:\n  name: myapp\n  compose_files: [a.yaml]\n",
			wantErrMsg: "compose_files",
		},
		{
			name:       "the withdrawn snapshot section is rejected as an unknown key",
			yaml:       "snapshot:\n  backend: cdc\n",
			wantErrMsg: "snapshot",
		},
		{
			name:       "the withdrawn backends section is rejected as an unknown key",
			yaml:       "backends:\n  cdc:\n    image: restic/restic:0.19.1\n",
			wantErrMsg: "backends",
		},
		{
			name:       "an unknown key is rejected",
			yaml:       "savepoint_dir: /snaps\n",
			wantErrMsg: "savepoint_dir",
		},
		{
			// The name addresses the project in every request, so it is gated here
			// rather than by whatever would have rendered it.
			name:       "a project name outside the charset is rejected",
			yaml:       "project:\n  name: MyApp\n",
			wantErrMsg: `invalid project name "MyApp"`,
		},
		{
			name:       "a project name that is a path is rejected",
			yaml:       "project:\n  name: ../elsewhere\n",
			wantErrMsg: "invalid project name",
		},
		{
			name:       "an unknown key is a typo",
			yaml:       "stray: true\n",
			wantErrMsg: "stray",
		},
		{
			// Viper's own comma-slice hook, left in place by rejectUnknownKeys.
			name: "the provider list decodes from a comma-joined value",
			yaml: "project:\n  name: p\n  providers:\n    stacks: my-linux-box,ci-2\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, []string{"my-linux-box", "ci-2"}, cfg.Project.Providers.Stacks)
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

// A project that names nothing takes its name from its directory, normalized
// rather than verbatim; nothing left after normalization is an error.
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
