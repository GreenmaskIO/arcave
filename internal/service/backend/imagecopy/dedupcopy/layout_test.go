package dedupcopy_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
)

const imageID = model.ImageDigestID("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

// The metadata members a real export carries; their names are the layout gate.
const (
	indexName     = "index.json"
	manifestName  = "manifest.json"
	ociLayoutName = "oci-layout"
)

func TestLayout_Store_KeepsEachBlobOnce(t *testing.T) {
	// Arrange
	shared, writable := []byte("a layer two replicas share"), []byte("the writable layer")
	archive := ociArchive(t, blob(shared), blob(shared), blob(writable))
	ctx := zerolog.Nop().WithContext(context.Background())
	capture, blobs := newStores(t)

	// Act
	size, err := dedupcopy.New(blobs).Store(ctx, capture, imageID, bytes.NewReader(archive))

	// Assert
	require.NoError(t, err)
	assert.Len(t, storedBlobs(t, blobs), 2, "the repeated blob is written once")
	assert.Less(t, size.Written, size.Image, "the repeat counts in size, not in stored")

	manifest, err := imagecopy.ReadManifest(ctx, capture, imagecopy.FormatBlobs)
	require.NoError(t, err)
	assert.Equal(t, imageID, manifest.ImageID)
	assert.Equal(t, []imagecopy.Member{
		{Name: "blobs/sha256/" + digestOf(shared), Kind: imagecopy.KindBlob, Digest: digestOf(shared)},
		{Name: "blobs/sha256/" + digestOf(shared), Kind: imagecopy.KindBlob, Digest: digestOf(shared)},
		{Name: "blobs/sha256/" + digestOf(writable), Kind: imagecopy.KindBlob, Digest: digestOf(writable)},
		{Name: indexName, Kind: imagecopy.KindMetadata},
		{Name: manifestName, Kind: imagecopy.KindMetadata},
		{Name: ociLayoutName, Kind: imagecopy.KindMetadata},
	}, manifest.Members, "the manifest names every member, in stream order")
}

func TestLayout_Store_RewritesABlobLeftShortByAKilledRun(t *testing.T) {
	// Arrange: a previous run died mid-blob, so the store holds a truncated one.
	body := []byte("a layer a killed run only half wrote")
	archive := ociArchive(t, blob(body))
	ctx := zerolog.Nop().WithContext(context.Background())
	capture, blobs := newStores(t)
	require.NoError(t, blobs.PutObject(ctx, "sha256/"+digestOf(body), strings.NewReader("half")))

	// Act
	_, err := dedupcopy.New(blobs).Store(ctx, capture, imageID, bytes.NewReader(archive))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, string(body), readObject(t, blobs, "sha256/"+digestOf(body)),
		"a short blob is rewritten, not adopted")
}

func TestLayout_Store_DropsABlobThatDoesNotVerify(t *testing.T) {
	// Arrange
	named := strings.Repeat("ab", 32)
	archive := ociArchive(t, member{name: "blobs/sha256/" + named, body: []byte("bytes of another layer")})
	ctx := zerolog.Nop().WithContext(context.Background())
	capture, blobs := newStores(t)

	// Act
	_, err := dedupcopy.New(blobs).Store(ctx, capture, imageID, bytes.NewReader(archive))

	// Assert
	require.ErrorContains(t, err, "hashes to")
	assert.Empty(t, storedBlobs(t, blobs), "it is not left under a good name")
}

func TestLayout_Store_RefusesAnArchiveItCannotAddress(t *testing.T) {
	tests := []struct {
		name    string
		archive func(t *testing.T) []byte
		wantMsg string
	}{
		{
			// Refused by the member names, before the metadata is ever counted.
			name: "the legacy layout of a daemon older than Docker 25",
			archive: func(t *testing.T) []byte {
				return rawArchive(t,
					member{name: "8fe2a1c9/", dir: true},
					member{name: "8fe2a1c9/VERSION", body: []byte("1.0")},
					member{name: "8fe2a1c9/layer.tar", body: []byte("layer")},
					member{name: manifestName, body: []byte("[]")},
				)
			},
			wantMsg: "is neither a blob nor metadata",
		},
		{
			// Docker writes oci-layout last, and dropping the terminator is what a
			// stream cut at a member boundary leaves: tar reports a clean EOF for it.
			name: "a stream cut short before the metadata docker writes last",
			archive: func(t *testing.T) []byte {
				full := rawArchive(t,
					blob([]byte("layer")),
					member{name: indexName, body: []byte("{}")},
					member{name: manifestName, body: []byte("[]")},
				)
				return full[:len(full)-tarTerminatorLen]
			},
			wantMsg: "carries no " + ociLayoutName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			capture, blobs := newStores(t)

			// Act
			_, err := dedupcopy.New(blobs).Store(ctx, capture, imageID, bytes.NewReader(tt.archive(t)))

			// Assert
			require.ErrorIs(t, err, model.ErrUnsupportedImageArchive)
			assert.ErrorContains(t, err, tt.wantMsg, "the other gate fired")
		})
	}
}

