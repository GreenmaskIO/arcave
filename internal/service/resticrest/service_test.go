package resticrest

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The repository over a real store, with no HTTP anywhere: these are the tests
// of what it promises the handler — the key an object lands under, which store
// failures become errObjectNotFound and errConfigExists, and the two places the
// protocol needs behaviour the Storager does not give it (an exclusive config
// write, an idempotent delete).

// newRepository returns the repository over a fresh directory store, plus that
// store so a test can plant objects behind the repository's back and read what
// it wrote.
func newRepository(t *testing.T) (*storeRepository, storages.Storager) {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{Path: t.TempDir()})
	require.NoError(t, err)
	return newStoreRepository(store), store
}

// A blob lands under a flat "<type>/<name>" key, where rest-server would shard
// it over data/xx. Everything else in this package recomputes that key rather
// than storing it, so this is the one place it is written down.
func TestStoreRepository_SaveBlob(t *testing.T) {
	// Arrange
	repo, store := newRepository(t)
	ctx := context.Background()

	// Act: the same name twice — restic retries uploads, and the name is
	// content-addressed, so the second one overwrites rather than being refused.
	require.NoError(t, repo.SaveBlob(ctx, "data", blobID, strings.NewReader("pack-bytes")))
	err := repo.SaveBlob(ctx, "data", blobID, strings.NewReader("pack-bytes-again"))

	// Assert
	require.NoError(t, err)
	st, err := store.Stat("data/" + blobID)
	require.NoError(t, err)
	require.True(t, st.Exist, "the blob is not at the flat key")
	assert.EqualValues(t, len("pack-bytes-again"), st.Size)
}

func TestStoreRepository_StatBlob(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		body       string
		objectType string
		blobName   string
		wantSize   int64
		wantErr    error
	}{
		{name: "a stored blob", key: "data/" + blobID, body: "0123456789", objectType: "data", blobName: blobID, wantSize: 10},
		{name: "an empty blob", key: "locks/" + blobID, objectType: "locks", blobName: blobID, wantSize: 0},
		{name: "a blob that is not there", objectType: "data", blobName: blobID, wantErr: errObjectNotFound},
		{name: "a blob of another type", key: "index/" + blobID, body: "xy", objectType: "data", blobName: blobID, wantErr: errObjectNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo, store := newRepository(t)
			if tt.key != "" {
				put(t, store, tt.key, tt.body)
			}

			// Act
			size, err := repo.StatBlob(context.Background(), tt.objectType, tt.blobName)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSize, size)
		})
	}
}

// An object that is not there is errObjectNotFound and never a file: the
// handler's 404 for a read is decided here, before a single byte is opened.
func TestStoreRepository_OpenBlob(t *testing.T) {
	// Arrange
	repo, store := newRepository(t)
	ctx := context.Background()
	put(t, store, "data/"+blobID, "0123456789")

	// Act
	f, err := repo.OpenBlob(ctx, "data", blobID)
	_, missing := repo.OpenBlob(ctx, "data", otherBlobID)

	// Assert
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	end, err := f.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	assert.EqualValues(t, 10, end, "the seeker must know the size without reading")

	_, err = f.Seek(4, io.SeekStart)
	require.NoError(t, err)
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "456789", string(got))

	assert.ErrorIs(t, missing, errObjectNotFound)
}

// The config is written once. A second write is errConfigExists and must leave
// the first one exactly as it was: this is what stops a restic init from
// silently re-initialising a repository that already holds data.
func TestStoreRepository_SaveConfig(t *testing.T) {
	// Arrange
	repo, _ := newRepository(t)
	ctx := context.Background()

	// Act
	first := repo.SaveConfig(ctx, strings.NewReader("[repository]"))
	second := repo.SaveConfig(ctx, strings.NewReader("[other]"))

	// Assert
	require.NoError(t, first)
	require.ErrorIs(t, second, errConfigExists)

	src, err := repo.OpenConfig(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, src.Close()) }()
	got, err := io.ReadAll(src)
	require.NoError(t, err)
	assert.Equal(t, "[repository]", string(got), "the refused write must not have touched the config")
}

