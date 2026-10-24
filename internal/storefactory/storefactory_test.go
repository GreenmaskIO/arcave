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
// root. The arcave project and the compose project differ on purpose: they are separate concepts,
// and a test whose two levels read alike would pass with them swapped.
const (
	testArcaveProject  = model.ArcaveProjectName("demo")
	testComposeProject = model.DockerComposeProjectName("shop")
)

// directoryStorage returns the directory arm of the storage union, pointed at path.
func directoryStorage(path string) config.Storage {
	return config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: path},
	}
}

// newComposeProjectStore builds the store every case works over: the full layout, with only
// the configured path varying.
func newComposeProjectStore(t *testing.T, cfg config.Storage, projectRoot string) (storages.Storager, error) {
	t.Helper()
	return New(t.Context(), cfg, projectRoot, testArcaveProject, model.DefaultSession, testComposeProject)
}

// layout is the path below the configured one that every store is rooted at.
func layout() string {
	return filepath.Join("arcave",
		"projects", testArcaveProject.String(),
		"sessions", model.DefaultSession.String(),
		"compose", testComposeProject.String())
}

func TestNew(t *testing.T) {
	t.Run("a relative path is resolved against the project root and namespaced", func(t *testing.T) {
		// Arrange: nothing under root exists yet — the whole configured path is
		// the store's prefix, which the backend creates on open.
		root := t.TempDir()

		// Act
		store, err := newComposeProjectStore(t, directoryStorage(filepath.Join(".arcave", "storage")), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, store.Close()) })
		namespace := filepath.Join(root, ".arcave", "storage", "arcave")
		assert.Equal(t, filepath.Join(root, ".arcave", "storage", layout()), store.GetCwd())
		assert.DirExists(t, namespace)
		// The segments below the namespace do not exist yet: they arrive with the
		// first object written under them, since the backend creates the parents of
		// every key.
		assert.NoDirExists(t, store.GetCwd())
	})

	t.Run("an absolute path is honoured verbatim, under the namespace", func(t *testing.T) {
		// Arrange: the absolute directory itself must exist; only the namespace
		// inside it is created.
		root, elsewhere := t.TempDir(), t.TempDir()
		abs := filepath.Join(elsewhere, "snaps")
		require.NoError(t, os.MkdirAll(abs, 0o755))

		// Act
		store, err := newComposeProjectStore(t, directoryStorage(abs), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, store.Close()) })
		assert.Equal(t, filepath.Join(abs, layout()), store.GetCwd())
		assert.DirExists(t, filepath.Join(abs, "arcave"))
	})

	// An absolute root is the one segment New never creates: a typo must fail
	// loudly instead of quietly growing a fresh empty tree.
	t.Run("a missing absolute path is refused, not created", func(t *testing.T) {
		// Arrange
		abs := filepath.Join(t.TempDir(), "no-such-store")

		// Act
		store, err := newComposeProjectStore(t, directoryStorage(abs), t.TempDir())

		// Assert
		require.ErrorIs(t, err, fs.ErrNotExist)
		assert.Nil(t, store)
		assert.NoDirExists(t, abs)
	})

	// The namespace bounds every destructive operation to arcave-owned keys, and
	// the three segments under it are what keeps one configured path holding many
	// projects, sessions and compose projects. Both are pinned by writing a real key.
	t.Run("the store is rooted under the namespace, the arcave project, the session and the compose project", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		store, err := newComposeProjectStore(t, directoryStorage("snaps"), root)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, store.Close()) })

		// Act
		err = store.PutObject(context.Background(), "100/volume-named-demo_pgdata/data.tar", strings.NewReader("payload"))

		// Assert
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(root, "snaps", layout(), "100", "volume-named-demo_pgdata", "data.tar"))
	})

	// A cdc repository lives beside the savepoint directories rather than in a
	// tree of its own: the session is self-contained, so dropping it drops the
	// repositories with it.
	t.Run("a backend's own tree lives under the same compose project root", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		store, err := newComposeProjectStore(t, directoryStorage("snaps"), root)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, store.Close()) })

		// Act
		restic, err := store.SubStorage("restic", true)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, restic.Close()) })

		// Assert
		assert.Equal(t, filepath.Join(root, "snaps", layout(), "restic"), restic.GetCwd())
	})

	// The store is rooted at the compose project, not at the namespace: nothing reached
	// through it can address a sibling arcave project, session or compose project, and the
	// library's guard — not a convention — is what enforces it.
	t.Run("the compose project store cannot reach out of its own tree", func(t *testing.T) {
		// Arrange
		store, err := newComposeProjectStore(t, directoryStorage("snaps"), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, store.Close()) })

		// Act
		_, err = store.SubStorage("../elsewhere", true)

		// Assert
		require.ErrorIs(t, err, storages.ErrUnsafeKey)
	})

	// Every segment is gated before a store is opened, each by the type that owns
	// its vocabulary. The messages differ, so a failure names which one was wrong.
	t.Run("an invalid segment is refused before a store is opened", func(t *testing.T) {
		// Only the segment under test is invalid in each case, so the message
		// pinned is the gate that fired.
		tests := []struct {
			name           string
			arcaveProject  model.ArcaveProjectName
			session        model.SessionID
			composeProject model.DockerComposeProjectName
			wantMsg        string
		}{
			{name: "an empty arcave project", arcaveProject: "", session: model.DefaultSession, composeProject: testComposeProject, wantMsg: "invalid arcave project name"},
			{name: "a nested arcave project", arcaveProject: "a/b", session: model.DefaultSession, composeProject: testComposeProject, wantMsg: "invalid arcave project name"},
			{name: "an upper-case arcave project", arcaveProject: "MyApp", session: model.DefaultSession, composeProject: testComposeProject, wantMsg: "invalid arcave project name"},
			{name: "an escaping arcave project", arcaveProject: "..", session: model.DefaultSession, composeProject: testComposeProject, wantMsg: "invalid arcave project name"},
			{name: "the nil session", arcaveProject: testArcaveProject, session: model.SessionID{}, composeProject: testComposeProject, wantMsg: "invalid session id"},
			{name: "an empty compose project", arcaveProject: testArcaveProject, session: model.DefaultSession, composeProject: "", wantMsg: "invalid compose project name"},
			{name: "a nested compose project", arcaveProject: testArcaveProject, session: model.DefaultSession, composeProject: "a/b", wantMsg: "invalid compose project name"},
			{name: "an upper-case compose project", arcaveProject: testArcaveProject, session: model.DefaultSession, composeProject: "MyShop", wantMsg: "invalid compose project name"},
			{name: "an escaping compose project", arcaveProject: testArcaveProject, session: model.DefaultSession, composeProject: "..", wantMsg: "invalid compose project name"},
			{name: "a compose project with a leading dash", arcaveProject: testArcaveProject, session: model.DefaultSession, composeProject: "-shop", wantMsg: "invalid compose project name"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Act
				store, err := New(t.Context(), directoryStorage("snaps"), t.TempDir(), tt.arcaveProject, tt.session, tt.composeProject)

				// Assert
				require.ErrorContains(t, err, tt.wantMsg)
				assert.Nil(t, store, "the gate stands in front of the store")
			})
		}
	})

	t.Run("an arm arcave does not implement is rejected", func(t *testing.T) {
		// Act
		_, err := newComposeProjectStore(t, config.Storage{Type: "gcs"}, t.TempDir())

		// Assert
		require.ErrorContains(t, err, "unsupported storage.type")
	})
}

