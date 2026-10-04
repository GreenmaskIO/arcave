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

package storefactory

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// The segments a case expects to find between the configured path and the store
// root. The project and the stack differ on purpose: they are separate concepts,
// and a test whose two levels read alike would pass with them swapped.
const (
	testProject = model.ProjectName("demo")
	testStack   = model.DockerComposeProjectName("mystack")
)

// directoryStorage returns the directory arm of the storage union, pointed at path.
func directoryStorage(path string) config.Storage {
	return config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: path},
	}
}

// newStack builds the store every case works over: the full layout, with only
// the configured path varying.
func newStack(t *testing.T, cfg config.Storage, projectRoot string) (storages.Storager, error) {
	t.Helper()
	return New(cfg, projectRoot, testProject, model.DefaultSession, testStack)
}

// layout is the path below the configured one that every store is rooted at.
func layout() string {
	return filepath.Join("arcave",
		"projects", testProject.String(),
		"sessions", model.DefaultSession.String(),
		"compose", testStack.String())
}

func TestNew(t *testing.T) {
	t.Run("a relative path is resolved against the project root and namespaced", func(t *testing.T) {
		// Arrange: nothing under root exists yet — the whole configured path is
		// the store's prefix, which the backend creates on open.
		root := t.TempDir()

		// Act
		stack, err := newStack(t, directoryStorage(filepath.Join(".arcave", "storage")), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, stack.Close()) })
		namespace := filepath.Join(root, ".arcave", "storage", "arcave")
		assert.Equal(t, filepath.Join(root, ".arcave", "storage", layout()), stack.GetCwd())
		assert.DirExists(t, namespace)
		// The segments below the namespace do not exist yet: they arrive with the
		// first object written under them, since the backend creates the parents of
		// every key.
		assert.NoDirExists(t, stack.GetCwd())
	})

	t.Run("an absolute path is honoured verbatim, under the namespace", func(t *testing.T) {
		// Arrange: the absolute directory itself must exist; only the namespace
		// inside it is created.
		root, elsewhere := t.TempDir(), t.TempDir()
		abs := filepath.Join(elsewhere, "snaps")
		require.NoError(t, os.MkdirAll(abs, 0o755))

		// Act
		stack, err := newStack(t, directoryStorage(abs), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, stack.Close()) })
		assert.Equal(t, filepath.Join(abs, layout()), stack.GetCwd())
		assert.DirExists(t, filepath.Join(abs, "arcave"))
	})

	// An absolute root is the one segment New never creates: a typo must fail
	// loudly instead of quietly growing a fresh empty tree.
	t.Run("a missing absolute path is refused, not created", func(t *testing.T) {
		// Arrange
		abs := filepath.Join(t.TempDir(), "no-such-store")

		// Act
		stack, err := newStack(t, directoryStorage(abs), t.TempDir())

		// Assert
		require.ErrorIs(t, err, fs.ErrNotExist)
		assert.Nil(t, stack)
		assert.NoDirExists(t, abs)
	})

	// The namespace bounds every destructive operation to arcave-owned keys, and
	// the three segments under it are what keeps one configured path holding many
	// projects, sessions and stacks. Both are pinned by writing a real key.
	t.Run("the store is rooted under the namespace, the project, the session and the stack", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		stack, err := newStack(t, directoryStorage("snaps"), root)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, stack.Close()) })

		// Act
		err = stack.PutObject(context.Background(), "100/volume-named-demo_pgdata/data.tar", strings.NewReader("payload"))

		// Assert
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(root, "snaps", layout(), "100", "volume-named-demo_pgdata", "data.tar"))
	})

	// A cdc repository lives beside the savepoint directories rather than in a
	// tree of its own: the session is self-contained, so dropping it drops the
	// repositories with it.
	t.Run("a backend's own tree lives under the same stack root", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		stack, err := newStack(t, directoryStorage("snaps"), root)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, stack.Close()) })

		// Act
		restic, err := stack.SubStorage("restic", true)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, restic.Close()) })

		// Assert
		assert.Equal(t, filepath.Join(root, "snaps", layout(), "restic"), restic.GetCwd())
	})

	// The store is rooted at the stack, not at the namespace: nothing reached
	// through it can address a sibling project, session or stack, and the
	// library's guard — not a convention — is what enforces it.
	t.Run("the stack store cannot reach out of its own tree", func(t *testing.T) {
		// Arrange
		stack, err := newStack(t, directoryStorage("snaps"), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, stack.Close()) })

		// Act
		_, err = stack.SubStorage("../elsewhere", true)

		// Assert
		require.ErrorIs(t, err, storages.ErrUnsafeKey)
	})

	// Every segment is gated before a store is opened, each by the type that owns
	// its vocabulary. The messages differ, so a failure names which one was wrong.
	t.Run("an invalid segment is refused before a store is opened", func(t *testing.T) {
		// Only the segment under test is invalid in each case, so the message
		// pinned is the gate that fired.
		tests := []struct {
			name    string
			project model.ProjectName
			session model.SessionID
			stack   model.DockerComposeProjectName
			wantMsg string
		}{
			{name: "an empty project", project: "", session: model.DefaultSession, stack: testStack, wantMsg: "invalid project name"},
			{name: "a nested project", project: "a/b", session: model.DefaultSession, stack: testStack, wantMsg: "invalid project name"},
			{name: "an upper-case project", project: "MyApp", session: model.DefaultSession, stack: testStack, wantMsg: "invalid project name"},
			{name: "an escaping project", project: "..", session: model.DefaultSession, stack: testStack, wantMsg: "invalid project name"},
			{name: "an empty session", project: testProject, session: "", stack: testStack, wantMsg: "invalid session id"},
			{name: "a nested session", project: testProject, session: "a/b", stack: testStack, wantMsg: "invalid session id"},
			{name: "an empty stack", project: testProject, session: model.DefaultSession, stack: "", wantMsg: "invalid compose project name"},
			{name: "a nested stack", project: testProject, session: model.DefaultSession, stack: "a/b", wantMsg: "invalid compose project name"},
			{name: "an upper-case stack", project: testProject, session: model.DefaultSession, stack: "MyStack", wantMsg: "invalid compose project name"},
			{name: "an escaping stack", project: testProject, session: model.DefaultSession, stack: "..", wantMsg: "invalid compose project name"},
			{name: "a stack with a leading dash", project: testProject, session: model.DefaultSession, stack: "-stack", wantMsg: "invalid compose project name"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Act
				store, err := New(directoryStorage("snaps"), t.TempDir(), tt.project, tt.session, tt.stack)

				// Assert
				require.ErrorContains(t, err, tt.wantMsg)
				assert.Nil(t, store, "the gate stands in front of the store")
			})
		}
	})

	t.Run("an arm arcave does not implement is rejected", func(t *testing.T) {
		// Act
		_, err := newStack(t, config.Storage{Type: "s3"}, t.TempDir())

		// Assert
		require.ErrorContains(t, err, "unsupported storage.type")
	})
}
