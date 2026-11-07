// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package safetysnapshot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
)

const (
	// The ids are UUIDv7s, so they order by age; CreatedAt below says the opposite.
	oldest = model.SavepointID("019e843e-1650-7000-8000-00000000000a")
	middle = model.SavepointID("019e843e-1650-7000-8000-00000000000b")
	newest = model.SavepointID("019e843e-1650-7000-8000-00000000000c")
	plain  = model.SavepointID("019e843e-1650-7000-8000-00000000000d")
)

// newSafetySnapshot is a safety snapshot whose CreatedAt runs against its id, as another host's clock may.
func newSafetySnapshot(id model.SavepointID) model.Savepoint {
	createdAt := map[model.SavepointID]int{oldest: 3, middle: 2, newest: 1}[id]
	return model.Savepoint{
		ID:        id,
		CreatedAt: gctest.Now.AddDate(0, 0, createdAt),
		Labels:    map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue},
	}
}

// The newest by id stay, the rest go; an ordinary savepoint is never counted or touched.
func TestCollector_Find(t *testing.T) {
	tests := []struct {
		name         string
		retainRecent int
		savepoints   []model.Savepoint
		wantTargets  []string
		wantRetained int
	}{
		{
			name:         "the default keeps the newest",
			retainRecent: 1,
			savepoints:   []model.Savepoint{newSafetySnapshot(oldest), newSafetySnapshot(newest), newSafetySnapshot(middle)},
			wantTargets:  []string{middle.String(), oldest.String()},
			wantRetained: 1,
		},
		{
			name:         "0 deletes every one",
			retainRecent: 0,
			savepoints:   []model.Savepoint{newSafetySnapshot(oldest), newSafetySnapshot(newest)},
			wantTargets:  []string{newest.String(), oldest.String()},
		},
		{
			name:         "2 keeps two",
			retainRecent: 2,
			savepoints:   []model.Savepoint{newSafetySnapshot(oldest), newSafetySnapshot(newest), newSafetySnapshot(middle)},
			wantTargets:  []string{oldest.String()},
			wantRetained: 2,
		},
		{
			name:         "fewer than the retention keeps them all",
			retainRecent: 5,
			savepoints:   []model.Savepoint{newSafetySnapshot(oldest)},
			wantRetained: 1,
		},
		{
			name:         "an ordinary savepoint, however new, neither takes a slot nor goes",
			retainRecent: 1,
			savepoints:   []model.Savepoint{{ID: plain}, newSafetySnapshot(newest), newSafetySnapshot(oldest)},
			wantTargets:  []string{oldest.String()},
			wantRetained: 1,
		},
		{
			name:         "no safety snapshot finds nothing",
			retainRecent: 1,
			savepoints:   []model.Savepoint{{ID: plain}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store, _ := gctest.NewStore(t)
			scope := gctest.NewScope(t, store, tt.savepoints...)

			// Act
			stat, err := New(tt.retainRecent).Find(context.Background(), scope)

			// Assert
			require.NoError(t, err)
			require.IsType(t, model.SafetySnapshotStat{}, stat)
			got := stat.(model.SafetySnapshotStat)
			var targets []string
			for _, op := range got.Ops {
				targets = append(targets, op.Target)
			}
			assert.Equal(t, tt.wantTargets, targets)
			assert.NotNil(t, got.Ops)
			assert.Equal(t, tt.wantRetained, got.Retained)
		})
	}
}

// An op says what deleting it takes and what it frees, and names no dead savepoint: the record is
// alive until it goes, so result.savepoints stays the list of the dead.
func TestCollector_Find_Op(t *testing.T) {
	// Arrange
	store, root := gctest.NewStore(t)
	gctest.WriteObject(t, root, oldest.String()+"/savepoint.json", "{}", gctest.Old)
	gctest.WriteObject(t, root, oldest.String()+"/volume-named-db/data.tar", "12345", gctest.Recent)
	gctest.WriteObject(t, root, plain.String()+"/savepoint.json", "{}", gctest.Old)
	scope := gctest.NewScope(t, store, newSafetySnapshot(oldest), model.Savepoint{ID: plain})

	// Act
	stat, err := New(0).Find(context.Background(), scope)

	// Assert
	require.NoError(t, err)
	ops := stat.Findings().Ops
	require.Len(t, ops, 1)
	assert.Equal(t, model.GarbageActionDeleteSavepoint, ops[0].Action)
	assert.Equal(t, oldest.String(), ops[0].Target)
	require.NotNil(t, ops[0].Bytes)
	assert.Equal(t, int64(7), *ops[0].Bytes)
	assert.Equal(t, model.GarbageReasonBeyondRetention, ops[0].Reason)
	assert.True(t, gctest.Recent.Equal(ops[0].LastModified))
	assert.Empty(t, ops[0].Savepoint)
}
