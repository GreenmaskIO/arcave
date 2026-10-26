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
	"errors"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc"
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
	assert.Equal(t, utils.New(int64(4)), got.Ops[0].Bytes)
	assert.Empty(t, got.Skipped)
	assert.Empty(t, got.Unjudged)
	assert.Equal(t, 1, got.Live)
}

// Delete removes each image directory its finding names, its manifest first: a removal cut short
// must leave no manifest a capture would take for the image stored.
func TestCollector_Delete(t *testing.T) {
	var (
		image2 = "shared/oci/images/sha256/" + strings.Repeat("2", 64)
		image3 = "shared/oci/images/sha256/" + strings.Repeat("3", 64)
		ops    = []model.GarbageOp{
			{Action: model.GarbageActionDeleteTree, Target: image2, Bytes: utils.New(int64(4))},
			{Action: model.GarbageActionDeleteTree, Target: image3, Bytes: utils.New(int64(2))},
		}
		stat     = model.OCIImageStat{GarbageFindings: model.GarbageFindings{Ops: ops}}
		missing  = &storages.MissingObjectsError{Paths: []string{image3 + "/image.json"}}
		denied   = errors.New("access denied")
		image3Ok = model.GarbageDeleted{Target: image3, Bytes: utils.New(int64(2))}
	)

	// step is one deletion the store expects, in order.
	type step struct {
		method string
		arg    any
		err    error
	}
	tests := []struct {
		name      string
		stat      model.GarbageStat
		steps     []step
		cancelled bool
		want      model.GarbageDeletion
		wantErr   bool
	}{
		{
			name: "the manifest goes before the directory, and one without a manifest goes whole",
			stat: stat,
			steps: []step{
				{"Delete", []string{image2 + "/image.json"}, nil}, {"DeleteAll", image2, nil},
				{"Delete", []string{image3 + "/image.json"}, missing}, {"DeleteAll", image3, nil},
			},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: image2, Bytes: utils.New(int64(4))}, image3Ok,
			}},
		},
		{
			name: "a manifest that cannot go keeps its directory",
			stat: stat,
			steps: []step{
				{"Delete", []string{image2 + "/image.json"}, denied},
				{"Delete", []string{image3 + "/image.json"}, nil}, {"DeleteAll", image3, nil},
			},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{image3Ok},
				Failed:  []model.GarbageDeleteFailure{{Target: image2, Error: "delete " + image2 + "/image.json: access denied"}},
			},
		},
		{
			name: "a directory cut short past its manifest fails, its manifest gone",
			stat: stat,
			steps: []step{
				{"Delete", []string{image2 + "/image.json"}, nil}, {"DeleteAll", image2, denied},
				{"Delete", []string{image3 + "/image.json"}, nil}, {"DeleteAll", image3, nil},
			},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{image3Ok},
				Failed:  []model.GarbageDeleteFailure{{Target: image2, Error: "delete " + image2 + ": access denied"}},
			},
		},
		{
			name:      "a cancelled run deletes nothing",
			stat:      stat,
			cancelled: true,
			want:      model.GarbageDeletion{Deleted: []model.GarbageDeleted{}, NotRun: 2},
		},
		{
			name:    "another kind's finding is refused",
			stat:    model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: ops}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			store := &gctest.MockStore{}
			var calls []*mock.Call
			for _, step := range tt.steps {
				calls = append(calls, store.On(step.method, mock.Anything, step.arg).Return(step.err).Once())
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got, err := New().Delete(ctx, gc.ComposeProjectScope{Store: store}, tt.stat)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			store.AssertExpectations(t)
		})
	}
}
