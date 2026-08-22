package savepoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"

	"github.com/greenmaskio/arcave/internal/model"
)

// newStore returns a Storager rooted at a fresh temp dir, plus that dir so a
// test can plant objects behind the repository's back — an incomplete namespace,
// a corrupt record — exactly as a crashed run would have left them.
func newStore(t *testing.T) (storages.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	return st, root
}

// record is a savepoint with one captured volume, for the id given. The stat's
// payload is nil: a typed one would not survive the round trip (it decodes back
// as a map), which is why nothing outside its own backend reads one.
func record(id model.SavepointID) model.Savepoint {
	return model.Savepoint{
		ID:        id,
		Name:      "before-migration",
		CreatedAt: time.Date(2026, 6, 1, 19, 41, 12, 0, time.UTC),
		Stats: []model.SnapshotStat{{
			Type: model.SnapshotBackendCopy,
			Volume: model.Volume{
				ObjectID: "volume-named-myapp_pgdata",
				Type:     model.VolumeTypeNamed,
				Backend:  model.SnapshotBackendCopy,
				Named: &model.VolumePropertiesNamed{
					VolumeName: "myapp_pgdata",
					UsedBy: []model.ServiceReference{
						{Service: "db", Target: "/var/lib/postgresql/data"},
					},
				},
			},
		}},
	}
}

// writeObject plants an object at key, relative to the store root.
func writeObject(t *testing.T, root, key, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

func TestRepository_Save(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := newStore(t)
	repo := New(store)
	want := record("1780335122")

	// Act
	saveErr := repo.SaveSavepoint(ctx, want)
	got, listErr := repo.ListSavepoints(ctx)

	// Assert
	require.NoError(t, saveErr)
	require.NoError(t, listErr)
	require.Len(t, got, 1)
	assert.Equal(t, want, got[0], "the record must survive a save/list round trip")

	// The record is the reserved key at the root of the savepoint's own namespace.
	assert.FileExists(t, filepath.Join(root, "1780335122", "savepoint.json"))
}

// An id that is not a Unix timestamp must never become a storage key: Save and
// Delete are the two writes, so both reject it at the boundary.
func TestRepository_Save_RejectsInvalidID(t *testing.T) {
	tests := []struct {
		name string
		id   model.SavepointID
	}{
		{name: "empty", id: ""},
		{name: "traversal", id: "../../etc"},
		{name: "a name", id: "before-migration"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store, root := newStore(t)
			repo := New(store)

			// Act
			saveErr := repo.SaveSavepoint(context.Background(), record(tt.id))
			deleteErr := repo.DeleteSavepoint(context.Background(), tt.id)

			// Assert
			require.Error(t, saveErr)
			require.Error(t, deleteErr)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			assert.Empty(t, entries, "nothing may be written for an invalid id")
		})
	}
}

func TestRepository_List(t *testing.T) {
	tests := []struct {
		name string
		// plant runs before List, seeding the store with objects a crashed run
		// would have left.
		plant   func(t *testing.T, root string)
		wantIDs []string
		wantMsg string // substring List's error must carry
	}{
		{
			name:    "an empty store lists nothing",
			plant:   func(*testing.T, string) {},
			wantIDs: nil,
		},
		{
			name: "a namespace with no record is an incomplete savepoint and is skipped",
			plant: func(t *testing.T, root string) {
				writeObject(t, root, "1780335999/db/data/data.tar", "payload")
			},
			wantIDs: nil,
		},
		{
			name: "a committed savepoint beside an incomplete one",
			plant: func(t *testing.T, root string) {
				writeObject(t, root, "1780335999/db/data/data.tar", "payload")
			},
			wantIDs: []string{"1780335122"},
		},
		{
			name: "a directory whose name is not a savepoint id is ignored",
			plant: func(t *testing.T, root string) {
				writeObject(t, root, "not-a-savepoint/savepoint.json", `{"id":"x"}`)
			},
			wantIDs: []string{"1780335122"},
		},
		{
			// cdc keeps its repositories beside the savepoint directories, under
			// the stack root this repository lists. "restic" is not a savepoint id,
			// so it is invisible here — which is what makes it a safe neighbour.
			name: "the cdc repository tree beside the savepoints is invisible",
			plant: func(t *testing.T, root string) {
				writeObject(t, root, "restic/volume-demo_pgdata/config", "{}")
				writeObject(t, root, "restic/volume-demo_pgdata/snapshots/abc", "{}")
			},
			wantIDs: []string{"1780335122"},
		},
		{
			name: "a corrupt record is an error, never a silent skip",
			plant: func(t *testing.T, root string) {
				writeObject(t, root, "1780335999/savepoint.json", "{not json")
			},
			wantMsg: "parse savepoint record 1780335999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := context.Background()
			store, root := newStore(t)
			repo := New(store)
			// Every case but the two that must fail carries the one committed
			// savepoint, so "skipped" is distinguishable from "store is empty".
			if len(tt.wantIDs) > 0 {
				require.NoError(t, repo.SaveSavepoint(ctx, record("1780335122")))
			}
			tt.plant(t, root)

			// Act
			got, err := repo.ListSavepoints(ctx)

			// Assert
			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
				return
			}
			require.NoError(t, err)
			ids := make([]string, 0, len(got))
			for _, r := range got {
				ids = append(ids, r.ID.String())
			}
			assert.Equal(t, tt.wantIDs, nilIfEmpty(ids))
		})
	}
}

func TestRepository_Delete(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := newStore(t)
	repo := New(store)
	require.NoError(t, repo.SaveSavepoint(ctx, record("1780335122")))
	writeObject(t, root, "1780335122/db/data/data.tar", "payload")

	// Act: the uncommit drops the marker and nothing else.
	err := repo.DeleteSavepoint(ctx, "1780335122")

	// Assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(root, "1780335122", "savepoint.json"))
	assert.FileExists(t, filepath.Join(root, "1780335122", "db", "data", "data.tar"),
		"Delete uncommits the savepoint; dropping its data is the backend's job")

	got, err := repo.ListSavepoints(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "an uncommitted savepoint must not list")

	// Delete is idempotent: a namespace with no record is already uncommitted.
	assert.NoError(t, repo.DeleteSavepoint(ctx, "1780335122"))
}

// nilIfEmpty normalises an empty slice to nil so a table can spell "no records"
// as a nil want.
func nilIfEmpty(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// The spec artifact round-trips verbatim under a reserved key beside the record.
func TestRepository_SaveSpecAndLoadSpec(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, _ := newStore(t)
	repo := New(store)
	spec := []byte("name: demo\nservices:\n  db:\n    image: postgres:16\n")

	// Act
	require.NoError(t, repo.SaveSpec(ctx, "1780335122", spec))
	got, err := repo.LoadSpec(ctx, "1780335122")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, spec, got)
	exists, err := store.Exists(ctx, "1780335122/project.yaml")
	require.NoError(t, err)
	assert.True(t, exists, "the artifact is a reserved key at the root of the savepoint's namespace")
}

// A namespace holding a spec but no record is an uncommitted savepoint.
func TestRepository_List_SkipsANamespaceWithOnlyASpec(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, _ := newStore(t)
	repo := New(store)
	require.NoError(t, repo.SaveSpec(ctx, "1780335122", []byte("name: demo\n")))

	// Act
	records, err := repo.ListSavepoints(ctx)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestRepository_LoadSpec_Missing(t *testing.T) {
	// Arrange
	store, _ := newStore(t)
	repo := New(store)

	// Act
	_, err := repo.LoadSpec(context.Background(), "1780335122")

	// Assert
	require.Error(t, err)
}
