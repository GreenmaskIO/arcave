package repo

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/rest/model"
)

// The repository over a real store, with no HTTP anywhere: these are the tests
// of what it promises the operations above it — the key an object lands under,
// which store failures become ErrObjectNotFound and ErrConfigExists, and the two
// places the protocol needs behaviour the Storager does not give it (an
// exclusive config write, an idempotent delete).

// A blob lands under a flat "<type>/<name>" key, where rest-server would shard
// it over data/xx. Everything else in this package recomputes that key rather
// than storing it, so this is the one place it is written down.
func TestRepository_SaveBlob(t *testing.T) {
	// Arrange
	repo, store := newRepository(t)
	ctx := context.Background()
	save := func(body string) error {
		return repo.SaveBlob(ctx, model.SaveBlobRequest{
			Type: model.TypeData, ID: blobID, Body: strings.NewReader(body),
		})
	}

	// Act: the same name twice — restic retries uploads, and the name is
	// content-addressed, so the second one overwrites rather than being refused.
	require.NoError(t, save("pack-bytes"))
	err := save("pack-bytes-again")

	// Assert
	require.NoError(t, err)
	st, err := store.Stat("data/" + string(blobID))
	require.NoError(t, err)
	require.True(t, st.Exist, "the blob is not at the flat key")
	assert.EqualValues(t, len("pack-bytes-again"), st.Size)
}

