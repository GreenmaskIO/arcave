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
	"io/fs"
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

// TestEncryption_ReadersNeverMark turns encryption on over plain data: every command is refused
// and writes nothing, and with encryption off again the data is where it was. The subtests run in
// order over one project.
func TestEncryption_ReadersNeverMark(t *testing.T) {
	t.Parallel()

	env := newStatusEnv(t)
	env.run(t, "savepoint", "create", "plain-1")
	encrypted := newEncryptionEnv(t)
	before := readProjectTree(t, env)

	t.Run("status is partial", func(t *testing.T) {
		// Act
		out, err := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "status", "--format", "json")

		// Assert
		var exitErr *exec.ExitError
		require.True(t, errors.As(err, &exitErr), "status over plain data is partial: %v", err)
		assert.Equal(t, 1, exitErr.ExitCode())
		status := decode[model.ComposeProjectStatus](t, out)
		require.Len(t, status.Unreadable, 1)
		assert.Contains(t, status.Unreadable[0].Error, model.ErrStoreHoldsPlainData.Error())
		assert.Equal(t, before, readProjectTree(t, env), "status writes nothing")
	})

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
	for _, command := range commands {
		t.Run(command.name+" is refused", func(t *testing.T) {
			// Act
			_, err := runArcaveWith(t, env.projectRoot, encrypted, command.args...)

			// Assert
			var exitErr *exec.ExitError
			require.True(t, errors.As(err, &exitErr), "refused: %v", err)
			assert.Contains(t, string(exitErr.Stderr), model.ErrStoreHoldsPlainData.Error())
			assert.Equal(t, before, readProjectTree(t, env), "a refusal writes nothing")
		})
	}

	t.Run("encryption off again rolls back", func(t *testing.T) {
		// Act
		_, err := runArcaveWith(t, env.projectRoot, nil, "-p", env.project, "rollback", "plain-1")

		// Assert
		require.NoError(t, err)
		assert.NoFileExists(t, encryptionMarkerPath(env))
	})
}

// TestEncryption_OnlyAWriterMarksAnEmptyProject opens an empty arcave project under encryption: a
// reader writes nothing, the first writer marks it. The subtests run in order over one project.
func TestEncryption_OnlyAWriterMarksAnEmptyProject(t *testing.T) {
	t.Parallel()

	env := newStatusEnv(t)
	encrypted := newEncryptionEnv(t)

	t.Run("status writes nothing", func(t *testing.T) {
		// Act
		_, err := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "status", "--format", "json")

		// Assert: with no record to read the status is whole or partial.
		var exitErr *exec.ExitError
		if err != nil {
			require.True(t, errors.As(err, &exitErr), "status: %v", err)
			assert.Equal(t, 1, exitErr.ExitCode(), "status: %s", exitErr.Stderr)
		}
		assert.Empty(t, readProjectTree(t, env))
	})

	t.Run("savepoint list writes nothing", func(t *testing.T) {
		// Act
		_, err := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "savepoint", "list")

		// Assert
		require.NoError(t, err)
		assert.Empty(t, readProjectTree(t, env))
	})

	t.Run("a gc dry run, which takes the session's lock, marks the project", func(t *testing.T) {
		// Act
		_, err := runArcaveWith(t, env.projectRoot, encrypted, "gc", "--dry-run")

		// Assert
		require.NoError(t, err)
		assert.FileExists(t, encryptionMarkerPath(env))
	})

	t.Run("a capture writes into the marked project", func(t *testing.T) {
		// Act
		_, err := runArcaveWith(t, env.projectRoot, encrypted, "-p", env.project, "savepoint", "create", "encrypted-1")

		// Assert
		require.NoError(t, err)
	})
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
	return filepath.Join(arcaveProjectDir(env), encryptionmarker.FileName)
}

func arcaveProjectDir(env *statusEnv) string {
	return filepath.Join(env.storeDir, "arcave", "projects", statusArcaveProject)
}

// readProjectTree is every file of the arcave project with its contents; none for a project never
// written to.
func readProjectTree(t *testing.T, env *statusEnv) map[string]string {
	t.Helper()

	tree := map[string]string{}
	err := filepath.WalkDir(arcaveProjectDir(env), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[path] = string(data)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return tree
	}
	require.NoError(t, err)
	return tree
}
