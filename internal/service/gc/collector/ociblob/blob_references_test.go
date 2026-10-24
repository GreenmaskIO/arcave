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

package ociblob

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
)

func TestReadBlobReferences(t *testing.T) {
	layer := model.WritableLayer{Service: "db", Replica: 1, ImageBackend: model.ImageBackendDedupCopy}
	layerManifest := func(image model.ImageDigestID, blob string) map[string]any {
		return map[string]any{
			"image_id": image, "format": "oci-blobs",
			"members": []map[string]any{
				{"name": "blobs/sha256/" + blob, "kind": "blob", "digest": blob},
				{"name": "index.json", "kind": "metadata"},
			},
		}
	}
	layerKey := func(id model.SavepointID) string { return id.String() + "/images/db/replicas/1/image.json" }

	tests := []struct {
		name       string
		plant      func(t *testing.T, root string)
		savepoints []model.Savepoint
		dead       []model.SavepointID
		want       blobReferences
		// wantUnreadable are the unreadable manifests' keys and the savepoints naming them.
		wantUnreadable map[string]model.SavepointID
	}{
		{
			name: "a record's layer and stored image hold their blobs",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, layerKey(gctest.LiveSavepoint), layerManifest(gctest.Digest("9"), gctest.LiveBlob), gctest.Old)
				gctest.WriteJSON(t, root, imageKeyOf(gctest.LiveImage), layerManifest(gctest.LiveImage, gctest.NewBlob), gctest.Old)
			},
			savepoints: []model.Savepoint{{
				ID:             gctest.LiveSavepoint,
				WritableLayers: []model.WritableLayer{layer},
				Images:         []model.Image{{ID: gctest.LiveImage, Stored: true}},
			}},
			want: blobReferences{bySavepoints: map[string]bool{gctest.LiveBlob: true, gctest.NewBlob: true}},
		},
		{
			name: "an image no record names holds its blobs until it goes; a tree that is not dead is not read",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, imageKeyOf(gctest.DeadImage), layerManifest(gctest.DeadImage, gctest.DeadBlob), gctest.Old)
				gctest.WriteJSON(t, root, layerKey(gctest.FreshSavepoint), layerManifest(gctest.Digest("9"), gctest.NewBlob), gctest.Recent)
			},
			want: blobReferences{byUnnamed: map[string]bool{gctest.DeadBlob: true}},
		},
		{
			name: "an image a record names but did not store holds nothing as the record's",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, imageKeyOf(gctest.LiveImage), layerManifest(gctest.LiveImage, gctest.NewBlob), gctest.Old)
			},
			savepoints: []model.Savepoint{{ID: gctest.LiveSavepoint, Images: []model.Image{{ID: gctest.LiveImage}}}},
			want:       blobReferences{byUnnamed: map[string]bool{gctest.NewBlob: true}},
		},
		{
			name: "dead trees hold nothing and only attribute what they list, in order",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, layerKey(gctest.OtherDeadSavepoint), layerManifest(gctest.Digest("9"), gctest.DeadBlob), gctest.Old)
				gctest.WriteJSON(t, root, layerKey(gctest.DeadSavepoint), layerManifest(gctest.Digest("9"), gctest.DeadBlob), gctest.Old)
				gctest.WriteObject(t, root, gctest.OtherDeadSavepoint.String()+"/images/web/replicas/1/image.json", "{torn", gctest.Old)
			},
			dead: []model.SavepointID{gctest.OtherDeadSavepoint, gctest.DeadSavepoint},
			want: blobReferences{byDeadSavepoints: map[string][]model.SavepointID{
				gctest.DeadBlob: {gctest.DeadSavepoint, gctest.OtherDeadSavepoint},
			}},
		},
		{
			name: "a manifest a record names that is torn or missing is unreadable",
			plant: func(t *testing.T, root string) {
				gctest.WriteObject(t, root, layerKey(gctest.LiveSavepoint), "{torn", gctest.Old)
			},
			savepoints: []model.Savepoint{{
				ID:             gctest.LiveSavepoint,
				WritableLayers: []model.WritableLayer{layer},
				Images:         []model.Image{{ID: gctest.LiveImage, Stored: true}},
			}},
			wantUnreadable: map[string]model.SavepointID{
				layerKey(gctest.LiveSavepoint): gctest.LiveSavepoint,
				imageKeyOf(gctest.LiveImage):   gctest.LiveSavepoint,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store, root := gctest.NewStore(t)
			tt.plant(t, root)
			scope := gctest.CaptureStatuses{Dead: tt.dead}.Apply(gctest.NewScope(t, store, tt.savepoints...))

			// Act
			got, err := readBlobReferences(context.Background(), scope, 2)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, orEmpty(tt.want.bySavepoints), got.bySavepoints)
			assert.Equal(t, orEmpty(tt.want.byUnnamed), got.byUnnamed)
			wantDead := tt.want.byDeadSavepoints
			if wantDead == nil {
				wantDead = map[string][]model.SavepointID{}
			}
			assert.Equal(t, wantDead, got.byDeadSavepoints)
			unreadable := map[string]model.SavepointID{}
			for _, manifest := range got.unreadable {
				unreadable[manifest.Key] = manifest.Savepoint
			}
			assert.Equal(t, orEmpty(tt.wantUnreadable), unreadable)
		})
	}
}

func TestReadBlobReferences_Cancelled(t *testing.T) {
	// Arrange
	store, root := gctest.NewStore(t)
	gctest.WriteJSON(t, root, imageKeyOf(gctest.DeadImage), map[string]any{"image_id": gctest.DeadImage}, gctest.Old)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Act
	_, err := readBlobReferences(ctx, gctest.NewScope(t, store), 1)

	// Assert: what a cancelled run read is no verdict.
	require.ErrorIs(t, err, context.Canceled)
}

func imageKeyOf(image model.ImageDigestID) string {
	return "shared/oci/images/sha256/" + strings.TrimPrefix(image.String(), "sha256:") + "/image.json"
}

func orEmpty[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}
