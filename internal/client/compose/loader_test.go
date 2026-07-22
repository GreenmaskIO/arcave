package compose

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Load is the one part of this package that reads the disk, so it is exercised
// against real files parsed by compose-go — no docker daemon involved. Each case
// writes its files into a fresh project directory and names the ones Load is
// handed, so the cases differ only in what is on disk.
func TestLoader_Load(t *testing.T) {
	const baseYAML = `
name: fixture
services:
  db:
    image: postgres:16
    volumes:
      - pgdata:/var/lib/postgresql/data
  app:
    image: busybox
    volumes:
      - ./cfg:/etc/cfg
volumes:
  pgdata:
`

	tests := []struct {
		name       string
		files      map[string]string
		load       []string
		env        map[string]string
		wantErrMsg string
		verify     func(t *testing.T, dir string, project *types.Project)
	}{
		{
			name:  "parses every service of the project",
			files: map[string]string{"docker-compose.yml": baseYAML},
			load:  []string{"docker-compose.yml"},
			verify: func(t *testing.T, dir string, project *types.Project) {
				assert.Equal(t, "fixture", project.Name)
				assert.ElementsMatch(t, []string{"app", "db"}, project.ServiceNames())
				assert.Equal(t, []string{filepath.Join(dir, "docker-compose.yml")}, project.ComposeFiles)
			},
		},
		{
			name:  "resolves a relative bind source against the project directory",
			files: map[string]string{"docker-compose.yml": baseYAML},
			load:  []string{"docker-compose.yml"},
			verify: func(t *testing.T, dir string, project *types.Project) {
				require.Len(t, project.Services["app"].Volumes, 1)
				assert.Equal(t, filepath.Join(dir, "cfg"), project.Services["app"].Volumes[0].Source)
			},
		},
		{
			// WithOsEnv — arcave passes its own environment through to compose-go.
			name:  "interpolates variables from the process environment",
			files: map[string]string{"docker-compose.yml": "name: fixture\nservices:\n  db:\n    image: postgres:${GM_TEST_PG_TAG}\n"},
			load:  []string{"docker-compose.yml"},
			env:   map[string]string{"GM_TEST_PG_TAG": "17"},
			verify: func(t *testing.T, _ string, project *types.Project) {
				assert.Equal(t, "postgres:17", project.Services["db"].Image)
			},
		},
		{
			// arcave hands Load whatever --file list the caller gave it, so the
			// later file must win — that is what an override file is for.
			name: "merges the files in the order given",
			files: map[string]string{
				"docker-compose.yml":          baseYAML,
				"docker-compose.override.yml": "services:\n  db:\n    image: postgres:17\n",
			},
			load: []string{"docker-compose.yml", "docker-compose.override.yml"},
			verify: func(t *testing.T, _ string, project *types.Project) {
				assert.Equal(t, "postgres:17", project.Services["db"].Image)
				assert.Len(t, project.Services, 2, "the override must not drop a service the base declares")
			},
		},
		{
			// A project with no services parses fine but is useless to arcave:
			// there is nothing to stop, start or snapshot, so Load refuses it
			// rather than letting an empty savepoint look like a success.
			name:       "refuses a project declaring no services",
			files:      map[string]string{"docker-compose.yml": "name: empty\nservices: {}\n"},
			load:       []string{"docker-compose.yml"},
			wantErrMsg: "no services",
		},
		{
			name:       "reports a compose file that is not there",
			load:       []string{"docker-compose.yml"},
			wantErrMsg: "load compose project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			dir := t.TempDir()
			for name, content := range tt.files {
				writeFile(t, dir, name, content)
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			paths := make([]string, 0, len(tt.load))
			for _, name := range tt.load {
				paths = append(paths, filepath.Join(dir, name))
			}

			// Act
			project, err := NewLoader().Load(context.Background(), paths...)

			// Assert
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			tt.verify(t, dir, project)
		})
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}
