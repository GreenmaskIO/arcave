package docker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockRunner is a test double for the docker CLI runner (the run field on
// ComposeRunner/Engine). It matches invocations via testify/mock so tests can
// assert on the assembled arguments without a daemon.
type mockRunner struct {
	mock.Mock
}

func (m *mockRunner) run(ctx context.Context, args ...string) error {
	return m.Called(ctx, args).Error(0)
}

func TestComposeRunner_Stop(t *testing.T) {
	// Arrange
	wantArgs := []string{"compose", "-f", "docker-compose.yml", "stop"}
	runner := &mockRunner{}
	runner.On("run", mock.Anything, wantArgs).Return(nil).Once()
	c := NewComposeRunner(Options{})
	c.run = runner.run

	// Act
	err := c.Stop(context.Background(), "docker-compose.yml")

	// Assert
	require.NoError(t, err)
	runner.AssertExpectations(t)
}

func TestComposeRunner_Up(t *testing.T) {
	tests := []struct {
		name        string
		files       []string
		waitTimeout int
		wantArgs    []string
	}{
		{
			name:        "interleaves multiple -f and bounds wait",
			files:       []string{"a.yml", "b.yml"},
			waitTimeout: 30,
			wantArgs:    []string{"compose", "-f", "a.yml", "-f", "b.yml", "up", "-d", "--wait", "--wait-timeout", "30"},
		},
		{
			name:        "falls back to default timeout when non-positive",
			files:       nil,
			waitTimeout: 0,
			wantArgs:    []string{"compose", "up", "-d", "--wait", "--wait-timeout", "120"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &mockRunner{}
			runner.On("run", mock.Anything, tt.wantArgs).Return(nil).Once()
			c := NewComposeRunner(Options{})
			c.run = runner.run

			// Act
			err := c.Up(context.Background(), tt.waitTimeout, tt.files...)

			// Assert
			require.NoError(t, err)
			runner.AssertExpectations(t)
		})
	}
}
