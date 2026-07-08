package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/storages/directory"
)

// newStorager returns a real directory Storager rooted at a fresh temp dir
// standing in for .gm/tmp, plus that root.
func newStorager(t *testing.T) (model.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(&directory.Config{Path: root})
	require.NoError(t, err)
	return st, root
}

func TestSessionRelPath(t *testing.T) {
	// Arrange
	st, _ := newStorager(t)
	sess := NewSession(st, "1780335122")

	// Act
	rel := sess.RelPath()

	// Assert
	assert.Equal(t, filepath.Join(".gm", "tmp", "rollback-1780335122"), rel)
}

func TestSessionWriter(t *testing.T) {
	tests := []struct {
		name    string
		service string
		mount   string
		wantKey string
	}{
		{
			name:    "single segment mount",
			service: "db",
			mount:   "/data",
			wantKey: "rollback-100/db/data/data.tar",
		},
		{
			name:    "nested mount path stays nested",
			service: "app",
			mount:   "/export/data",
			wantKey: "rollback-100/app/export/data/data.tar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStorager(t)
			sess := NewSession(st, "100")

			// Act
			w, key := sess.Writer(tt.service, tt.mount)
			err := w.Write(context.Background(), strings.NewReader("payload"))

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, key)
			data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(tt.wantKey)))
			require.NoError(t, readErr)
			assert.Equal(t, "payload", string(data))
		})
	}
}

func TestSessionRemove(t *testing.T) {
	tests := []struct {
		name          string
		otherBackup   bool // a second rollback backup also present under the tmp root
		wantTmpPruned bool
	}{
		{name: "prunes empty tmp parent", otherBackup: false, wantTmpPruned: true},
		{name: "keeps tmp holding another backup", otherBackup: true, wantTmpPruned: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStorager(t)
			ctx := context.Background()
			sess := NewSession(st, "100")
			w, _ := sess.Writer("db", "/data")
			err := w.Write(ctx, strings.NewReader("x"))
			require.NoError(t, err)
			if tt.otherBackup {
				w2, _ := NewSession(st, "200").Writer("db", "/data")
				require.NoError(t, w2.Write(ctx, strings.NewReader("y")))
			}

			// Act
			err = sess.Remove(ctx)

			// Assert
			require.NoError(t, err)
			_, statErr := os.Stat(filepath.Join(root, "rollback-100"))
			assert.True(t, os.IsNotExist(statErr), "session directory must be gone")

			_, tmpErr := os.Stat(root)
			if tt.wantTmpPruned {
				assert.True(t, os.IsNotExist(tmpErr), "tmp root should be pruned when empty")
			} else {
				assert.NoError(t, tmpErr, "tmp root should survive while another backup remains")
			}
		})
	}
}
