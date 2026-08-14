package cli

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
)

// testConfig is the config the addressing falls back to: one project, one
// declared provider.
var testConfig = config.Config{
	Project: config.Project{
		Name:      "demo",
		Providers: config.Providers{Stacks: []string{"my-linux-box"}},
	},
	ControlPlane: config.ControlPlane{Address: config.DefaultControlPlaneAddress},
}

func TestResolveScope(t *testing.T) {
	sessionID := uuid.MustParse("6f8f2f0e-1a5c-4a2e-9d4b-0f1f2a3b4c5d")

	tests := []struct {
		name       string
		cfg        config.Config
		addressing Addressing
		want       scope
		wantErrMsg string
	}{
		{
			name:       "the config answers the project and the provider",
			cfg:        testConfig,
			addressing: Addressing{SessionID: sessionID.String(), Stack: "myapp"},
			want: scope{
				ProjectName: "demo",
				SessionID:   sessionID,
				ProviderID:  "my-linux-box",
				StackID:     "myapp",
			},
		},
		{
			name: "every flag beats the config",
			cfg:  testConfig,
			addressing: Addressing{
				Project:   "other",
				SessionID: sessionID.String(),
				Provider:  "ci-2",
				Stack:     "myapp",
			},
			want: scope{
				ProjectName: "other",
				SessionID:   sessionID,
				ProviderID:  "ci-2",
				StackID:     "myapp",
			},
		},
		{
			name:       "an unnamed stack stops the chain before the session",
			cfg:        testConfig,
			addressing: Addressing{SessionID: sessionID.String()},
			wantErrMsg: "no stack",
		},
		{
			name:       "an unnamed session is the last link to fail",
			cfg:        testConfig,
			addressing: Addressing{Stack: "myapp"},
			wantErrMsg: "no session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv(EnvSessionID, "")

			// Act
			got, err := resolveScope(tt.cfg, tt.addressing)

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveStackScope(t *testing.T) {
	// Arrange
	t.Setenv(EnvSessionID, "")

	// Act
	got, err := resolveStackScope(testConfig, Addressing{Stack: "myapp"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, scope{ProjectName: "demo", ProviderID: "my-linux-box", StackID: "myapp"}, got)
}

func TestResolveProjectName(t *testing.T) {
	tests := []struct {
		name       string
		flagValue  string
		configured string
		want       string
		wantErrMsg string
	}{
		{name: "the flag wins", flagValue: "flagproj", configured: "cfgproj", want: "flagproj"},
		{name: "the config answers", configured: "cfgproj", want: "cfgproj"},
		{name: "neither source names one", wantErrMsg: "no project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := resolveProjectName(tt.flagValue, tt.configured)

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveSessionID(t *testing.T) {
	flagID := uuid.MustParse("6f8f2f0e-1a5c-4a2e-9d4b-0f1f2a3b4c5d")
	envID := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	tests := []struct {
		name       string
		flagValue  string
		env        string
		want       uuid.UUID
		wantErrMsg string
	}{
		{name: "the flag wins over the environment", flagValue: flagID.String(), env: envID.String(), want: flagID},
		{name: "the environment answers", env: envID.String(), want: envID},
		{name: "neither source names one", wantErrMsg: "arcave session create"},
		{name: "a flag that is not a uuid names the flag", flagValue: "nonsense", wantErrMsg: "--session-id"},
		{name: "an environment value that is not a uuid names the variable", env: "nonsense", wantErrMsg: EnvSessionID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv(EnvSessionID, tt.env)

			// Act
			got, err := resolveSessionID(tt.flagValue)

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveProviderID(t *testing.T) {
	tests := []struct {
		name       string
		flagValue  string
		declared   []string
		want       string
		wantErrMsg string
	}{
		{name: "the flag wins", flagValue: "ci-2", declared: []string{"my-linux-box"}, want: "ci-2"},
		{name: "the sole declared provider is the default", declared: []string{"my-linux-box"}, want: "my-linux-box"},
		{
			name:       "several declared and no flag names them",
			declared:   []string{"my-linux-box", "ci-2"},
			wantErrMsg: "my-linux-box, ci-2",
		},
		{name: "none declared and no flag", wantErrMsg: "project.providers.stacks"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := resolveProviderID(tt.flagValue, tt.declared)

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveStackID(t *testing.T) {
	// Act
	got, err := resolveStackID("myapp")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "myapp", got)

	// Act
	_, err = resolveStackID("")

	// Assert
	require.ErrorContains(t, err, "no stack")
}
