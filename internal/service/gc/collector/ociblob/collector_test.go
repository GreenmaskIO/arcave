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

func TestCollector_Find(t *testing.T) {
	layer := model.WritableLayer{Service: "db", Replica: 1, ImageBackend: model.ImageBackendDedupCopy}
	manifest := map[string]any{
		"image_id": "sha256:" + strings.Repeat("9", 64), "format": "oci-blobs",
		"members": []map[string]any{
			{"name": "blobs/sha256/" + gctest.LiveBlob, "kind": "blob", "digest": gctest.LiveBlob},
			{"name": "index.json", "kind": "metadata"},
		},
	}
	imageManifest := map[string]any{
		"image_id": gctest.LiveImage, "format": "oci-blobs",
		"members": []map[string]any{{"name": "blobs/sha256/" + gctest.NewBlob, "kind": "blob", "digest": gctest.NewBlob}},
	}

	orphanManifest := map[string]any{
		"image_id": "sha256:" + strings.Repeat("8", 64), "format": "oci-blobs",
		"members": []map[string]any{{"name": "blobs/sha256/" + gctest.DeadBlob, "kind": "blob", "digest": gctest.DeadBlob}},
	}

	freshBlob := strings.Repeat("d", 64)

	tests := []struct {
		name    string
		plant   func(t *testing.T, root string)
		dead    []model.SavepointID
		wantOps []string
		// wantFreedBy is the dead savepoint each op names, "" for none.
		wantFreedBy    []model.SavepointID
		wantSkipped    []string
		wantAborted    bool
		wantLive       int
		wantUnreadable []string
	}{
		{
			name: "an unlisted blob is garbage whatever its age, a listed one is not",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, gctest.LiveSavepoint.String()+"/images/db/replicas/1/image.json", manifest, gctest.Old)
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", imageManifest, gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.LiveBlob, "live", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.NewBlob, "image", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+freshBlob, "new", gctest.Recent)
			},
			wantOps:     []string{"shared/oci/blobs/sha256/" + gctest.DeadBlob, "shared/oci/blobs/sha256/" + freshBlob},
			wantFreedBy: []model.SavepointID{"", ""},
			wantSkipped: []string{},
			wantLive:    2,
		},
		{
			name: "a dead tree's manifests hold nothing: a blob only one lists goes with it in the same run",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, gctest.LiveSavepoint.String()+"/images/db/replicas/1/image.json", manifest, gctest.Old)
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", imageManifest, gctest.Old)
				gctest.WriteJSON(t, root, gctest.DeadSavepoint.String()+"/images/db/replicas/1/image.json", orphanManifest, gctest.Recent)
				gctest.WriteJSON(t, root, gctest.DeadSavepoint.String()+"/images/web/replicas/1/image.json", map[string]any{
					"image_id": "sha256:" + strings.Repeat("7", 64), "format": "oci-blobs",
					"members": []map[string]any{{"name": "blobs/sha256/" + freshBlob, "kind": "blob", "digest": freshBlob}},
				}, gctest.Recent)
				// Both dead trees list the fresh blob: it goes, but frees neither alone.
				gctest.WriteJSON(t, root, gctest.OtherDeadSavepoint.String()+"/images/web/replicas/1/image.json", map[string]any{
					"image_id": "sha256:" + strings.Repeat("7", 64), "format": "oci-blobs",
					"members": []map[string]any{{"name": "blobs/sha256/" + freshBlob, "kind": "blob", "digest": freshBlob}},
				}, gctest.Recent)
				// A torn manifest in a dead tree keeps nothing alive.
				gctest.WriteObject(t, root, gctest.OtherDeadSavepoint.String()+"/images/db/replicas/1/image.json", "{torn", gctest.Recent)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.LiveBlob, "live", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.NewBlob, "image", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Recent)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+freshBlob, "new", gctest.Recent)
			},
			dead:        []model.SavepointID{gctest.DeadSavepoint, gctest.OtherDeadSavepoint},
			wantOps:     []string{"shared/oci/blobs/sha256/" + gctest.DeadBlob, "shared/oci/blobs/sha256/" + freshBlob},
			wantFreedBy: []model.SavepointID{gctest.DeadSavepoint, ""},
			wantSkipped: []string{},
			wantLive:    2,
		},
		{
			name: "a missing manifest references everything",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", imageManifest, gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Old)
			},
			wantOps:        []string{},
			wantSkipped:    []string{},
			wantAborted:    true,
			wantUnreadable: []string{gctest.LiveSavepoint.String() + "/images/db/replicas/1/image.json"},
		},
		{
			name: "a blob an unreferenced image lists is held until it goes",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, gctest.LiveSavepoint.String()+"/images/db/replicas/1/image.json", manifest, gctest.Old)
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", imageManifest, gctest.Old)
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("8", 64)+"/image.json", orphanManifest, gctest.Old)
				// An unreferenced image dir holding no manifest is one a capture writes again.
				gctest.WriteObject(t, root, "shared/oci/images/sha256/"+strings.Repeat("3", 64)+"/index.json", "{}", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.LiveBlob, "live", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.NewBlob, "image", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Old)
			},
			wantOps:     []string{},
			wantSkipped: []string{"shared/oci/blobs/sha256/" + gctest.DeadBlob},
			wantLive:    2,
		},
		{
			name: "an unreferenced manifest a capture would write again holds nothing",
			plant: func(t *testing.T, root string) {
				gctest.WriteJSON(t, root, gctest.LiveSavepoint.String()+"/images/db/replicas/1/image.json", manifest, gctest.Old)
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", imageManifest, gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/images/sha256/"+strings.Repeat("2", 64)+"/image.json", `{"image_id": "sha`, gctest.Old)
				// It lists the blob, but under another image's name: a capture of image 3 rewrites it.
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("3", 64)+"/image.json", orphanManifest, gctest.Old)
				gctest.WriteObject(t, root, gctest.DeadSavepoint.String()+"/images/db/replicas/1/image.json", "", gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Old)
			},
			wantOps:     []string{"shared/oci/blobs/sha256/" + gctest.DeadBlob},
			wantFreedBy: []model.SavepointID{""},
			wantSkipped: []string{},
		},
		{
			name: "a torn manifest a record names references everything",
			plant: func(t *testing.T, root string) {
				gctest.WriteObject(t, root, gctest.LiveSavepoint.String()+"/images/db/replicas/1/image.json", "{torn", gctest.Old)
				gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", imageManifest, gctest.Old)
				gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Old)
			},
			wantOps:        []string{},
			wantSkipped:    []string{},
			wantAborted:    true,
			wantUnreadable: []string{gctest.LiveSavepoint.String() + "/images/db/replicas/1/image.json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := context.Background()
			store, root := gctest.NewStore(t)
			tt.plant(t, root)
			savepoint := model.Savepoint{
				ID: gctest.LiveSavepoint, WritableLayers: []model.WritableLayer{layer},
				Images: []model.Image{{ID: gctest.LiveImage, Stored: true}},
			}

			// Act
			scope := gctest.NewScope(t, store, savepoint)
			scope.Dead = tt.dead
			stat, err := New(2).Find(ctx, scope)

			// Assert
			require.NoError(t, err)
			got := stat.(model.OCIBlobStat)
			assert.Equal(t, tt.wantOps, gctest.Targets(got.Ops))
			var freedBy []model.SavepointID
			for _, op := range got.Ops {
				freedBy = append(freedBy, op.Savepoint)
			}
			assert.Equal(t, tt.wantFreedBy, freedBy)
			assert.Equal(t, tt.wantSkipped, gctest.SkippedTargets(got.Skipped))
			assert.Empty(t, got.Unjudged)
			assert.Equal(t, tt.wantAborted, got.Aborted != "")
			assert.Equal(t, tt.wantLive, got.Live)
			var unreadable []string
			for _, manifest := range got.Unreadable {
				unreadable = append(unreadable, manifest.Key)
			}
			assert.Equal(t, tt.wantUnreadable, unreadable)
		})
	}
}

