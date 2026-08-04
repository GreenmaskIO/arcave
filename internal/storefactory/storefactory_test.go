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

// directoryStorage returns the directory arm of the storage union, pointed at path.
func directoryStorage(path string) config.Storage {
	return config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: path},
	}
}

func TestNew(t *testing.T) {
	t.Run("a relative path is resolved against the project root and namespaced", func(t *testing.T) {
		// Arrange: nothing under root exists yet — the whole configured path is
		// the store's prefix, which the backend creates on open.
		root := t.TempDir()

		// Act
		session, err := New(directoryStorage(filepath.Join(".arcave", "savepoints", "demo")), root, model.DefaultSession)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, session.Close()) })
		store := filepath.Join(root, ".arcave", "savepoints", "demo", "arcave")
		assert.Equal(t, filepath.Join(store, model.DefaultSession.String()), session.GetCwd())
		assert.DirExists(t, store)
		// The session segment does not exist yet: it arrives with the first object
		// written under it, since the backend creates the parents of every key.
		assert.NoDirExists(t, session.GetCwd())
	})

	t.Run("an absolute path is honoured verbatim, under the namespace", func(t *testing.T) {
		// Arrange: the absolute directory itself must exist; only the namespace
		// inside it is created.
		root, elsewhere := t.TempDir(), t.TempDir()
		abs := filepath.Join(elsewhere, "snaps")
		require.NoError(t, os.MkdirAll(abs, 0o755))

		// Act
		session, err := New(directoryStorage(abs), root, model.DefaultSession)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, session.Close()) })
		assert.Equal(t, filepath.Join(abs, "arcave", model.DefaultSession.String()), session.GetCwd())
		assert.DirExists(t, filepath.Join(abs, "arcave"))
	})

	// An absolute root is the one segment New never creates: a typo must fail
	// loudly instead of quietly growing a fresh empty tree.
	t.Run("a missing absolute path is refused, not created", func(t *testing.T) {
		// Arrange
		abs := filepath.Join(t.TempDir(), "no-such-store")

		// Act
		session, err := New(directoryStorage(abs), t.TempDir(), model.DefaultSession)

		// Assert
		require.ErrorIs(t, err, fs.ErrNotExist)
		assert.Nil(t, session)
		assert.NoDirExists(t, abs)
	})

	// The namespace bounds every destructive operation to arcave-owned keys:
	// whatever the config names, the store lives under <path>/arcave. It is the
	// invariant that replaced the validateStoreRoot blocklist, so it is pinned.
	t.Run("the store is rooted under the static arcave namespace and the session", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		session, err := New(directoryStorage("snaps"), root, model.DefaultSession)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, session.Close()) })

		// Act
		err = session.PutObject(context.Background(), "100/volume-named-demo_pgdata/data.tar", strings.NewReader("payload"))

		// Assert
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(root, "snaps", "arcave", "global", "100", "volume-named-demo_pgdata", "data.tar"))
	})

	// The store is rooted at the session, not at the namespace: nothing reached
	// through it can address a sibling session, and the library's guard — not a
	// convention — is what enforces it.
	t.Run("the session store cannot reach out of its own tree", func(t *testing.T) {
		// Arrange
		session, err := New(directoryStorage("snaps"), t.TempDir(), model.DefaultSession)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, session.Close()) })

		// Act
		_, err = session.SubStorage("../elsewhere", true)

		// Assert
		require.ErrorIs(t, err, storages.ErrUnsafeKey)
	})

	t.Run("an invalid session is refused before a store is opened", func(t *testing.T) {
		tests := []struct {
			name    string
			session model.SessionID
		}{
			{name: "empty", session: ""},
			{name: "a nested path", session: "a/b"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Arrange
				root := t.TempDir()

				// Act
				session, err := New(directoryStorage("snaps"), root, tt.session)

				// Assert
				require.ErrorContains(t, err, "invalid session id")
				assert.Nil(t, session, "the gate stands in front of the store")
			})
		}
	})

	t.Run("an arm arcave does not implement is rejected", func(t *testing.T) {
		// Act
		_, err := New(config.Storage{Type: "s3"}, t.TempDir(), model.DefaultSession)

		// Assert
		require.ErrorContains(t, err, "unsupported storage.type")
	})
}

// NewShared resolves the same path the same way and roots at "shared" instead of
// a session: what lives there is a backend's own data, which outlives any one
// lifetime on purpose.
func TestNewShared(t *testing.T) {
	t.Run("the store sits beside the sessions, under the same namespace", func(t *testing.T) {
		// Arrange
		root := t.TempDir()

		// Act
		shared, err := NewShared(directoryStorage("snaps"), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, shared.Close()) })
		assert.Equal(t, filepath.Join(root, "snaps", "arcave", "shared"), shared.GetCwd())
	})

	// "shared" cannot collide with a session: SessionID.Validate admits only
	// "global" or a canonical UUID, and this pins that they are siblings rather
	// than the same tree.
	t.Run("a session store and the shared store are siblings, never the same", func(t *testing.T) {
		// Arrange
		root := t.TempDir()

		// Act
		session, err := New(directoryStorage("snaps"), root, model.DefaultSession)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, session.Close()) })
		shared, err := NewShared(directoryStorage("snaps"), root)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, shared.Close()) })

		// Assert
		assert.NotEqual(t, session.GetCwd(), shared.GetCwd())
		assert.Equal(t, filepath.Dir(session.GetCwd()), filepath.Dir(shared.GetCwd()))
	})

	t.Run("the shared store cannot reach out of its own tree", func(t *testing.T) {
		// Arrange
		shared, err := NewShared(directoryStorage("snaps"), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, shared.Close()) })

		// Act
		_, err = shared.SubStorage("../global", true)

		// Assert
		require.ErrorIs(t, err, storages.ErrUnsafeKey)
	})

	t.Run("an arm arcave does not implement is rejected", func(t *testing.T) {
		// Act
		_, err := NewShared(config.Storage{Type: "s3"}, t.TempDir())

		// Assert
		require.ErrorContains(t, err, "unsupported storage.type")
	})
}
