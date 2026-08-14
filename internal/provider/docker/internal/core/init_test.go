package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	stackmodel "github.com/greenmaskio/arcave/internal/provider/docker/stack/model"
)

const configPath = "/proj/.arcave/config.yaml"

func TestInitService_Init(t *testing.T) {
	inspectErr := errors.New("no such compose file")

	tests := []struct {
		name         string
		cfg          config.Config
		composeFiles []string
		override     bool
		exists       bool  // does .arcave/config.yaml already exist?
		inspectErr   error // what the compose inspector reports
		wantErr      string
		verify       func(t *testing.T, saved config.Config)
	}{
		{
			name:         "writes the config",
			cfg:          config.Config{Project: config.Project{Name: "demo"}},
			composeFiles: []string{"docker-compose.yaml"},
			verify: func(t *testing.T, saved config.Config) {
				// ApplyDefaults fills the control plane address.
				assert.Equal(t, config.DefaultControlPlaneAddress, saved.ControlPlane.Address)
			},
		},
		{
			name:    "an initialized project is an error",
			cfg:     config.Config{Project: config.Project{Name: "demo"}},
			exists:  true,
			wantErr: "project already initialized",
		},
		{
			name:     "override overwrites an initialized project",
			cfg:      config.Config{Project: config.Project{Name: "demo"}},
			override: true,
			exists:   true,
		},
		{
			// Init persists the config verbatim, so a section it has no flag for
			// survives an --override.
			name: "override preserves the sections init has no flag for",
			cfg: config.Config{
				Project:      config.Project{Name: "demo", Providers: config.Providers{Stacks: []string{"ci-2"}}},
				ControlPlane: config.ControlPlane{Address: "https://arcaved.internal:8443", Token: "t0ken"},
			},
			override: true,
			exists:   true,
			verify: func(t *testing.T, saved config.Config) {
				assert.Equal(t, "https://arcaved.internal:8443", saved.ControlPlane.Address)
				assert.Equal(t, "t0ken", saved.ControlPlane.Token)
				assert.Equal(t, []string{"ci-2"}, saved.Project.Providers.Stacks)
			},
		},
		{
			name:       "an unverifiable compose project writes nothing",
			cfg:        config.Config{Project: config.Project{Name: "demo"}},
			inspectErr: inspectErr,
			wantErr:    "verifying compose project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			cw, ci := &mockConfig{}, &mockInspector{}
			cw.On("Exists").Return(tt.exists, nil)
			cw.On("Path").Return(configPath).Maybe()
			// Init only cares that the project loads, so an empty result suffices.
			ci.On("Introspect", mock.Anything, tt.composeFiles).
				Return(stackmodel.IntrospectionResult{}, tt.inspectErr).Maybe()
			var saved config.Config
			cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
				return c.ControlPlane.Address != "" // ApplyDefaults ran before the write
			})).Run(func(args mock.Arguments) {
				saved = args.Get(0).(config.Config)
			}).Return(nil).Maybe()
			svc := NewInitService("/proj", cw, ci)

			// Act
			name, path, err := svc.Init(context.Background(), tt.cfg, tt.composeFiles, tt.override)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				cw.AssertNotCalled(t, "Save", mock.Anything)
				cw.AssertExpectations(t)
				ci.AssertExpectations(t)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "demo", name)
			assert.Equal(t, configPath, path)
			if tt.verify != nil {
				tt.verify(t, saved)
			}
			cw.AssertExpectations(t)
			ci.AssertExpectations(t)
		})
	}
}

// A Config reaches Init by value, so the defaults it stamps must not reach the caller's copy.
func TestInitService_Init_LeavesTheCallersConfigUntouched(t *testing.T) {
	// Arrange
	cw, ci := &mockConfig{}, &mockInspector{}
	cw.On("Exists").Return(false, nil)
	cw.On("Path").Return(configPath)
	ci.On("Introspect", mock.Anything, []string(nil)).Return(stackmodel.IntrospectionResult{}, nil)
	cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
		return c.ControlPlane.Address != "" // ApplyDefaults ran before the write
	})).Return(nil)
	svc := NewInitService("/proj", cw, ci)
	cfg := config.Config{}

	// Act
	_, _, err := svc.Init(context.Background(), cfg, nil, false)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, cfg.Project.Name)
	assert.Empty(t, cfg.ControlPlane.Address)
	cw.AssertExpectations(t)
	ci.AssertExpectations(t)
}

// The project name defaults to the project root's base name, normalized into
// the charset a store-root segment may carry.
func TestInitService_Init_DefaultsTheName(t *testing.T) {
	// Arrange
	cw, ci := &mockConfig{}, &mockInspector{}
	cw.On("Exists").Return(false, nil)
	cw.On("Path").Return(configPath)
	ci.On("Introspect", mock.Anything, []string(nil)).Return(stackmodel.IntrospectionResult{}, nil)
	var saved config.Config
	cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
		return c.ControlPlane.Address != "" // ApplyDefaults ran before the write
	})).Run(func(args mock.Arguments) {
		saved = args.Get(0).(config.Config)
	}).Return(nil)
	svc := NewInitService(filepath.Join("/home", "vadim", "MyProj"), cw, ci)

	// Act
	name, _, err := svc.Init(context.Background(), config.Config{}, nil, false)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "myproj", name)
	assert.Equal(t, "myproj", saved.Project.Name)
	cw.AssertExpectations(t)
	ci.AssertExpectations(t)
}

// mockConfig doubles configWriter; the seam takes no ctx.
type mockConfig struct{ mock.Mock }

func (m *mockConfig) Exists() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

func (m *mockConfig) Save(cfg config.Config) error { return m.Called(cfg).Error(0) }

func (m *mockConfig) Path() string { return m.Called().String(0) }