func TestRoot_UnreachableStorage(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Storage
		wantMsg string
	}{
		{
			name: "s3",
			cfg: config.Storage{Type: config.StorageTypeS3, S3: &config.S3Storage{
				Endpoint: "http://127.0.0.1:1", Bucket: "savepoints", Region: "us-east-1",
				AccessKeyID: "key", SecretAccessKey: "secret", MaxRetries: 1,
			}},
			wantMsg: "reach s3 storage",
		},
		{
			name: "ssh",
			cfg: config.Storage{Type: config.StorageTypeSSH, SSH: &config.SSHStorage{
				Host: "127.0.0.1", Port: 1, User: "arcave", Password: "secret",
			}},
			wantMsg: "reach ssh storage",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			store, err := Root(t.Context(), tt.cfg, t.TempDir())

			// Assert
			require.ErrorContains(t, err, tt.wantMsg)
			assert.Nil(t, store)
		})
	}
}

func TestDescent(t *testing.T) {
	t.Run("each step descends one segment and the chain equals New", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		rootStore, err := Root(t.Context(), directoryStorage("snaps"), root)
		require.NoError(t, err)

		// Act
		arcaveProjectStore, err := ArcaveProject(rootStore, testArcaveProject)
		require.NoError(t, err)
		sessionStore, err := Session(arcaveProjectStore, model.DefaultSession)
		require.NoError(t, err)
		composeProjectStore, err := ComposeProject(sessionStore, testComposeProject)
		require.NoError(t, err)

		// Assert
		base := filepath.Join(root, "snaps", "arcave")
		assert.Equal(t, base, rootStore.GetCwd())
		assert.Equal(t, filepath.Join(base, "projects", testArcaveProject.String()), arcaveProjectStore.GetCwd())
		assert.Equal(t, filepath.Join(base, "projects", testArcaveProject.String(), "sessions", model.DefaultSession.String()), sessionStore.GetCwd())
		assert.Equal(t, filepath.Join(root, "snaps", layout()), composeProjectStore.GetCwd())
	})

	t.Run("each step gates its own segment", func(t *testing.T) {
		// Arrange
		rootStore, err := Root(t.Context(), directoryStorage("snaps"), t.TempDir())
		require.NoError(t, err)

		// Act
		_, projectErr := ArcaveProject(rootStore, "..")
		_, sessionErr := Session(rootStore, model.SessionID{})
		_, composeErr := ComposeProject(rootStore, "MyShop")

		// Assert
		assert.ErrorContains(t, projectErr, "invalid arcave project name")
		assert.ErrorContains(t, sessionErr, "invalid session id")
		assert.ErrorContains(t, composeErr, "invalid compose project name")
	})
}