func TestLayout_Store_SerialisesConcurrentWritersOfOneBlob(t *testing.T) {
	// Arrange: the same archive stored twice at once, as a scaled service does.
	body := bytes.Repeat([]byte("shared layer bytes "), 4096)
	archive := ociArchive(t, blob(body))
	ctx := zerolog.Nop().WithContext(context.Background())
	first, blobs := newStores(t)
	second, err := first.SubStorage("second", true)
	require.NoError(t, err)
	layout := dedupcopy.New(blobs)

	// Act
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, dst := range []storages.Storager{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = layout.Store(ctx, dst, imageID, bytes.NewReader(archive))
		}()
	}
	wg.Wait()

	// Assert
	require.NoError(t, errors.Join(errs...))
	assert.Equal(t, string(body), readObject(t, blobs, "sha256/"+digestOf(body)),
		"neither writer truncated the other")
}

func TestLayout_Load_RendersTheArchiveMemberForMember(t *testing.T) {
	// Arrange
	archive := ociArchive(t, blob([]byte("base layer")), blob([]byte("writable layer")))
	ctx := zerolog.Nop().WithContext(context.Background())
	capture, blobs := newStores(t)
	layout := dedupcopy.New(blobs)
	_, err := layout.Store(ctx, capture, imageID, bytes.NewReader(archive))
	require.NoError(t, err)

	// Act
	var out bytes.Buffer
	err = layout.Load(ctx, capture, imageID, &out)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, membersOf(t, archive), membersOf(t, out.Bytes()),
		"the members come back under the same names, in the same order")
}

func TestLayout_Load_RefusesWhatItCannotAddress(t *testing.T) {
	tests := []struct {
		name     string
		manifest imagecopy.Manifest
		wantErr  error
		contains string
	}{
		{
			name: "a digest that would leave the blob store",
			manifest: imagecopy.Manifest{
				ImageID: imageID, Format: imagecopy.FormatBlobs,
				Members: []imagecopy.Member{
					{Name: "blobs/sha256/x", Kind: imagecopy.KindBlob, Digest: "../../escape"},
				},
			},
			wantErr:  model.ErrUnsupportedImageArchive,
			contains: "../../escape",
		},
		{
			name: "a metadata name that is not one of the archive's",
			manifest: imagecopy.Manifest{
				ImageID: imageID, Format: imagecopy.FormatBlobs,
				Members: []imagecopy.Member{{Name: "../secret", Kind: imagecopy.KindMetadata}},
			},
			wantErr: model.ErrUnsupportedImageArchive,
		},
		{
			name: "a kind this build does not know",
			manifest: imagecopy.Manifest{
				ImageID: imageID, Format: imagecopy.FormatBlobs,
				Members: []imagecopy.Member{{Name: indexName, Kind: "layer-v9"}},
			},
			wantErr: model.ErrUnsupportedImageArchive,
		},
		{
			name: "a capture holding another image",
			manifest: imagecopy.Manifest{
				ImageID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Format: imagecopy.FormatBlobs,
				Members: []imagecopy.Member{{Name: indexName, Kind: imagecopy.KindMetadata}},
			},
			wantErr: model.ErrSavepointImageMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			capture, blobs := newStores(t)
			require.NoError(t, imagecopy.WriteManifest(ctx, capture, tt.manifest))

			// Act
			err := dedupcopy.New(blobs).Load(ctx, capture, imageID, io.Discard)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			if tt.contains != "" {
				assert.ErrorContains(t, err, tt.contains, "the error names what it refused")
			}
		})
	}
}

// member is one entry of a test archive.
type member struct {
	name string
	body []byte
	dir  bool
}

// blob names a member the way the daemon does: by the digest of its own bytes.
func blob(body []byte) member {
	return member{name: "blobs/sha256/" + digestOf(body), body: body}
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ociArchive builds what a daemon of Docker 25 or newer exports: the two blob
// directories, the blobs, and the metadata members last.
func ociArchive(t *testing.T, blobs ...member) []byte {
	t.Helper()

	members := []member{{name: "blobs/", dir: true}, {name: "blobs/sha256/", dir: true}}
	members = append(members, blobs...)
	return rawArchive(t, append(members,
		member{name: indexName, body: []byte(`{"schemaVersion":2}`)},
		member{name: manifestName, body: []byte(`[{"Config":"config"}]`)},
		member{name: ociLayoutName, body: []byte(`{"imageLayoutVersion":"1.0.0"}`)},
	)...)
}

// tarTerminatorLen is the two zero blocks tar.Writer.Close appends; cutting them
// off leaves the stream ending exactly at a member boundary.
const tarTerminatorLen = 2 * 512

func rawArchive(t *testing.T, members ...member) []byte {
	t.Helper()

	var buf bytes.Buffer
	archive := tar.NewWriter(&buf)
	for _, m := range members {
		header := &tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg}
		if m.dir {
			header = &tar.Header{Name: m.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		require.NoError(t, archive.WriteHeader(header))
		if !m.dir {
			_, err := archive.Write(m.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, archive.Close())
	return buf.Bytes()
}

// membersOf reduces an archive to name and content digest per file member, so two
// archives compare on what they carry rather than on their tar padding.
func membersOf(t *testing.T, archive []byte) []string {
	t.Helper()

	var got []string
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		require.NoError(t, err)
		if header.Typeflag == tar.TypeDir {
			continue
		}
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		got = append(got, fmt.Sprintf("%s=%s", header.Name, digestOf(body)))
	}
}

func newStores(t *testing.T) (capture, blobs storages.Storager) {
	t.Helper()

	capture, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	blobs, err = directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	return capture, blobs
}

func storedBlobs(t *testing.T, store storages.Storager) []string {
	t.Helper()

	objects, err := store.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(objects))
	for _, object := range objects {
		names = append(names, object.Name)
	}
	return names
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
