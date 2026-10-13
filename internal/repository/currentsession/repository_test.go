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

package currentsession

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestRepository_PutGetClear(t *testing.T) {
	// Arrange
	repo := New(t.TempDir())
	ctx := context.Background()
	first, err := model.ParseSessionID("019e843e-1650-7000-8000-000000000000")
	require.NoError(t, err)

	// Act & Assert: absent, written, replaced, cleared, cleared again.
	_, found, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, repo.Put(ctx, first))
	got, found, err := repo.Get(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, first, got)

	require.NoError(t, repo.Put(ctx, model.DefaultSession))
	got, found, err = repo.Get(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, model.DefaultSession, got)

	require.NoError(t, repo.Clear(ctx))
	_, found, err = repo.Get(ctx)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, repo.Clear(ctx), "clearing an absent pointer is not an error")
}

func TestRepository_Get_HandWritten(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "no trailing newline", content: "019e843e-1650-7000-8000-000000000000"},
		{name: "surrounding whitespace", content: "  019e843e-1650-7000-8000-000000000000\n\n"},
		{name: "a name is not an id", content: "feat\n", wantErr: true},
		{name: "non-canonical spelling", content: "019E843E-1650-7000-8000-000000000000\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".arcave"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".arcave", pointerFile), []byte(tt.content), 0o644))

			// Act
			got, found, err := New(root).Get(context.Background())

			// Assert: a pointer that cannot be read refuses rather than reads as absent.
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidSessionID)
				assert.False(t, found)
				return
			}
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, "019e843e-1650-7000-8000-000000000000", got.String())
		})
	}
}

func TestRepository_Put_LeavesOnlyThePointer(t *testing.T) {
	// Arrange
	root := t.TempDir()
	repo := New(root)

	// Act
	require.NoError(t, repo.Put(context.Background(), model.DefaultSession))
	require.NoError(t, repo.Put(context.Background(), model.DefaultSession))

	// Assert
	entries, err := os.ReadDir(filepath.Join(root, ".arcave"))
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	assert.Equal(t, []string{pointerFile}, names, "no temp file is left behind")
}

func TestRepository_Get_NeverSeesAPartialPut(t *testing.T) {
	// Arrange
	repo := New(t.TempDir())
	ctx := context.Background()
	first, err := model.ParseSessionID("019e843e-1650-7000-8000-000000000000")
	require.NoError(t, err)
	ids := []model.SessionID{first, model.DefaultSession}
	require.NoError(t, repo.Put(ctx, first))
	const rounds = 500

	// Act: one writer alternates the pointer while the reader reads it.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range rounds {
			assert.NoError(t, repo.Put(ctx, ids[i%2]))
		}
	}()
	var got []model.SessionID
	var readErrs []error
	for range rounds {
		id, found, err := repo.Get(ctx)
		if err != nil || !found {
			readErrs = append(readErrs, err)
			continue
		}
		got = append(got, id)
	}
	wg.Wait()

	// Assert
	assert.Empty(t, readErrs, "every read finds a whole pointer")
	for _, id := range got {
		assert.Contains(t, ids, id)
	}
}
