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

package ociimage

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
)

// An image no record names is garbage whatever its age, unless a capture that may name it is
// live or unknown.
func TestCollector_Find(t *testing.T) {
	image2 := "shared/oci/images/sha256/" + strings.Repeat("2", 64)
	image3 := "shared/oci/images/sha256/" + strings.Repeat("3", 64)
	tests := []struct {
		name         string
		statuses     gctest.CaptureStatuses
		wantOps      []string
		wantSkipped  []model.GarbageReason
		wantUnjudged []model.GarbageReason
	}{
		{
			name:     "no capture alive: every unnamed image goes, a fresh one too",
			statuses: gctest.CaptureStatuses{Dead: []model.SavepointID{gctest.DeadSavepoint}},
			wantOps:  []string{image2, image3},
		},
		{
			name:        "a capture in progress holds every unnamed image",
			statuses:    gctest.CaptureStatuses{InProgress: []model.SavepointID{gctest.FreshSavepoint}},
			wantOps:     []string{},
			wantSkipped: []model.GarbageReason{model.GarbageReasonCaptureInProgress, model.GarbageReasonCaptureInProgress},
		},
		{
			name:         "a capture of unknown status leaves every unnamed image unjudged",
			statuses:     gctest.CaptureStatuses{Unknown: []gc.UnknownCapture{{ID: gctest.UnknownSavepoint, Error: "boom"}}},
			wantOps:      []string{},
			wantUnjudged: []model.GarbageReason{model.GarbageReasonStatusUnknown, model.GarbageReasonStatusUnknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := context.Background()
			store, root := gctest.NewStore(t)
			gctest.WriteObject(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", "{}", gctest.Old)
			gctest.WriteObject(t, root, image2+"/image.json", "{}", gctest.Old)
			gctest.WriteObject(t, root, image2+"/index.json", "{}", gctest.Old)
			gctest.WriteObject(t, root, image3+"/image.json", "{}", gctest.Recent)
			savepoint := model.Savepoint{ID: gctest.LiveSavepoint, Images: []model.Image{
				{ID: gctest.LiveImage, Stored: true},
				{ID: gctest.DeadImage, Stored: false}, // only its identity is recorded: it keeps nothing alive
			}}

			// Act
			stat, err := New().Find(ctx, tt.statuses.Apply(gctest.NewScope(t, store, savepoint)))

			// Assert
			require.NoError(t, err)
			got := stat.(model.OCIImageStat)
			assert.Equal(t, tt.wantOps, gctest.Targets(got.Ops))
			if len(got.Ops) > 0 {
				assert.Equal(t, utils.New(int64(4)), got.Ops[0].Bytes)
			}
			assert.Equal(t, tt.wantSkipped, gctest.NilIfEmpty(gctest.Reasons(got.Skipped)))
			assert.Equal(t, tt.wantUnjudged, gctest.NilIfEmpty(gctest.Reasons(got.Unjudged)))
			assert.Equal(t, 1, got.Live)
		})
	}
}
