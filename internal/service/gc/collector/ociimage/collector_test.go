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
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
)

// An image no record names is garbage whatever its age; one recorded by identity alone keeps
// nothing alive.
func TestCollector_Find(t *testing.T) {
	// Arrange
	ctx := context.Background()
	image2 := "shared/oci/images/sha256/" + strings.Repeat("2", 64)
	image3 := "shared/oci/images/sha256/" + strings.Repeat("3", 64)
	store, root := gctest.NewStore(t)
	gctest.WriteObject(t, root, "shared/oci/images/sha256/"+strings.Repeat("1", 64)+"/image.json", "{}", gctest.Old)
	gctest.WriteObject(t, root, image2+"/image.json", "{}", gctest.Old)
	gctest.WriteObject(t, root, image2+"/index.json", "{}", gctest.Old)
	gctest.WriteObject(t, root, image3+"/image.json", "{}", gctest.Recent)
	savepoint := model.Savepoint{ID: gctest.LiveSavepoint, Images: []model.Image{
		{ID: gctest.LiveImage, Stored: true},
		{ID: gctest.DeadImage, Stored: false}, // only its identity is recorded: it keeps nothing alive
	}}
	scope := gctest.NewScope(t, store, savepoint)
	scope.Dead = []model.SavepointID{gctest.DeadSavepoint}

	// Act
	stat, err := New().Find(ctx, scope)

	// Assert
	require.NoError(t, err)
	got := stat.(model.OCIImageStat)
	require.Len(t, got.Ops, 2)
	assert.Equal(t, []string{image2, image3}, gctest.Targets(got.Ops))
	for _, op := range got.Ops {
		assert.Equal(t, model.GarbageActionDeleteImage, op.Action, op.Target)
	}
	assert.Equal(t, utils.New(int64(4)), got.Ops[0].Bytes)
	assert.Empty(t, got.Skipped)
	assert.Empty(t, got.Unjudged)
	assert.Equal(t, 1, got.Live)
}
