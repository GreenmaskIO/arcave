package directory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStorage returns a Storage rooted at a fresh temp dir, plus that dir.
func newStorage(t *testing.T) (*Storage, string) {
	t.Helper()
	root := t.TempDir()
	st, err := NewStorage(&Config{Path: root})
	require.NoError(t, err)
	return st, root
}

func TestStorage_PutObject(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "object at the store root", path: "manifest.json", body: `{"savepoints":[]}`},
		{name: "lazily creates nested parents", path: "100/db/export/data/data.tar", body: "payload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStorage(t)

			// Act
			err := st.PutObject(context.Background(), tt.path, strings.NewReader(tt.body))

			// Assert
			require.NoError(t, err)

			got, err := os.ReadFile(filepath.Join(root, tt.path))
			require.NoError(t, err)
			assert.Equal(t, tt.body, string(got))
		})
	}
}

// resolve is the gate every object-addressing method passes through. A key that
// names the store root, or anything outside it, must never reach a syscall.
func TestStorage_resolve(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantRel string // relative to the store root; empty means the key must be rejected
	}{
		{name: "empty key names the store root", key: ""},
		{name: "dot names the store root", key: "."},
		{name: "parent escapes the store", key: "../x"},
		{name: "absolute key is not store-relative", key: "/etc"},
		{name: "traversal through a child escapes the store", key: "a/../../b"},
		{name: "plain key", key: "ok/key", wantRel: "ok/key"},
		{name: "traversal that stays inside is fine", key: "a/../b", wantRel: "b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStorage(t)

			// Act
			got, err := st.resolve(tt.key)

			// Assert
			if tt.wantRel == "" {
				require.ErrorIs(t, err, ErrUnsafeKey)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(root, filepath.FromSlash(tt.wantRel)), got)
		})
	}
}

// The destructive methods are the reason resolve exists: a savepoint id read
// back from a hand-edited record must never be able to name the store root.
func TestStorage_DeleteAll_RejectsUnsafeKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "store root via empty key", key: ""},
		{name: "store root via dot", key: "."},
		{name: "ancestor of the store", key: "../.."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStorage(t)
			require.NoError(t, st.PutObject(context.Background(), "keep.txt", strings.NewReader("data")))

			// Act
			err := st.DeleteAll(context.Background(), tt.key)

			// Assert
			require.ErrorIs(t, err, ErrUnsafeKey)
			assert.DirExists(t, root, "the store root must survive")
			assert.FileExists(t, filepath.Join(root, "keep.txt"))
		})
	}
}

// A second Put replaces the object wholesale, even when the new body is shorter
// than the old one — O_TRUNC, not a partial overwrite.
func TestStorage_PutObject_Overwrite(t *testing.T) {
	// Arrange
	st, root := newStorage(t)
	ctx := context.Background()
	require.NoError(t, st.PutObject(ctx, "manifest.json", strings.NewReader("a much longer original body")))

	// Act
	err := st.PutObject(ctx, "manifest.json", strings.NewReader("short"))

	// Assert
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	require.NoError(t, err)
	assert.Equal(t, "short", string(got))
}
