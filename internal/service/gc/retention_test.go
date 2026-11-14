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
package gc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	// The ids are UUIDv7s, so they order by age; CreatedAt below says the opposite.
	oldest = model.SavepointID("019e843e-1650-7000-8000-00000000000a")
	middle = model.SavepointID("019e843e-1650-7000-8000-00000000000b")
	newest = model.SavepointID("019e843e-1650-7000-8000-00000000000c")
	plain  = model.SavepointID("019e843e-1650-7000-8000-00000000000d")
)

// createdAt is the day the newest safety snapshot below says it was taken.
var createdAt = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// newSafetySnapshot is a safety snapshot whose CreatedAt runs against its id, as another host's clock may.
func newSafetySnapshot(id model.SavepointID) model.Savepoint {
	days := map[model.SavepointID]int{oldest: 3, middle: 2, newest: 1}[id]
	return model.Savepoint{
		ID:        id,
		CreatedAt: createdAt.AddDate(0, 0, days),
		Labels:    map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue},
	}
}

// The newest by id stay, the rest go; an ordinary savepoint is never counted or touched.
func TestNewRetentionStat(t *testing.T) {
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
			// Act
			got := newRetentionStat(tt.savepoints, tt.retainRecent)

			// Assert
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

// An op says what deleting it takes and names neither a dead savepoint nor a size.
func TestNewRetentionStat_Op(t *testing.T) {
	// Act
	got := newRetentionStat([]model.Savepoint{newSafetySnapshot(oldest), {ID: plain}}, 0)

	// Assert
	assert.Equal(t, []model.GarbageOp{{
		Action: model.GarbageActionDeleteSavepoint,
		Target: oldest.String(),
		Reason: model.GarbageReasonBeyondRetention,
	}}, got.Ops)
	assert.Nil(t, got.Summary().Bytes, "the size of what goes is not known")
}