// A concurrency of zero still reads every manifest, one at a time.
func TestCollector_Find_ZeroConcurrency(t *testing.T) {
	// Arrange
	store, root := gctest.NewStore(t)
	gctest.WriteJSON(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", map[string]any{
		"image_id": gctest.LiveImage, "format": "oci-blobs",
		"members": []map[string]any{{"name": "blobs/sha256/" + gctest.LiveBlob, "kind": "blob", "digest": gctest.LiveBlob}},
	}, gctest.Old)
	gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Old)

	// Act
	stat, err := New(0).Find(context.Background(), gctest.NewScope(t, store))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{"shared/oci/blobs/sha256/" + gctest.DeadBlob}, gctest.Targets(stat.(model.OCIBlobStat).Ops))
}

// A read of an unreferenced manifest that falls short of its listed size could be an intact
// manifest cut off on the way: it references everything rather than nothing.
func TestCollector_Find_ShortRead(t *testing.T) {
	// Arrange
	store, root := gctest.NewStore(t)
	manifestKey := "shared/oci/images/sha256/" + strings.Repeat("2", 64) + "/image.json"
	gctest.WriteJSON(t, root, manifestKey, map[string]any{
		"image_id": gctest.DeadImage, "format": "oci-blobs",
		"members": []map[string]any{{"name": "blobs/sha256/" + gctest.DeadBlob, "kind": "blob", "digest": gctest.DeadBlob}},
	}, gctest.Old)
	gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "dead!", gctest.Old)
	scope := gctest.NewScope(t, store)
	for i := range scope.Objects {
		if scope.Objects[i].Name == manifestKey {
			scope.Objects[i].Size++ // the store listed more than the read returns
		}
	}

	// Act
	stat, err := New(2).Find(context.Background(), scope)

	// Assert
	require.NoError(t, err)
	got := stat.(model.OCIBlobStat)
	assert.Empty(t, got.Ops)
	assert.NotEmpty(t, got.Aborted)
	require.Len(t, got.Unreadable, 1)
	assert.Equal(t, manifestKey, got.Unreadable[0].Key)
	assert.Empty(t, got.Unreadable[0].Savepoint)
}
