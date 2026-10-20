// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/composespec"
)

// generateComposeSpec is two services, a named volume, a bind under the project's directory and a
// `$` the user's compose would interpolate; it is parsed without interpolation, as an artifact is.
const generateComposeSpec = `
name: %s
services:
  db:
    image: busybox
    entrypoint: ["sh", "-c", "while true; do sleep 1; done"]
    environment:
      PASS: a$b
    healthcheck:
      test: ["CMD-SHELL", "test -n \"$PASS\""]
      interval: 1s
    volumes:
      - data:/data
      - %s:/etc/conf:ro
  web:
    image: busybox
    entrypoint: ["sh", "-c", "while true; do sleep 1; done"]
    depends_on: [db]
volumes:
  data: {}
`

// The generated file names what runs, its bind relative to where the file lands, and compose
// creates a compose project from it.
func TestToolsGenerateCompose_FileRebuildsTheComposeProject(t *testing.T) {
	// Arrange
	env := newComposeEnv(t, "gencompose", 10*time.Second)
	projectDir := t.TempDir()
	confDir := filepath.Join(projectDir, "conf")
	require.NoError(t, os.Mkdir(confDir, 0o755))
	project := env.up(t, generateComposeSpec, confDir)
	output := filepath.Join(projectDir, "generated.yaml")

	// Act
	out, err := arcaveCommandIn(t, projectDir, "tools", "generate-compose", "-p", env.project, "-o", output).Output()

	// Assert
	require.NoError(t, err, "%s", out)
	summary := decode[model.GenerateComposeResponse](t, out)
	assert.Equal(t, model.GenerateComposeResponse{
		ComposeProject: model.DockerComposeProjectName(env.project),
		Services:       []string{"db", "web"},
		Path:           output,
	}, summary)
	generated := loadComposeFile(t, output)
	assert.Equal(t, env.project, generated.Name)
	assert.Equal(t, "./conf", bindSource(parseComposeFile(t, output)), "the bind is relative to the file")
	assert.Equal(t, confDir, bindSource(generated))
	db := generated.Services["db"]
	assert.Equal(t, "a$b", *db.Environment["PASS"], "compose reads the value back as the container has it")
	assert.Equal(t, types.HealthCheckTest{"CMD-SHELL", `test -n "$PASS"`}, db.HealthCheck.Test)

	// The file pins its volume and network names to the compose project, so it is put back as that one.
	require.NoError(t, env.compose.Down(env.ctx, project))
	require.NoError(t, env.compose.Create(env.ctx, generated), "compose creates the project from the generated file")
}

// loadComposeFile loads path as the docker CLI would, its relative paths resolved against its directory.
func loadComposeFile(t *testing.T, path string) *types.Project {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	project, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
		WorkingDir:  filepath.Dir(path),
		ConfigFiles: []types.ConfigFile{{Filename: path, Content: content}},
	})
	require.NoError(t, err)
	return project
}

// parseComposeFile is path as the file spells it, before any loader resolves its paths.
func parseComposeFile(t *testing.T, path string) *types.Project {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	project, err := composespec.NewComposeFileCodec().Parse(context.Background(), content)
	require.NoError(t, err)
	return project
}

// bindSource is the source of db's bind mount, "" when it has none.
func bindSource(project *types.Project) string {
	for _, volume := range project.Services["db"].Volumes {
		if volume.Type == types.VolumeTypeBind {
			return volume.Source
		}
	}
	return ""
}
