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

package heartbeat

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStore returns a Storager rooted at a fresh temp dir, and that dir.
func newStore(t *testing.T) (storages.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	return st, root
}

// failingPut is a store whose every write fails.
type failingPut struct {
	storages.Storager
	err error
}

func (f failingPut) PutObject(context.Context, string, io.Reader) error { return f.err }

func TestWriter_Write(t *testing.T) {
	// Arrange
	ctx := context.Background()
	st, root := newStore(t)
	w := NewWriter(st)

	// Act
	err := w.Write(ctx, Heartbeat{Operation: "fork", Status: StatusInProgress, UpdatedAt: testAt})
	again := w.Write(ctx, Heartbeat{Operation: "fork", Status: StatusDone, UpdatedAt: testAt, FinishedAt: testAt})

	// Assert: the second write replaces the first, at the root of the tree.
	require.NoError(t, err)
	require.NoError(t, again)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, FileName, entries[0].Name())
	data, err := os.ReadFile(filepath.Join(root, FileName))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"status":"done"`)
}

func TestWriter_Write_InvalidStatusWritesNothing(t *testing.T) {
	// Arrange
	st, root := newStore(t)

	// Act
	err := NewWriter(st).Write(context.Background(), Heartbeat{Status: "paused"})

	// Assert
	require.ErrorIs(t, err, errInvalidStatus)
	assert.NoFileExists(t, filepath.Join(root, FileName))
}

func TestWriter_Write_StoreFails(t *testing.T) {
	// Arrange
	errPut := errors.New("store unreachable")

	// Act
	err := NewWriter(failingPut{err: errPut}).Write(context.Background(), Heartbeat{Status: StatusInProgress})

	// Assert
	require.ErrorIs(t, err, errPut)
}
