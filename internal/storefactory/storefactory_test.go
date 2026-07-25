package storefactory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
)

// directoryStorage returns the directory arm of the storage union, pointed at path.
func directoryStorage(path string) config.Storage {
	return config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: path},
	}
}

func TestNew(t *testing.T) {
	t.Run("a relative path is resolved against the project root, namespaced and created", func(t *testing.T) {
		// Arrange
		root := t.TempDir()

		// Act
		st, err := New(directoryStorage(filepath.Join(".arcave", "savepoints", "demo")), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, st.Close()) })
		want := filepath.Join(root, ".arcave", "savepoints", "demo", "arcave")
		assert.Equal(t, want, st.GetCwd())
		assert.DirExists(t, want, "the store must exist before the first PutObject")
	})

	t.Run("an absolute path is honoured verbatim, under the namespace", func(t *testing.T) {
		// Arrange
		root, elsewhere := t.TempDir(), t.TempDir()
		abs := filepath.Join(elsewhere, "snaps")

		// Act
		st, err := New(directoryStorage(abs), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, st.Close()) })
		assert.Equal(t, filepath.Join(abs, "arcave"), st.GetCwd())
	})

	// The namespace is what bounds every destructive operation to arcave-owned
	// keys: whatever the config names, the store — and therefore anything arcave
	// can ever delete through it — lives under <path>/arcave. This is the
	// invariant that replaced the validateStoreRoot blocklist, so it is pinned.
	t.Run("the store is rooted under the static arcave namespace", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		st, err := New(directoryStorage("snaps"), root)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, st.Close()) })

		// Act
		err = st.PutObject(context.Background(), "100/db/data/data.tar", strings.NewReader("payload"))

		// Assert
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(root, "snaps", "arcave", "100", "db", "data", "data.tar"))
	})

	t.Run("an arm arcave does not implement is rejected", func(t *testing.T) {
		// Act
		_, err := New(config.Storage{Type: "s3"}, t.TempDir())

		// Assert
		require.ErrorContains(t, err, "unsupported storage.type")
	})
}
