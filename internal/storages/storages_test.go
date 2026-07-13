package storages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/config"
)

// directoryStorage returns the directory arm of the storage union, pointed at path.
func directoryStorage(path string) config.Storage {
	return config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: path},
	}
}

func TestNew(t *testing.T) {
	t.Run("a relative path is resolved against the project root and created", func(t *testing.T) {
		// Arrange
		root := t.TempDir()

		// Act
		st, err := New(directoryStorage(filepath.Join(".gm", "savepoints", "demo")), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, st.Close()) })
		want := filepath.Join(root, ".gm", "savepoints", "demo")
		assert.Equal(t, want, st.GetCwd())
		assert.DirExists(t, want, "the store must exist before the first PutObject")
	})

	t.Run("an absolute path is honoured verbatim", func(t *testing.T) {
		// Arrange
		root, elsewhere := t.TempDir(), t.TempDir()
		abs := filepath.Join(elsewhere, "snaps")

		// Act
		st, err := New(directoryStorage(abs), root)

		// Assert
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, st.Close()) })
		assert.Equal(t, abs, st.GetCwd())
	})

	t.Run("the returned store is already rooted at the store", func(t *testing.T) {
		// Arrange
		root := t.TempDir()
		st, err := New(directoryStorage("snaps"), root)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, st.Close()) })

		// Act
		err = st.PutObject(context.Background(), "100/db/data/data.tar", strings.NewReader("payload"))

		// Assert
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(root, "snaps", "100", "db", "data", "data.tar"))
	})

	t.Run("an arm GM does not implement is rejected", func(t *testing.T) {
		// Act
		_, err := New(config.Storage{Type: "s3"}, t.TempDir())

		// Assert
		require.ErrorContains(t, err, "unsupported storage.type")
	})
}

// A store root whose destruction would be catastrophic is refused before any
// directory is created — a savepoint_dir of /root must never become the place a
// `gm savepoint remove` operates relative to.
func TestValidateStoreRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name    string
		root    string
		wantMsg string // empty means the root must be accepted
	}{
		{name: "the filesystem root", root: "/", wantMsg: "system directory"},
		{name: "a shallow system directory", root: "/root", wantMsg: "system directory"},
		{name: "another shallow system directory", root: "/etc", wantMsg: "system directory"},
		{name: "the home directory", root: home, wantMsg: "home directory"},
		{name: "a deep path under a system directory", root: "/var/lib/gm/savepoints"},
		{name: "a sibling of a system directory", root: "/etcetera"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := validateStoreRoot(filepath.Clean(tt.root), t.TempDir())

			// Assert
			if tt.wantMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// The project root and its ancestors are refused too: GM deletes namespaces
// inside the store, and the store must not contain the code it is protecting.
func TestValidateStoreRoot_ProjectAncestors(t *testing.T) {
	parent := t.TempDir()
	projectRoot := filepath.Join(parent, "project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))

	tests := []struct {
		name   string
		root   string
		wantOK bool
	}{
		{name: "the project root itself", root: projectRoot},
		{name: "the project's parent", root: parent},
		{name: "a directory inside the project", root: filepath.Join(projectRoot, ".gm", "savepoints"), wantOK: true},
		{name: "a sibling of the project", root: filepath.Join(parent, "elsewhere"), wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := validateStoreRoot(tt.root, projectRoot)

			// Assert
			if tt.wantOK {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "contains the project")
		})
	}
}
