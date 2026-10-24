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

package imagecopy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	inventorySavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000001")
	layerKey           = inventorySavepoint + "/images/db/replicas/1"
)

var (
	inventoryImage = model.ImageDigestID("sha256:" + strings.Repeat("1", 64))
	inventoryBlob  = strings.Repeat("a", 64)
	listedManifest = Manifest{
		ImageID: inventoryImage,
		Format:  FormatBlobs,
		Members: []Member{
			{Name: "blobs/sha256/" + inventoryBlob, Kind: KindBlob, Digest: inventoryBlob},
			{Name: "index.json", Kind: KindMetadata},
		},
	}
)

func TestNewStoredImages(t *testing.T) {
	// Arrange
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	dir := "shared/oci/images/sha256/" + strings.Repeat("1", 64)
	stray := "shared/oci/images/sha256/" + strings.Repeat("2", 64)
	objects := []storages.ObjectStat{
		{Name: dir + "/image.json", Size: 10, LastModified: at},
		{Name: dir + "/index.json", Size: 5, LastModified: at.Add(time.Hour)},
		{Name: stray + "/index.json", Size: 3, LastModified: at},
		{Name: "shared/oci/blobs/sha256/" + inventoryBlob, Size: 100, LastModified: at},
		{Name: string(layerKey) + "/image.json", Size: 7, LastModified: at},
	}

	// Act
	images := NewStoredImages(objects)

	// Assert
	assert.Equal(t, []StoredImage{
		{ID: inventoryImage, Key: dir, Bytes: 15, LastModified: at.Add(time.Hour), ManifestBytes: 10, HasManifest: true},
		{ID: model.ImageDigestID("sha256:" + strings.Repeat("2", 64)), Key: stray, Bytes: 3, LastModified: at},
	}, images)
}

func TestNewLayerManifests(t *testing.T) {
	// Arrange
	objects := []storages.ObjectStat{
		{Name: string(layerKey) + "/image.json", Size: 7},
		{Name: string(layerKey) + "/index.json", Size: 3},
		{Name: "shared/oci/images/sha256/" + strings.Repeat("1", 64) + "/image.json", Size: 4},
		{Name: "not-a-savepoint/images/db/replicas/1/image.json", Size: 5},
		{Name: inventorySavepoint.String() + "/volume-named-db/data.tar", Size: 6},
	}

	// Act
	manifests := NewLayerManifests(objects)

	// Assert: the tree is read off the key, gated by the caller.
	assert.Equal(t, []LayerManifest{
		{Savepoint: inventorySavepoint, Key: string(layerKey), Size: 7},
		{Savepoint: "not-a-savepoint", Key: "not-a-savepoint/images/db/replicas/1", Size: 5},
	}, manifests)
}

func TestReadListedBlobs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr error
	}{
		{
			name: "an intact manifest lists its blobs, not its metadata",
			body: mustMarshal(t, listedManifest),
			want: []string{inventoryBlob},
		},
		{
			name:    "a missing manifest is a missing image",
			wantErr: model.ErrSavepointImageMissing,
		},
		{
			name:    "a manifest naming no members is a missing image",
			body:    mustMarshal(t, Manifest{ImageID: inventoryImage, Format: FormatBlobs}),
			wantErr: model.ErrSavepointImageMissing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newManifestStore(t, tt.body)

			// Act
			digests, err := ReadListedBlobs(context.Background(), store, string(layerKey))

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, digests)
		})
	}
}

func TestReadListedBlobs_Torn(t *testing.T) {
	// Arrange
	store := newManifestStore(t, "{torn")

	// Act
	_, err := ReadListedBlobs(context.Background(), store, string(layerKey))

	// Assert: a record names it, so a torn one references everything rather than nothing.
	require.ErrorContains(t, err, "decode image.json")
}

func TestReadReusableBlobs(t *testing.T) {
	intact := mustMarshal(t, listedManifest)
	memberless := mustMarshal(t, Manifest{ImageID: inventoryImage, Format: FormatBlobs})
	tests := []struct {
		name    string
		body    string
		size    int64
		image   model.ImageDigestID
		want    []string
		wantErr string
	}{
		{
			name:  "an intact manifest of its image lists its blobs",
			body:  intact,
			size:  int64(len(intact)),
			image: inventoryImage,
			want:  []string{inventoryBlob},
		},
		{
			name:  "bytes past the JSON go unread, as the capture reads them: it reuses the blobs",
			body:  intact + "\n}garbage",
			size:  int64(len(intact)) + 9,
			image: inventoryImage,
			want:  []string{inventoryBlob},
		},
		{
			name:  "a manifest naming no members is written again: it holds nothing",
			body:  memberless,
			size:  int64(len(memberless)),
			image: inventoryImage,
		},
		{
			name:  "a missing manifest is written again: it holds nothing",
			image: inventoryImage,
		},
		{
			name:  "a manifest that is not JSON is written again: it holds nothing",
			body:  "{torn",
			size:  5,
			image: inventoryImage,
		},
		{
			name:  "a manifest naming another image is written again: it holds nothing",
			body:  intact,
			size:  int64(len(intact)),
			image: model.ImageDigestID("sha256:" + strings.Repeat("3", 64)),
		},
		{
			name:    "a manifest read shorter than listed is unreadable",
			body:    intact,
			size:    int64(len(intact)) + 1,
			wantErr: "bytes listed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newManifestStore(t, tt.body)

			// Act
			digests, err := ReadReusableBlobs(context.Background(), store, string(layerKey), tt.size, tt.image)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, digests)
		})
	}
}

// newManifestStore is a store holding body as the layer's manifest; none when body is empty.
func newManifestStore(t *testing.T, body string) storages.Storager {
	t.Helper()
	store := inmemory.New("")
	if body != "" {
		require.NoError(t, store.PutObject(context.Background(), ManifestKey(string(layerKey)), strings.NewReader(body)))
	}
	return store
}

func mustMarshal(t *testing.T, manifest Manifest) string {
	t.Helper()
	body, err := json.Marshal(manifest)
	require.NoError(t, err)
	return string(body)
}
