package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
)

// mockConfigRepository is a double for .arcave/config.yaml.
type mockConfigRepository struct{ mock.Mock }

func (m *mockConfigRepository) Exists() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

func (m *mockConfigRepository) Save(cfg config.Config) error {
	return m.Called(cfg).Error(0)
}

func (m *mockConfigRepository) Path() string {
	return m.Called().String(0)
}

func TestInitHandler_Init(t *testing.T) {
	cfg := config.Config{
		Project: config.Project{
			Name:      "demo",
			Providers: config.Providers{Stacks: []string{"my-linux-box"}},
		},
		ControlPlane: config.ControlPlane{Address: "https://arcave.internal:8443", Token: "t0ken"},
		Log:          config.Log{Level: "debug", Format: "console"},
	}
	errStat := errors.New("stat .arcave/config.yaml: permission denied")
	errSave := errors.New("write config: disk full")

	tests := []struct {
		name      string
		exists    bool
		existsErr error
		override  bool
		saveErr   error
		wantSave  bool
		wantErr   error
	}{
		{
			name:     "writes the config of a fresh project",
			wantSave: true,
		},
		{
			name:    "an initialized project is refused",
			exists:  true,
			wantErr: ErrProjectInitialized,
		},
		{
			name:     "override replaces an initialized project",
			exists:   true,
			override: true,
			wantSave: true,
		},
		{
			name:      "a failed existence check surfaces",
			existsErr: errStat,
			wantErr:   errStat,
		},
		{
			name:     "a failed write surfaces",
			saveErr:  errSave,
			wantSave: true,
			wantErr:  errSave,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockConfigRepository{}
			repo.On("Exists").Return(tt.exists, tt.existsErr)
			if tt.wantSave {
				repo.On("Save", cfg).Return(tt.saveErr)
			}
			if tt.wantSave && tt.saveErr == nil {
				repo.On("Path").Return(".arcave/config.yaml")
			}
			h := NewInitHandler(repo)

			// Act
			err := h.Init(context.Background(), cfg, tt.override)

			// Assert
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
			repo.AssertExpectations(t)
		})
	}
}
