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
	"github.com/greenmaskio/arcave/internal/model"
)

const configPath = "/proj/.arcave/config.yaml"

func TestInitService_Init(t *testing.T) {
	inspectErr := errors.New("no such compose file")

	tests := []struct {
		name       string
		cfg        config.Config
		override   bool
		exists     bool  // does .arcave/config.yaml already exist?
		inspectErr error // what the compose inspector reports
		wantErr    string
		verify     func(t *testing.T, saved config.Config)
	}{
		{
			name: "writes the config",
			cfg: config.Config{Project: config.Project{
				Name:         "demo",
				ComposeFiles: []string{"docker-compose.yaml"},
			}},
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
				Project:  config.Project{Name: "demo"},
				Docker:   config.Docker{Host: "tcp://remote:2375"},
				Snapshot: config.Snapshot{Backend: config.SnapshotBackendFullCopy},
			},
			override: true,
			exists:   true,
			verify: func(t *testing.T, saved config.Config) {
				assert.Equal(t, "tcp://remote:2375", saved.Docker.Host)
				assert.Equal(t, config.SnapshotBackendFullCopy, saved.Snapshot.Backend)
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
			// Init only cares that the project loads, so an empty result suffices;
			// the files reach the inspector verbatim (ApplyDefaults leaves them be).
			ci.On("Introspect", mock.Anything, tt.cfg.Project.ComposeFiles).
				Return(model.IntrospectionResult{}, tt.inspectErr).Maybe()
			var saved config.Config
			cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
				return c.Version == config.Version
			})).Run(func(args mock.Arguments) {
				saved = args.Get(0).(config.Config)
			}).Return(nil).Maybe()
			svc := NewInitService("/proj", cw, ci)

			// Act
			name, path, err := svc.Init(context.Background(), tt.cfg, tt.override)

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
			assert.Equal(t, config.Version, saved.Version)
			// ApplyDefaults fills the name-dependent savepoint dir.
			assert.Equal(t, filepath.Join(".arcave", "storage", "demo"), saved.Storage.DirectoryPath())
			if tt.verify != nil {
				tt.verify(t, saved)
			}
			cw.AssertExpectations(t)
			ci.AssertExpectations(t)
		})
	}
}

// A Config reaches Init by value, so the defaults it stamps must not reach back
// through the caller's storage-arm pointer.
func TestInitService_Init_LeavesTheCallersConfigUntouched(t *testing.T) {
	// Arrange
	cw, ci := &mockConfig{}, &mockInspector{}
	cw.On("Exists").Return(false, nil)
	cw.On("Path").Return(configPath)
	ci.On("Introspect", mock.Anything, []string(nil)).Return(model.IntrospectionResult{}, nil)
	cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
		return c.Version == config.Version
	})).Return(nil)
	svc := NewInitService("/proj", cw, ci)
	cfg := config.Config{Storage: config.Storage{Directory: &config.DirectoryStorage{}}}

	// Act
	_, _, err := svc.Init(context.Background(), cfg, false)

	// Assert
	require.NoError(t, err)
	assert.Zero(t, cfg.Version)
	assert.Empty(t, cfg.Project.Name)
	assert.Empty(t, cfg.Storage.DirectoryPath())
	cw.AssertExpectations(t)
	ci.AssertExpectations(t)
}

// The project name defaults to the project root's base name, and the savepoint
// directory follows from it.
func TestInitService_Init_DefaultsTheName(t *testing.T) {
	// Arrange
	cw, ci := &mockConfig{}, &mockInspector{}
	cw.On("Exists").Return(false, nil)
	cw.On("Path").Return(configPath)
	ci.On("Introspect", mock.Anything, []string(nil)).Return(model.IntrospectionResult{}, nil)
	var saved config.Config
	cw.On("Save", mock.MatchedBy(func(c config.Config) bool {
		return c.Version == config.Version
	})).Run(func(args mock.Arguments) {
		saved = args.Get(0).(config.Config)
	}).Return(nil)
	svc := NewInitService(filepath.Join("/home", "vadim", "myproj"), cw, ci)

	// Act
	name, _, err := svc.Init(context.Background(), config.Config{}, false)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "myproj", name)
	assert.Equal(t, "myproj", saved.Project.Name)
	assert.Equal(t, filepath.Join(".arcave", "storage", "myproj"), saved.Storage.DirectoryPath())
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
