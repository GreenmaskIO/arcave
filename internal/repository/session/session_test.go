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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
)

var (
	sessionA = model.SessionID(uuid.MustParse("0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"))
	sessionB = model.SessionID(uuid.MustParse("7d1e3b2a-4c5f-4a6e-8b9d-0e1f2a3b4c5d"))
)

// newStore returns a Storager rooted at a fresh temp dir, and that dir.
func newStore(t *testing.T) (storages.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	return st, root
}

// record is a session cloned from baseline.
func record(id model.SessionID) model.Session {
	return model.Session{
		ID:        id,
		Name:      "feat/add-new-table",
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		From:      &model.SessionOrigin{Session: model.DefaultSession},
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
		{name: "the nil session", id: model.SessionID{}, wantErr: "invalid session id"},
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

func TestRepository_LoadSession_Default(t *testing.T) {
	// Arrange
	store, _ := newStore(t)

	// Act
	got, found, err := New(store).LoadSession(context.Background(), model.DefaultSession)

	// Assert
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, model.DefaultSessionRecord, got)
}

func TestRepository_ListSessions(t *testing.T) {
	t.Run("returns committed records and skips what is not one", func(t *testing.T) {
		// Arrange
		ctx := context.Background()
		store, root := newStore(t)
		repo := New(store)
		require.NoError(t, repo.SaveSession(ctx, record(sessionA)))
		// The default session holds data but never a record; it is listed once, first.
		writeObject(t, root, "sessions/"+model.DefaultSession.String()+"/compose/shop/100/savepoint.json", "{}")
		// A session whose create died before committing.
		writeObject(t, root, "sessions/"+sessionB.String()+"/compose/shop/100/savepoint.json", "{}")
		writeObject(t, root, "sessions/not-a-session/session.json", "{}")

		// Act
		got, err := repo.ListSessions(ctx)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []model.Session{model.DefaultSessionRecord, record(sessionA)}, got)
	})

	t.Run("an empty store lists the default session", func(t *testing.T) {
		// Arrange
		store, _ := newStore(t)

		// Act
		got, err := New(store).ListSessions(context.Background())

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []model.Session{model.DefaultSessionRecord}, got)
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

func TestRepository_ListUncommittedSessions(t *testing.T) {
	// Arrange: a committed session, a tree without a record, the default session's tree and a
	// directory that is no session.
	ctx := context.Background()
	store, root := newStore(t)
	repo := New(store)
	require.NoError(t, repo.SaveSession(ctx, record(sessionA)))
	writeObject(t, root, "sessions/"+sessionB.String()+"/heartbeat.json", "{}")
	writeObject(t, root, "sessions/"+model.DefaultSession.String()+"/compose/shop/100/savepoint.json", "{}")
	writeObject(t, root, "sessions/not-a-session/heartbeat.json", "{}")

	// Act
	got, err := repo.ListUncommittedSessions(ctx)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.SessionID{sessionB}, got)
}

func TestRepository_SessionSize(t *testing.T) {
	// Arrange: two compose projects, a restic repository and a shared blob all count;
	// another session's objects do not.
	ctx := context.Background()
	store, root := newStore(t)
	dir := "sessions/" + sessionA.String() + "/"
	writeObject(t, root, dir+"session.json", "0123456789")
	writeObject(t, root, dir+"compose/shop/019e843e-1650-7000-8000-000000000100/volume-named-db/data.tar", "abcdefghij")
	writeObject(t, root, dir+"compose/blog/019e843e-1650-7000-8000-000000000200/savepoint.json", "{}")
	writeObject(t, root, dir+"compose/shop/restic/volume-named-db/data/ab/abcd", "packpack")
	writeObject(t, root, dir+"compose/shop/shared/oci/blobs/sha256/abcd", "blob")
	writeObject(t, root, "sessions/"+sessionB.String()+"/session.json", "not counted")
	repo := New(store)

	// Act
	got, err := repo.SessionSize(ctx, sessionA)
	absent, absentErr := repo.SessionSize(ctx, model.DefaultSession)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, int64(10+10+2+8+4), got)
	require.NoError(t, absentErr)
	assert.Zero(t, absent)
}

func TestRepository_CopySession(t *testing.T) {
	// Arrange: only the root record and the excepted heartbeat stay behind; the destination's own
	// heartbeat and a sibling prefixed by its id do not make it non-empty.
	ctx := context.Background()
	store, root := newStore(t)
	src := "sessions/" + sessionA.String() + "/"
	writeObject(t, root, src+"session.json", "the source's record")
	writeObject(t, root, src+"heartbeat.json", "the source's heartbeat")
	writeObject(t, root, "sessions/"+sessionB.String()+"/heartbeat.json", "the clone's heartbeat")
	writeObject(t, root, src+"compose/shop/019e843e-1650-7000-8000-000000000100/savepoint.json", "{}")
	writeObject(t, root, src+"compose/shop/019e843e-1650-7000-8000-000000000100/volume-named-db/data.tar", "abcdefghij")
	writeObject(t, root, src+"compose/shop/shared/oci/blobs/sha256/abcd", "blob")
	writeObject(t, root, src+"compose/shop/restic/volume-named-db/locks/1234", "lock")
	writeObject(t, root, src+"compose/blog/019e843e-1650-7000-8000-000000000200/session.json", "nested")
	writeObject(t, root, "sessions/"+sessionB.String()+"x/other", "a sibling")
	repo := New(store)

	// Act
	got, err := repo.CopySession(ctx, sessionA, sessionB, "heartbeat.json")

	// Assert
	require.NoError(t, err)
	want := treeOf(t, filepath.Join(root, "sessions", sessionA.String()))
	delete(want, "session.json")
	want["heartbeat.json"] = "the clone's heartbeat"
	assert.Equal(t, want, treeOf(t, filepath.Join(root, "sessions", sessionB.String())))
	assert.Equal(t, int64(2+10+4+4+6), got)
}

