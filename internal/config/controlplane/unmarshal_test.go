package controlplane

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newViper returns a viper holding yaml, standing in for the file
// cmd/control-plane reads.
func newViper(t *testing.T, yaml string) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(yaml)))
	return v
}

// A registry entry spelled in full, so the cases below can name one section at a
// time without repeating the rest.
const composeProvider = "providers:\n  stacks:\n" +
	"    - name: docker-compose\n      type: compose\n      settings:\n" +
	"        storage:\n          type: directory\n          directory:\n            path: /var/lib/arcave\n"

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
			name: "a control plane with no providers defaults and validates",
			yaml: "",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, DefaultAPIAddress, cfg.API.Address)
				assert.Empty(t, cfg.Providers.Stacks)
			},
		},
		{
			name: "the api address is read",
			yaml: "api:\n  address: 0.0.0.0:9000\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, "0.0.0.0:9000", cfg.API.Address)
			},
		},
		{
			name:       "an api address that is not host:port is rejected",
			yaml:       "api:\n  address: not-an-address\n",
			wantErrMsg: "api.address",
		},
		{
			// The control plane carries the section; it does not read it, default it
			// or judge it.
			name: "a provider entry carries its settings verbatim",
			yaml: composeProvider,
			verify: func(t *testing.T, cfg Config) {
				require.Len(t, cfg.Providers.Stacks, 1)
				sp := cfg.Providers.Stacks[0]
				assert.Equal(t, "docker-compose", sp.Name)
				assert.Equal(t, "compose", sp.Type)
				assert.Equal(t, map[string]any{
					"storage": map[string]any{
						"type":      "directory",
						"directory": map[string]any{"path": "/var/lib/arcave"},
					},
				}, map[string]any(sp.Settings))
			},
		},
		{
			// A key of a provider type this control plane has never heard of decodes
			// as happily as one it registers: nothing here reads either.
			name: "settings of an unregistered provider type decode too",
			yaml: "providers:\n  stacks:\n" +
				"    - name: k8s\n      type: kubernetes\n      settings:\n" +
				"        kubeconfig: ~/.kube/config\n        namespace: demo\n",
			verify: func(t *testing.T, cfg Config) {
				require.Len(t, cfg.Providers.Stacks, 1)
				assert.Equal(t, "kubernetes", cfg.Providers.Stacks[0].Type)
				assert.Equal(t, "demo", cfg.Providers.Stacks[0].Settings["namespace"])
			},
		},
		{
			name: "an entry with no settings at all is legitimate",
			yaml: "providers:\n  stacks:\n    - name: dc\n      type: compose\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Empty(t, cfg.Providers.Stacks[0].Settings)
			},
		},
		{
			name:       "an entry without a name is rejected",
			yaml:       "providers:\n  stacks:\n    - type: compose\n",
			wantErrMsg: "providers.stacks[0]: name is required",
		},
		{
			name:       "an entry without a type is rejected",
			yaml:       "providers:\n  stacks:\n    - name: dc\n",
			wantErrMsg: "providers.stacks[0]: type is required",
		},
		{
			name: "two providers under one name are rejected",
			yaml: "providers:\n  stacks:\n" +
				"    - name: dc\n      type: compose\n    - name: dc\n      type: compose\n",
			wantErrMsg: `duplicate provider name "dc"`,
		},
		{
			name:       "an unknown key is a typo",
			yaml:       "database:\n  dsn: file:cp.db\n",
			wantErrMsg: "database",
		},
		{
			name: "the log section is read",
			yaml: "log:\n  level: warn\n  format: json\n",
			verify: func(t *testing.T, cfg Config) {
				assert.Equal(t, "warn", cfg.Log.Level)
				assert.Equal(t, "json", cfg.Log.Format)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			v := newViper(t, tt.yaml)

			// Act
			cfg, err := Unmarshal(v)

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
