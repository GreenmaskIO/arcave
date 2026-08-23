package fullcopy_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/fullcopy"
)

const (
	imageID = model.ImageDigestID("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	archive = "a save archive, verbatim"
)

func TestLayout_Store(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	dst := newStore(t)

	// Act
	size, err := fullcopy.New().Store(ctx, dst, imageID, strings.NewReader(archive))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, imagecopy.StoredSize{Image: int64(len(archive)), Written: int64(len(archive))}, size,
		"nothing is deduplicated here, so every byte stored is a byte added")
	assert.Equal(t, archive, readObject(t, dst, "image.tar"))

	manifest, err := imagecopy.ReadManifest(ctx, dst, imagecopy.FormatArchive)
	require.NoError(t, err)
	assert.Equal(t, imageID, manifest.ImageID)
	assert.Equal(t, []imagecopy.Member{{Name: "image.tar", Kind: imagecopy.KindArchive}}, manifest.Members)
}

func TestLayout_Load(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := newStore(t)
	_, err := fullcopy.New().Store(ctx, store, imageID, strings.NewReader(archive))
	require.NoError(t, err)

	// Act
	var out bytes.Buffer
	err = fullcopy.New().Load(ctx, store, imageID, &out)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, archive, out.String(), "what went in comes back byte for byte")
}

func TestLayout_Load_RefusesWhatIsNotItsOwn(t *testing.T) {
	tests := []struct {
		name     string
		manifest *imagecopy.Manifest
		imageID  model.ImageDigestID
		wantErr  error
	}{
		{
			name: "a capture stored by the other layout",
			manifest: &imagecopy.Manifest{
				ImageID: imageID, Format: imagecopy.FormatBlobs,
				Members: []imagecopy.Member{{Name: "index.json", Kind: imagecopy.KindMetadata}},
			},
			imageID: imageID,
			wantErr: model.ErrUnsupportedImageArchive,
		},
		{
			name: "a capture holding another image",
			manifest: &imagecopy.Manifest{
				ImageID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Format: imagecopy.FormatArchive,
				Members: []imagecopy.Member{{Name: "image.tar", Kind: imagecopy.KindArchive}},
			},
			imageID: imageID,
			wantErr: model.ErrSavepointImageMissing,
		},
		{
			name:    "a capture with no manifest",
			imageID: imageID,
			wantErr: model.ErrSavepointImageMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			store := newStore(t)
			if tt.manifest != nil {
				require.NoError(t, imagecopy.WriteManifest(ctx, store, *tt.manifest))
			}

			// Act
			err := fullcopy.New().Load(ctx, store, tt.imageID, io.Discard)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func newStore(t *testing.T) storages.Storager {
	t.Helper()

	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	return store
}

func readObject(t *testing.T, store storages.Storager, key string) string {
	t.Helper()

	src, err := store.GetObject(context.Background(), key)
	require.NoError(t, err)
	defer func() { require.NoError(t, src.Close()) }()
	body, err := io.ReadAll(src)
	require.NoError(t, err)
	return string(body)
}
