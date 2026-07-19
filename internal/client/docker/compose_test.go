package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestComposeRunner_Stop(t *testing.T) {
	// Arrange
	file := writeComposeFile(t)
	svc := &mockCompose{}
	svc.On("Stop", mock.Anything, "demo", mock.Anything).Return(nil).Once()
	c := &ComposeRunner{svc: svc}

	// Act
	err := c.Stop(context.Background(), file)

	// Assert
	require.NoError(t, err)
	svc.AssertExpectations(t)
	opts := svc.Calls[0].Arguments.Get(2).(api.StopOptions)
	require.NotNil(t, opts.Project, "compose needs the project to know which services to stop")
	assertLabelled(t, opts.Project)
}

func TestComposeRunner_Up(t *testing.T) {
	tests := []struct {
		name        string
		waitTimeout int
		wantTimeout time.Duration
	}{
		{
			name:        "carries the caller's bound",
			waitTimeout: 30,
			wantTimeout: 30 * time.Second,
		},
		{
			name:        "falls back to the default when non-positive",
			waitTimeout: 0,
			wantTimeout: DefaultUpWaitTimeout * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			file := writeComposeFile(t)
			svc := &mockCompose{}
			svc.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			c := &ComposeRunner{svc: svc}

			// Act
			err := c.Up(context.Background(), tt.waitTimeout, file)

			// Assert
			require.NoError(t, err)
			svc.AssertExpectations(t)

			project := svc.Calls[0].Arguments.Get(1).(*types.Project)
			assert.Equal(t, "demo", project.Name)
			assertLabelled(t, project)

			opts := svc.Calls[0].Arguments.Get(2).(api.UpOptions)
			assert.True(t, opts.Start.Wait, "without Wait, Up would return before the services are healthy")
			assert.Equal(t, tt.wantTimeout, opts.Start.WaitTimeout)
			assert.Equal(t, []string{"db"}, opts.Start.Services)

			// The library's zero CreateOptions means "renew anonymous volumes",
			// the opposite of the CLI's default: a container recreated because
			// the compose file changed would lose data arcave promises to keep.
			assert.True(t, opts.Create.Inherit, "a recreated container must inherit its anonymous volumes")
			assert.Equal(t, api.RecreateDiverged, opts.Create.Recreate)
		})
	}
}

// Compose does not label the project for you when it is called as a library, and
// without the labels it does not recognise its own containers: an Up would
// recreate a stack it should have reused, and a Stop would find nothing to stop.
func assertLabelled(t *testing.T, project *types.Project) {
	t.Helper()
	for name, service := range project.Services {
		assert.Equal(t, project.Name, service.CustomLabels[api.ProjectLabel])
		assert.Equal(t, name, service.CustomLabels[api.ServiceLabel])
		assert.Equal(t, api.ComposeVersion, service.CustomLabels[api.VersionLabel])
		assert.Equal(t, "False", service.CustomLabels[api.OneoffLabel])
	}
}

// writeComposeFile drops a one-service project on disk and returns its path.
func writeComposeFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte("name: demo\nservices:\n  db:\n    image: postgres:16\n"), 0o600))
	return path
}
