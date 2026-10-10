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

package inflight

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestRepository_PutGetClear(t *testing.T) {
	// Arrange
	repo := New(t.TempDir())
	ctx := context.Background()
	marker := model.InflightMarker{
		Phase:  model.InflightRestoring,
		Target: "019e843e-1650-7000-8000-000000000000",
		Safety: "019e844b-7818-7000-8000-000000000000",
		At:     time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}

	// Act & Assert: absent, written, replaced, cleared, cleared again.
	_, found, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, repo.Put(ctx, marker))
	got, found, err := repo.Get(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, marker.Phase, got.Phase)
	assert.Equal(t, marker.Target, got.Target)
	assert.Equal(t, marker.Safety, got.Safety)
	assert.True(t, got.At.Equal(marker.At), "at = %v", got.At)

	marker.Phase = model.InflightRestoreDone
	require.NoError(t, repo.Put(ctx, marker))
	got, found, err = repo.Get(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, model.InflightRestoreDone, got.Phase)

	require.NoError(t, repo.Clear(ctx))
	_, found, err = repo.Get(ctx)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, repo.Clear(ctx), "clearing an absent marker is not an error")
}

func TestRepository_Get_CorruptMarker(t *testing.T) {
	// Arrange
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".arcave"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".arcave", markerFile), []byte("{torn"), 0o644))

	// Act
	_, _, err := New(root).Get(context.Background())

	// Assert: a marker that cannot be judged refuses rather than reads as absent.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding the inflight marker")
}