func TestComposeProjects(t *testing.T) {
	t.Run("lists the compose projects of a session, sorted, skipping what is not one", func(t *testing.T) {
		// Arrange
		ctx := context.Background()
		root := t.TempDir()
		rootStore, err := Root(t.Context(), directoryStorage("snaps"), root)
		require.NoError(t, err)
		arcaveProjectStore, err := ArcaveProject(rootStore, testArcaveProject)
		require.NoError(t, err)
		sessionStore, err := Session(arcaveProjectStore, model.DefaultSession)
		require.NoError(t, err)
		for _, key := range []string{"compose/shop/100/savepoint.json", "compose/blog/200/savepoint.json", "compose/Not_Compose/x"} {
			require.NoError(t, sessionStore.PutObject(ctx, key, strings.NewReader("{}")))
		}

		// Act
		names, err := ComposeProjects(ctx, sessionStore)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []model.DockerComposeProjectName{"blog", "shop"}, names)
	})

	t.Run("a session holding nothing yet lists empty", func(t *testing.T) {
		// Arrange
		rootStore, err := Root(t.Context(), directoryStorage("snaps"), t.TempDir())
		require.NoError(t, err)
		sessionStore, err := Session(rootStore, model.DefaultSession)
		require.NoError(t, err)

		// Act
		names, err := ComposeProjects(context.Background(), sessionStore)

		// Assert
		require.NoError(t, err)
		assert.Empty(t, names)
	})
}

func TestSessionLock(t *testing.T) {
	t.Run("the lock lies beside the session's tree", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		rootStore, err := Root(t.Context(), directoryStorage("snaps"), root)
		require.NoError(t, err)
		arcaveProjectStore, err := ArcaveProject(rootStore, testArcaveProject)
		require.NoError(t, err)

		// Act
		store, key, err := SessionLock(arcaveProjectStore, model.DefaultSession)

		// Assert
		require.NoError(t, err)
		base := filepath.Join(root, "snaps", "arcave", "projects", testArcaveProject.String(), "sessions")
		assert.Equal(t, base, store.GetCwd())
		assert.Equal(t, model.DefaultSession.String()+".lock.json", key)
	})

	t.Run("the nil session is refused", func(t *testing.T) {
		// Arrange
		rootStore, err := Root(t.Context(), directoryStorage("snaps"), t.TempDir())
		require.NoError(t, err)

		// Act
		_, _, err = SessionLock(rootStore, model.SessionID{})

		// Assert
		require.ErrorIs(t, err, model.ErrInvalidSessionID)
	})
}
