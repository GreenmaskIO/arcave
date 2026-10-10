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

package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	sessionA = model.SessionID("0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31")
	sessionB = model.SessionID("7d1e3b2a-4c5f-4a6e-8b9d-0e1f2a3b4c5d")
)

// newStore returns a Storager rooted at a fresh temp dir, and that dir.
func newStore(t *testing.T) (storages.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	return st, root
}

// record is a session forked from a baseline savepoint.
func record(id model.SessionID) model.Session {
	return model.Session{
		ID:        id,
		Name:      "feat/add-new-table",
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		From:      &model.SessionOrigin{Session: model.DefaultSession, Savepoint: "0199a92e-2e00-7000-8000-000000000000"},
	}
}

// writeObject plants an object at key, relative to the store root.
func writeObject(t *testing.T, root, key, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

func TestRepository_SaveSession(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := newStore(t)
	repo := New(store)

	// Act
	err := repo.SaveSession(ctx, record(sessionA))

	// Assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, "sessions", sessionA.String(), "session.json"))
	got, found, err := repo.LoadSession(ctx, sessionA)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, record(sessionA), got)
}

func TestRepository_SaveSession_RejectsInvalidID(t *testing.T) {
	tests := []struct {
		name    string
		id      model.SessionID
		wantErr string
	}{
		{name: "the default session", id: model.DefaultSession, wantErr: "the default session has no record"},
		{name: "not a uuid", id: "feature-42", wantErr: "invalid session id"},
		{name: "escaping", id: "..", wantErr: "invalid session id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store, root := newStore(t)

			// Act
			err := New(store).SaveSession(context.Background(), record(tt.id))

			// Assert
			require.ErrorContains(t, err, tt.wantErr)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestRepository_LoadSession_Missing(t *testing.T) {
	// Arrange
	store, _ := newStore(t)

	// Act
	_, found, err := New(store).LoadSession(context.Background(), sessionA)

	// Assert
	require.NoError(t, err)
	assert.False(t, found)
}

func TestRepository_ListSessions(t *testing.T) {
	t.Run("returns committed records and skips what is not one", func(t *testing.T) {
		// Arrange
		ctx := context.Background()
		store, root := newStore(t)
		repo := New(store)
		require.NoError(t, repo.SaveSession(ctx, record(sessionA)))
		// The default session holds data but never a record.
		writeObject(t, root, "sessions/baseline/compose/shop/100/savepoint.json", "{}")
		// A session whose create died before committing.
		writeObject(t, root, "sessions/"+sessionB.String()+"/compose/shop/100/savepoint.json", "{}")
		writeObject(t, root, "sessions/not-a-session/session.json", "{}")

		// Act
		got, err := repo.ListSessions(ctx)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []model.Session{record(sessionA)}, got)
	})

	t.Run("an empty store lists nothing", func(t *testing.T) {
		// Arrange
		store, _ := newStore(t)

		// Act
		got, err := New(store).ListSessions(context.Background())

		// Assert
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a corrupt record is an error, never a skip", func(t *testing.T) {
		// Arrange
		store, root := newStore(t)
		writeObject(t, root, "sessions/"+sessionA.String()+"/session.json", "{not json")

		// Act
		_, err := New(store).ListSessions(context.Background())

		// Assert
		require.ErrorContains(t, err, "parse session record")
	})
}

func TestRepository_DeleteSession(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := newStore(t)
	repo := New(store)
	require.NoError(t, repo.SaveSession(ctx, record(sessionA)))
	writeObject(t, root, "sessions/"+sessionA.String()+"/compose/shop/100/savepoint.json", "{}")

	// Act
	err := repo.DeleteSession(ctx, sessionA)
	again := repo.DeleteSession(ctx, sessionA)

	// Assert
	require.NoError(t, err)
	require.NoError(t, again, "deleting an uncommitted session is a no-op")
	assert.NoFileExists(t, filepath.Join(root, "sessions", sessionA.String(), "session.json"))
	assert.FileExists(t, filepath.Join(root, "sessions", sessionA.String(), "compose", "shop", "100", "savepoint.json"),
		"only the record goes; the data is the caller's to drop")
}