func TestRepository_CopySession_FromBaseline(t *testing.T) {
	// Arrange: the default session has no record, but has a tree.
	ctx := context.Background()
	store, root := newStore(t)
	writeObject(t, root, "sessions/"+model.DefaultSession.String()+"/compose/shop/x/data.tar", "bytes")

	// Act
	got, err := New(store).CopySession(ctx, model.DefaultSession, sessionB)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, int64(5), got)
	assert.Equal(t, map[string]string{"compose/shop/x/data.tar": "bytes"},
		treeOf(t, filepath.Join(root, "sessions", sessionB.String())))
}

func TestRepository_CopySession_Refused(t *testing.T) {
	tests := []struct {
		name     string
		to       model.SessionID
		plant    string
		wantErr  string
		occupied bool
	}{
		{name: "onto itself", to: sessionA, wantErr: "onto itself", occupied: true},
		{name: "onto the default session", to: model.DefaultSession, wantErr: "the default session has no record"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store, root := newStore(t)
			writeObject(t, root, "sessions/"+sessionA.String()+"/compose/shop/y/data.tar", "bytes")
			if tt.plant != "" {
				writeObject(t, root, tt.plant, "already here")
			}

			// Act
			_, err := New(store).CopySession(context.Background(), sessionA, tt.to)

			// Assert
			require.ErrorContains(t, err, tt.wantErr)
			assert.Equal(t, tt.occupied, errors.Is(err, model.ErrSessionTreeOccupied))
			assert.FileExists(t, filepath.Join(root, "sessions", sessionA.String(), "compose", "shop", "y", "data.tar"))
		})
	}
}

func TestRepository_CopySession_ObjectGoneMidCopy(t *testing.T) {
	// Arrange: CopyAll reports not found for a prefix that does hold objects.
	store, root := newStore(t)
	src := "sessions/" + sessionA.String() + "/"
	writeObject(t, root, src+"compose/shop/y/data.tar", "bytes")
	failing := failingCopyAll{Storager: store, prefix: src + "compose"}

	// Act
	_, err := New(failing).CopySession(context.Background(), sessionA, sessionB)

	// Assert
	require.ErrorIs(t, err, storages.ErrFileNotFound)
	assert.ErrorContains(t, err, "copy compose")
}

func TestRepository_CopySession_EmptyDirectoryCopiesNothing(t *testing.T) {
	// Arrange: a directory holding no object, which CopyAll refuses as not found.
	store, root := newStore(t)
	src := "sessions/" + sessionA.String() + "/"
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(src+"compose/shop/empty")), 0o755))
	writeObject(t, root, src+"loose.json", "{}")

	// Act
	got, err := New(store).CopySession(context.Background(), sessionA, sessionB)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"loose.json": "{}"}, treeOf(t, filepath.Join(root, "sessions", sessionB.String())))
	assert.Equal(t, int64(2), got)
}

func Test_rootKeys(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		except []string
		want   []string
	}{
		{name: "every file", files: []string{"a.json", "b"}, want: []string{"a.json", "b"}},
		{
			name:   "the record and the excepted keys stay behind",
			files:  []string{"heartbeat.json", "loose.json", "session.json"},
			except: []string{"heartbeat.json"},
			want:   []string{"loose.json"},
		},
		{name: "folder markers are not objects", files: []string{"", "compose/", "loose.json"}, want: []string{"loose.json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := rootKeys(tt.files, tt.except)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRepository_CopySession_LooseKeys(t *testing.T) {
	// Arrange: keys outside any compose/<name> tree are copied one by one, the excepted ones left.
	store, root := newStore(t)
	src := "sessions/" + sessionA.String() + "/"
	writeObject(t, root, src+"heartbeat.json", "the source's heartbeat")
	writeObject(t, root, src+"loose.json", "{}")
	writeObject(t, root, src+"compose/stray", "abc")
	writeObject(t, root, src+"compose/shop/x/data.tar", "bytes")

	// Act
	got, err := New(store).CopySession(context.Background(), sessionA, sessionB, "heartbeat.json")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"loose.json":              "{}",
		"compose/stray":           "abc",
		"compose/shop/x/data.tar": "bytes",
	}, treeOf(t, filepath.Join(root, "sessions", sessionB.String())))
	assert.Equal(t, int64(2+3+5), got)
}

