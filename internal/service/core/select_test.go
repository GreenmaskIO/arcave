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

package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestFindSavepoint(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	// The safety snapshot is the newest record and shares a name with two others, so
	// it is in reach of every scanning selector — and must be picked by none.
	safetySnapshot := model.Savepoint{
		ID: "400", Name: "dup", Labels: newSafetySnapshotLabels(), CreatedAt: now.Add(3 * time.Hour),
	}
	records := []model.Savepoint{
		{ID: "100", Name: "before-migration", CreatedAt: now},
		{ID: "300", Name: "dup", CreatedAt: now.Add(2 * time.Hour)}, // newest visible
		{ID: "200", Name: "dup", CreatedAt: now.Add(time.Hour)},
		safetySnapshot,
	}

	latest := model.RollbackRequest{Latest: ptrBool(true)}
	byName := func(name string) model.RollbackRequest { return model.RollbackRequest{Name: &name} }

	tests := []struct {
		name    string
		records []model.Savepoint
		req     model.RollbackRequest
		wantID  model.SavepointID
		wantErr bool
		// wantErrIs, when set, is the sentinel the error must carry.
		wantErrIs error
	}{
		{name: "latest picks newest by created_at", records: records, req: latest, wantID: "300"},
		{name: "explicit id hit", records: records, req: model.RollbackRequest{ID: ptrID("200")}, wantID: "200"},
		{name: "unique name hit", records: records, req: byName("before-migration"), wantID: "100"},
		{name: "ambiguous name errors", records: records, req: byName("dup"), wantErr: true},
		{name: "unknown id errors", records: records, req: model.RollbackRequest{ID: ptrID("999")}, wantErr: true},
		{name: "unknown name errors", records: records, req: byName("nope"), wantErr: true},
		// An id is exact and never falls back to a name: a savepoint *named* "300"
		// is not what `rollback 300` asks for.
		{
			name:    "an id never resolves by name",
			records: []model.Savepoint{{ID: "100", Name: "300", CreatedAt: now}},
			req:     model.RollbackRequest{ID: ptrID("300")},
			wantErr: true,
		},
		// The manual recovery path after a killed rollback: the id was read off the
		// log or off `savepoint list --include-safety-snapshots`, and naming it is the
		// caller's statement that they know what they are asking for.
		{name: "an explicit id reaches a safety snapshot", records: records, req: model.RollbackRequest{ID: ptrID("400")}, wantID: "400"},
		{
			name:    "latest never lands on a safety snapshot",
			records: []model.Savepoint{{ID: "100", CreatedAt: now}, safetySnapshot},
			req:     latest,
			wantID:  "100",
		},
		{
			name:    "nor does a name that only it carries",
			records: []model.Savepoint{{ID: "100", Name: "sp", CreatedAt: now}, safetySnapshot},
			req:     byName("dup"),
			wantErr: true,
		},
		{
			name:      "a store holding only safety snapshots reads as empty",
			records:   []model.Savepoint{safetySnapshot},
			req:       latest,
			wantErr:   true,
			wantErrIs: model.ErrNoSavepoints,
		},
		{name: "no savepoints at all errors", req: latest, wantErr: true, wantErrIs: model.ErrNoSavepoints},
		// The backstop for a request that skipped Validate: no selector resolves to
		// no savepoint, not to an arbitrary one.
		{name: "no selector errors", records: records, req: model.RollbackRequest{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := findSavepoint(tt.records, tt.req)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}
}

func TestFindSavepoint_AmbiguousName(t *testing.T) {
	// Arrange: two visible "dup"s out of id order, plus a safety snapshot of the same name.
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	name := "dup"
	records := []model.Savepoint{
		{ID: "300", Name: name, CreatedAt: now},
		{ID: "200", Name: name, CreatedAt: now},
		{ID: "400", Name: name, Labels: newSafetySnapshotLabels(), CreatedAt: now},
	}

	// Act
	_, err := findSavepoint(records, model.RollbackRequest{Name: &name})

	// Assert
	var ambiguous *model.AmbiguousSavepointNameError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, name, ambiguous.Name)
	assert.Equal(t, []model.SavepointID{"200", "300"}, ambiguous.IDs, "sorted, and never the safety snapshot")
	assert.EqualError(t, err, `2 savepoints are named "dup"; name one by id instead: 200, 300`)
}