// StatBlob answers the exact size, and anything not at the flat key — absent
// or of another type — is ErrObjectNotFound.
func TestRepository_StatBlob(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		body       string
		objectType model.ObjectType
		blobName   model.ObjectID
		wantSize   int64
		wantErr    error
	}{
		{
			name: "a stored blob", key: "data/" + string(blobID), body: "0123456789",
			objectType: model.TypeData, blobName: blobID, wantSize: 10,
		},
		{
			name: "an empty blob", key: "locks/" + string(blobID),
			objectType: model.TypeLocks, blobName: blobID, wantSize: 0,
		},
		{
			name:       "a blob that is not there",
			objectType: model.TypeData, blobName: blobID, wantErr: model.ErrObjectNotFound,
		},
		{
			name: "a blob of another type", key: "index/" + string(blobID), body: "xy",
			objectType: model.TypeData, blobName: blobID, wantErr: model.ErrObjectNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo, store := newRepository(t)
			if tt.key != "" {
				put(t, store, tt.key, tt.body)
			}

			// Act
			size, err := repo.StatBlob(context.Background(), model.StatBlobRequest{Type: tt.objectType, ID: tt.blobName})

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

// A read is the interval the request names and nothing else — the bytes before
// the offset never travel, which is what lets restic pull one blob out of a
// pack. The zero-length interval is answered without asking the store: an empty
// blob has one, and every range over a zero-length object is invalid down there.
func TestRepository_OpenBlobRange(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		body     string
		blobName model.ObjectID
		offset   int64
		length   int64
		want     string
		wantErr  error
	}{
		{
			name: "the whole blob", key: "data/" + string(blobID), body: "0123456789",
			blobName: blobID, length: 10, want: "0123456789",
		},
		{
			name: "an interval inside it", key: "data/" + string(blobID), body: "0123456789",
			blobName: blobID, offset: 4, length: 6, want: "456789",
		},
		{
			// The handler stats before it reads, so a zero length always means an
			// empty object rather than an empty read of a missing one.
			name: "an empty blob", key: "data/" + string(blobID),
			blobName: blobID, length: 0, want: "",
		},
		{
			name: "a blob that is not there", blobName: otherBlobID,
			length: 4, wantErr: model.ErrObjectNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo, store := newRepository(t)
			if tt.key != "" {
				put(t, store, tt.key, tt.body)
			}

			// Act
			body, err := repo.OpenBlobRange(context.Background(), model.OpenBlobRangeRequest{
				Type: model.TypeData, ID: tt.blobName, Offset: tt.offset, Length: tt.length,
			})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			defer func() { require.NoError(t, body.Close()) }()
			got, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// The config is written once. A second write is ErrConfigExists and must leave
// the first one exactly as it was: this is what stops a restic init from
// silently re-initialising a repository that already holds data.
func TestRepository_SaveConfig(t *testing.T) {
	// Arrange
	repo, _ := newRepository(t)
	ctx := context.Background()

	// Act
	first := repo.SaveConfig(ctx, strings.NewReader("[repository]"))
	second := repo.SaveConfig(ctx, strings.NewReader("[other]"))

	// Assert
	require.NoError(t, first)
	require.ErrorIs(t, second, model.ErrConfigExists)

	src, err := repo.OpenConfig(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, src.Close()) }()
	got, err := io.ReadAll(src)
	require.NoError(t, err)
	assert.Equal(t, "[repository]", string(got), "the refused write must not have touched the config")
}

// A missing config is ErrObjectNotFound, same as any other absent object.
func TestRepository_OpenConfig_Missing(t *testing.T) {
	// Arrange
	repo, _ := newRepository(t)

	// Act
	_, err := repo.OpenConfig(context.Background())

	// Assert
	assert.ErrorIs(t, err, model.ErrObjectNotFound)
}

// StatConfig reports the config's size once it exists, ErrObjectNotFound before.
func TestRepository_StatConfig(t *testing.T) {
	// Arrange
	repo, store := newRepository(t)
	ctx := context.Background()

	// Act
	_, missing := repo.StatConfig(ctx)
	put(t, store, "config", "[repository]")
	size, err := repo.StatConfig(ctx)

	// Assert
	assert.ErrorIs(t, missing, model.ErrObjectNotFound)
	require.NoError(t, err)
	assert.EqualValues(t, len("[repository]"), size)
}

// Deleting is idempotent, where the Storager's own Delete is not: restic
// retries deletes during prune and unlock, and a retry that lands after the
// first attempt succeeded must not fail the operation.
func TestRepository_Delete(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		delete func(repo *Repository, ctx context.Context) error
	}{
		{
			name: "a blob", key: "data/" + string(blobID),
			delete: func(repo *Repository, ctx context.Context) error {
				return repo.DeleteBlob(ctx, model.DeleteBlobRequest{Type: model.TypeData, ID: blobID})
			},
		},
		{
			name: "a lock", key: "locks/" + string(blobID),
			delete: func(repo *Repository, ctx context.Context) error {
				return repo.DeleteBlob(ctx, model.DeleteBlobRequest{Type: model.TypeLocks, ID: blobID})
			},
		},
		{
			name: "the config", key: "config",
			delete: func(repo *Repository, ctx context.Context) error {
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

// A listing is the type's own flat keys with sizes — never a prefix-sharing
// sibling's, and [] when empty.
func TestRepository_ListBlobs(t *testing.T) {
	tests := []struct {
		name       string
		objectType model.ObjectType
		want       []model.Blob
	}{
		{
			name: "the blobs of one type", objectType: model.TypeData,
			want: []model.Blob{{Name: string(blobID), Size: 3}, {Name: string(otherBlobID), Size: 5}},
		},
		{
			name: "a type never matches its prefix-sharing sibling", objectType: model.TypeIndex,
			want: []model.Blob{{Name: string(blobID), Size: 2}},
		},
		{
			// Create makes no directories, so "absent" and "empty" are the same
			// state and a fresh repository must still be listable.
			name: "a type never written", objectType: model.TypeSnapshots, want: []model.Blob{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo, store := newRepository(t)
			put(t, store, "data/"+string(blobID), "abc")
			put(t, store, "data/"+string(otherBlobID), "abcde")
			put(t, store, "index/"+string(blobID), "xy")

			// Act
			blobs, err := repo.ListBlobs(context.Background(), model.ListBlobsRequest{Type: tt.objectType})

			// Assert
			require.NoError(t, err)
			require.NotNil(t, blobs, "an empty listing is [] on the wire, never null")
			assert.Equal(t, tt.want, blobs)
		})
	}
}

// A listing holds only this repository's own flat keys: anything nested under
// the type prefix was not written here and is not a blob of it.
func TestRepository_ListBlobs_SkipsNestedKeys(t *testing.T) {
	// Arrange
	repo, store := newRepository(t)
	put(t, store, "data/"+string(blobID), "abc")
	put(t, store, "data/aa/"+string(otherBlobID), "nested")

	// Act
	blobs, err := repo.ListBlobs(context.Background(), model.ListBlobsRequest{Type: model.TypeData})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Blob{{Name: string(blobID), Size: 3}}, blobs)
}