func TestRepository_CopySession_CancelledCopiesNothing(t *testing.T) {
	// Arrange
	store, root := newStore(t)
	writeObject(t, root, "sessions/"+sessionA.String()+"/compose/shop/x/data.tar", "bytes")
	writeObject(t, root, "sessions/"+sessionA.String()+"/compose/web/y/data.tar", "bytes")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Act
	n, err := New(store).CopySession(ctx, sessionA, sessionB)

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, n)
	assert.NoDirExists(t, filepath.Join(root, "sessions", sessionB.String()))
}

func TestRepository_IsSessionTreeEmpty(t *testing.T) {
	tests := []struct {
		name  string
		plant []string
		want  bool
	}{
		{name: "an absent tree", want: true},
		{name: "a heartbeat alone", plant: []string{"heartbeat.json"}},
		{name: "a record alone", plant: []string{"session.json"}},
		{name: "data", plant: []string{"compose/shop/x/data.tar"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store, root := newStore(t)
			for _, name := range tt.plant {
				writeObject(t, root, "sessions/"+sessionB.String()+"/"+name, "x")
			}

			// Act
			got, err := New(store).IsSessionTreeEmpty(context.Background(), sessionB)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
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
	require.NoError(t, again, "deleting a deleted session is a no-op")
	assert.NoDirExists(t, filepath.Join(root, "sessions", sessionA.String()))
}

func TestRepository_DeleteSession_IncompleteSession(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := newStore(t)
	repo := New(store)
	writeObject(t, root, "sessions/"+sessionA.String()+"/compose/shop/x/data.tar", "bytes")

	// Act
	err := repo.DeleteSession(ctx, sessionA)

	// Assert
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(root, "sessions", sessionA.String()), "a session with no record is still dropped")
}

func TestRepository_DeleteSession_RecordFirst(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, _ := newStore(t)
	rec := &recordingStorage{Storager: store}
	repo := New(rec)
	require.NoError(t, repo.SaveSession(ctx, record(sessionA)))

	// Act
	err := repo.DeleteSession(ctx, sessionA)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"Delete sessions/" + sessionA.String() + "/session.json",
		"DeleteAll sessions/" + sessionA.String(),
	}, rec.calls)
}

func TestRepository_DeleteSession_RefusesDefault(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, _ := newStore(t)
	repo := New(store)

	// Act
	err := repo.DeleteSession(ctx, model.DefaultSession)

	// Assert
	require.Error(t, err)
}

// recordingStorage records the deletes it forwards, in order.
type recordingStorage struct {
	storages.Storager
	calls []string
}

func (s *recordingStorage) Delete(ctx context.Context, filePaths ...string) error {
	for _, p := range filePaths {
		s.calls = append(s.calls, "Delete "+p)
	}
	return s.Storager.Delete(ctx, filePaths...)
}

func (s *recordingStorage) DeleteAll(ctx context.Context, pathPrefix string) error {
	s.calls = append(s.calls, "DeleteAll "+pathPrefix)
	return s.Storager.DeleteAll(ctx, pathPrefix)
}

func TestRepository_ListSessionSavepoints(t *testing.T) {
	// Arrange: one name in two compose projects is two savepoints, not a conflict.
	ctx := context.Background()
	store, root := newStore(t)
	saveSavepoint := func(composeProject string, sp model.Savepoint) {
		sub, err := store.SubStorage("sessions/"+sessionA.String()+"/compose/"+composeProject, true)
		require.NoError(t, err)
		require.NoError(t, sprepo.New(sub).SaveSavepoint(ctx, sp))
	}
	shop := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000100", Name: "v1"}
	blog := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000200", Name: "v1"}
	saveSavepoint("shop", shop)
	saveSavepoint("blog", blog)
	// A compose project whose only savepoint never committed.
	writeObject(t, root, "sessions/"+sessionA.String()+"/compose/cache/019e843e-1650-7000-8000-000000000300/project.yaml",
		"services: {}")
	repo := New(store)

	// Act
	got, err := repo.ListSessionSavepoints(ctx, sessionA)
	empty, emptyErr := repo.ListSessionSavepoints(ctx, model.DefaultSession)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.ComposeProjectSavepoints{
		{ComposeProject: "blog", Savepoints: []model.Savepoint{blog}},
		{ComposeProject: "shop", Savepoints: []model.Savepoint{shop}},
	}, got)
	require.NoError(t, emptyErr)
	assert.Empty(t, empty)
}

// treeOf maps every file under dir, by its slash-separated path relative to dir, to its body.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = string(body)
		return nil
	}))
	return tree
}

// failingCopyAll is a store whose CopyAll of prefix reports it missing.
type failingCopyAll struct {
	storages.Storager
	prefix string
}

func (f failingCopyAll) CopyAll(
	ctx context.Context, srcPrefix, dstPrefix string, opts ...storages.CopyAllOption,
) (storages.CopyStat, error) {
	if srcPrefix == f.prefix {
		return storages.CopyStat{}, fmt.Errorf("%w: %s", storages.ErrFileNotFound, srcPrefix)
	}
	return f.Storager.CopyAll(ctx, srcPrefix, dstPrefix, opts...)
}
