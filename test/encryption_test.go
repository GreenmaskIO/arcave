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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
)

// TestEncryption_ReadersNeverMark turns encryption on over an arcave project written without it:
// every command, reading or writing, is refused and none marks the project, so turning encryption
// off again finds the data where it was.
func TestEncryption_ReadersNeverMark(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newStatusEnv(t)
	env.run(t, "savepoint", "create", "plain-1")
	encrypted := newEncryptionEnv(t)
	commands := []struct {
		name string
		args []string
	}{
		{name: "savepoint list", args: []string{"-p", env.project, "savepoint", "list"}},
		{name: "session list", args: []string{"session", "list"}},
		{name: "session current", args: []string{"session", "current"}},
		{name: "gc dry run", args: []string{"gc", "--dry-run"}},
		{name: "savepoint create", args: []string{"-p", env.project, "savepoint", "create", "encrypted-1"}},
	}

	// Act
	statusOut, statusErr := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "status", "--format", "json")

	// Assert
	var exitErr *exec.ExitError
	require.True(t, errors.As(statusErr, &exitErr), "status over plain data is partial: %v", statusErr)
	assert.Equal(t, 1, exitErr.ExitCode())
	status := decode[model.ComposeProjectStatus](t, statusOut)
	require.Len(t, status.Unreadable, 1)
	assert.Contains(t, status.Unreadable[0].Error, model.ErrStoreHoldsPlainData.Error())
	assertUnmarked(t, env)

	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			// Act
			_, err := runArcaveWith(t, env.projectRoot, encrypted, command.args...)

			// Assert
			require.True(t, errors.As(err, &exitErr), "refused: %v", err)
			assert.Contains(t, string(exitErr.Stderr), model.ErrStoreHoldsPlainData.Error())
			assertUnmarked(t, env)
		})
	}

	// Act: encryption off again.
	env.run(t, "rollback", "plain-1")

	// Assert
	assertUnmarked(t, env)
}

// TestEncryption_OnlyAWriterMarksAnEmptyProject opens an arcave project holding nothing yet under
// encryption: a reader leaves it unmarked, the first writer — a gc dry run, which takes the
// session's lock — marks it, and a capture writes into it.
func TestEncryption_OnlyAWriterMarksAnEmptyProject(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newStatusEnv(t)
	encrypted := newEncryptionEnv(t)

	// Act
	_, statusErr := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "status", "--format", "json")
	_, listErr := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "savepoint", "list")

	// Assert: a status with no record to read is whole or partial, never a marker.
	var exitErr *exec.ExitError
	if statusErr != nil {
		require.True(t, errors.As(statusErr, &exitErr), "status: %v", statusErr)
		assert.Equal(t, 1, exitErr.ExitCode(), "status: %s", exitErr.Stderr)
	}
	require.NoError(t, listErr)
	assertUnmarked(t, env)

	// Act
	_, gcErr := runArcaveWith(t, env.projectRoot, encrypted, "gc", "--dry-run")

	// Assert
	require.NoError(t, gcErr)
	_, err := os.Stat(encryptionMarkerPath(env))
	require.NoError(t, err, "the first writer marks the project")

	// Act
	_, createErr := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "savepoint", "create", "encrypted-1")

	// Assert
	require.NoError(t, createErr)
}

// newEncryptionEnv is the environment that turns encryption on, to a fresh key.
func newEncryptionEnv(t *testing.T) []string {
	t.Helper()

	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return []string{"ARCAVE_ENCRYPTION_ENABLED=true", "ARCAVE_ENCRYPTION_KEY=" + identity.String()}
}

// runArcaveWith runs the built binary under env, its stderr kept on the exit error.
func runArcaveWith(t *testing.T, projectRoot string, env []string, args ...string) ([]byte, error) {
	t.Helper()

	cmd := arcaveCommandIn(t, projectRoot, args...)
	cmd.Env = append(cmd.Env, env...)
	return cmd.Output()
}

func encryptionMarkerPath(env *statusEnv) string {
	return filepath.Join(env.storeDir, "arcave", "projects", statusArcaveProject, encryptionmarker.FileName)
}

func assertUnmarked(t *testing.T, env *statusEnv) {
	t.Helper()

	_, err := os.Stat(encryptionMarkerPath(env))
	assert.ErrorIs(t, err, os.ErrNotExist, "no command marks the project")
}