func TestStoreRepository_OpenConfig_Missing(t *testing.T) {
	// Arrange
	repo, _ := newRepository(t)

	// Act
	_, err := repo.OpenConfig(context.Background())

	// Assert
	assert.ErrorIs(t, err, errObjectNotFound)
}

func TestStoreRepository_StatConfig(t *testing.T) {
	// Arrange
	repo, store := newRepository(t)
	ctx := context.Background()

	// Act
	_, missing := repo.StatConfig(ctx)
	put(t, store, "config", "[repository]")
	size, err := repo.StatConfig(ctx)

	// Assert
	assert.ErrorIs(t, missing, errObjectNotFound)
	require.NoError(t, err)
	assert.EqualValues(t, len("[repository]"), size)
}

// Deleting is idempotent, where the Storager's own Delete is not: restic
// retries deletes during prune and unlock, and a retry that lands after the
// first attempt succeeded must not fail the operation.
func TestStoreRepository_Delete(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		delete func(repo *storeRepository, ctx context.Context) error
	}{
		{
			name: "a blob", key: "data/" + blobID,
			delete: func(repo *storeRepository, ctx context.Context) error {
				return repo.DeleteBlob(ctx, "data", blobID)
			},
		},
		{
			name: "a lock", key: "locks/" + blobID,
			delete: func(repo *storeRepository, ctx context.Context) error {
				return repo.DeleteBlob(ctx, "locks", blobID)
			},
		},
		{
			name: "the config", key: "config",
			delete: func(repo *storeRepository, ctx context.Context) error {
				return repo.DeleteConfig(ctx)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo, store := newRepository(t)
			ctx := context.Background()
			put(t, store, tt.key, "payload")

			// Act
			first := tt.delete(repo, ctx)
			second := tt.delete(repo, ctx)

			// Assert
			require.NoError(t, first)
			assert.NoError(t, second, "deleting what is already gone is a success")

			exists, err := store.Exists(ctx, tt.key)
			require.NoError(t, err)
			assert.False(t, exists)
		})
	}
}

func TestStoreRepository_ListBlobs(t *testing.T) {
	tests := []struct {
		name       string
		objectType string
		want       []blob
	}{
		{
			name: "the blobs of one type", objectType: "data",
			want: []blob{{Name: blobID, Size: 3}, {Name: otherBlobID, Size: 5}},
		},
		{
			name: "a type never matches its prefix-sharing sibling", objectType: "index",
			want: []blob{{Name: blobID, Size: 2}},
		},
		{
			// Create makes no directories, so "absent" and "empty" are the same
			// state and a fresh repository must still be listable.
			name: "a type never written", objectType: "snapshots", want: []blob{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo, store := newRepository(t)
			put(t, store, "data/"+blobID, "abc")
			put(t, store, "data/"+otherBlobID, "abcde")
			put(t, store, "index/"+blobID, "xy")

			// Act
			blobs, err := repo.ListBlobs(context.Background(), tt.objectType)

			// Assert
			require.NoError(t, err)
			require.NotNil(t, blobs, "an empty listing is [] on the wire, never null")
			assert.Equal(t, tt.want, blobs)
		})
	}
}

// A listing holds only this repository's own flat keys: anything nested under
// the type prefix was not written here and is not a blob of it.
func TestStoreRepository_ListBlobs_SkipsNestedKeys(t *testing.T) {
	// Arrange
	repo, store := newRepository(t)
	put(t, store, "data/"+blobID, "abc")
	put(t, store, "data/aa/"+otherBlobID, "nested")

	// Act
	blobs, err := repo.ListBlobs(context.Background(), "data")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []blob{{Name: blobID, Size: 3}}, blobs)
}
