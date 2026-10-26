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

package eraser

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
)

// TreeEraser removes each tree its ops name, whole, in order.
func TestTreeEraser_Erase(t *testing.T) {
	var (
		dead      = gctest.DeadSavepoint.String()
		otherDead = gctest.OtherDeadSavepoint.String()
		ops       = []model.GarbageOp{
			{Action: model.GarbageActionDeleteTree, Target: dead, Bytes: utils.New(int64(5))},
			{Action: model.GarbageActionDeleteTree, Target: otherDead, Bytes: utils.New(int64(2))},
		}
		bothDeleted = []model.GarbageDeleted{
			{Target: dead, Bytes: utils.New(int64(5))}, {Target: otherDead, Bytes: utils.New(int64(2))},
		}
	)

	tests := []struct {
		name string
		// errs is what deleting each tree returns, one per tree in order; nil expects no deletion.
		errs      []error
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "every dead tree goes",
			errs: []error{nil, nil},
			want: model.GarbageDeletion{Deleted: bothDeleted},
		},
		{
			name: "a tree already gone counts as deleted, freeing nothing",
			errs: []error{&storages.MissingObjectsError{Paths: []string{dead}}, nil},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: dead, Bytes: utils.New(int64(0))}, {Target: otherDead, Bytes: utils.New(int64(2))},
			}},
		},
		{
			name: "a tree that cannot go fails alone",
			errs: []error{errors.New("access denied"), nil},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: otherDead, Bytes: utils.New(int64(2))}},
				Failed:  []model.GarbageDeleteFailure{{Target: dead, Error: "access denied"}},
			},
		},
		{
			name:      "a cancelled run deletes nothing",
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 2},
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
			for i, err := range tt.errs {
				calls = append(calls, store.On("DeleteAll", mock.Anything, ops[i].Target).Return(err).Once())
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got := NewTreeEraser().Erase(ctx, store, ops)

			// Assert
			assert.Equal(t, tt.want, got)
			store.AssertExpectations(t)
		})
	}
}

// ImageEraser removes each stored image its ops name, its manifest first: a removal cut short
// must leave no manifest a capture would take for the image stored.
func TestImageEraser_Erase(t *testing.T) {
	var (
		image2 = "shared/oci/images/sha256/" + strings.Repeat("2", 64)
		image3 = "shared/oci/images/sha256/" + strings.Repeat("3", 64)
		ops    = []model.GarbageOp{
			{Action: model.GarbageActionDeleteImage, Target: image2, Bytes: utils.New(int64(4))},
			{Action: model.GarbageActionDeleteImage, Target: image3, Bytes: utils.New(int64(2))},
		}
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
		steps     []step
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "the manifest goes before the directory, and one without a manifest goes whole",
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
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 2},
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
			got := NewImageEraser().Erase(ctx, store, ops)

			// Assert
			assert.Equal(t, tt.want, got)
			store.AssertExpectations(t)
		})
	}
}

// ObjectEraser removes each blob its ops name, one at a time, in order.
func TestObjectEraser_Erase(t *testing.T) {
	var (
		dead    = "shared/oci/blobs/sha256/" + gctest.DeadBlob
		newDead = "shared/oci/blobs/sha256/" + gctest.NewBlob
		ops     = []model.GarbageOp{
			{Action: model.GarbageActionDeleteObject, Target: dead, Bytes: utils.New(int64(1))},
			{Action: model.GarbageActionDeleteObject, Target: newDead, Bytes: utils.New(int64(1))},
		}
		bothDeleted = []model.GarbageDeleted{
			{Target: dead, Bytes: utils.New(int64(1))}, {Target: newDead, Bytes: utils.New(int64(1))},
		}
	)

	tests := []struct {
		name string
		// errs is what deleting each blob returns, one per blob in order; nil expects no deletion.
		errs      []error
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "every dead blob goes",
			errs: []error{nil, nil},
			want: model.GarbageDeletion{Deleted: bothDeleted},
		},
		{
			name: "a blob already gone counts as deleted, freeing nothing",
			errs: []error{&storages.MissingObjectsError{Paths: []string{dead}}, nil},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: dead, Bytes: utils.New(int64(0))}, {Target: newDead, Bytes: utils.New(int64(1))},
			}},
		},
		{
			name: "a blob that cannot go fails alone",
			errs: []error{errors.New("access denied"), nil},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: newDead, Bytes: utils.New(int64(1))}},
				Failed:  []model.GarbageDeleteFailure{{Target: dead, Error: "access denied"}},
			},
		},
		{
			name:      "a cancelled run deletes nothing",
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 2},
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
			for i, err := range tt.errs {
				calls = append(calls, store.On("Delete", mock.Anything, []string{ops[i].Target}).Return(err).Once())
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got := NewObjectEraser().Erase(ctx, store, ops)

			// Assert
			assert.Equal(t, tt.want, got)
			store.AssertExpectations(t)
		})
	}
}
