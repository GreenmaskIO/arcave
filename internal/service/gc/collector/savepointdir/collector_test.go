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

package savepointdir

import (
	"context"
	"errors"
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

// Every dead tree goes, however young.
func TestCollector_Find(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := gctest.NewStore(t)
	gctest.WriteObject(t, root, gctest.LiveSavepoint.String()+"/savepoint.json", "{}", gctest.Old)
	gctest.WriteObject(t, root, gctest.DeadSavepoint.String()+"/volume-named-db/data.tar", "12345", gctest.Recent)
	gctest.WriteObject(t, root, gctest.OtherDeadSavepoint.String()+"/heartbeat.json", "{}", gctest.Recent)
	gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "x", gctest.Old)
	scope := gctest.NewScope(t, store, model.Savepoint{ID: gctest.LiveSavepoint})
	scope.Dead = []model.SavepointID{gctest.DeadSavepoint, gctest.OtherDeadSavepoint}

	// Act
	stat, err := New().Find(ctx, scope)

	// Assert
	require.NoError(t, err)
	got := stat.(model.SavepointDirectoryStat)
	// The directory store reports local times.
	for i := range got.Ops {
		got.Ops[i].LastModified = got.Ops[i].LastModified.UTC()
	}
	assert.Equal(t, []model.GarbageOp{
		{
			Action: model.GarbageActionDeleteTree, Target: gctest.DeadSavepoint.String(), Bytes: utils.New(int64(5)),
			Reason: model.GarbageReasonDeadCapture, LastModified: gctest.Recent, Savepoint: gctest.DeadSavepoint,
		},
		{
			Action: model.GarbageActionDeleteTree, Target: gctest.OtherDeadSavepoint.String(), Bytes: utils.New(int64(2)),
			Reason: model.GarbageReasonDeadCapture, LastModified: gctest.Recent, Savepoint: gctest.OtherDeadSavepoint,
		},
	}, got.Ops)
	assert.Empty(t, got.Skipped)
	assert.Empty(t, got.Unjudged)
	assert.Equal(t, 1, got.Committed)
}

// Delete removes each tree its finding names, whole, in order.
func TestCollector_Delete(t *testing.T) {
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
		stat = model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: ops}}
	)

	tests := []struct {
		name string
		stat model.GarbageStat
		// errs is what deleting each tree returns, one per tree in order; nil expects no deletion.
		errs      []error
		cancelled bool
		want      model.GarbageDeletion
		wantErr   bool
	}{
		{
			name: "every dead tree goes",
			stat: stat,
			errs: []error{nil, nil},
			want: model.GarbageDeletion{Deleted: bothDeleted},
		},
		{
			name: "a tree already gone counts as deleted",
			stat: stat,
			errs: []error{&storages.MissingObjectsError{Paths: []string{dead}}, nil},
			want: model.GarbageDeletion{Deleted: bothDeleted},
		},
		{
			name: "a tree that cannot go fails alone",
			stat: stat,
			errs: []error{errors.New("access denied"), nil},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: otherDead, Bytes: utils.New(int64(2))}},
				Failed:  []model.GarbageDeleteFailure{{Target: dead, Error: "access denied"}},
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
			stat:    model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: ops}},
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
			for i, err := range tt.errs {
				calls = append(calls, store.On("DeleteAll", mock.Anything, ops[i].Target).Return(err).Once())
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
