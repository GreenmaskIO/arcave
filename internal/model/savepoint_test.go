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

package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A safety snapshot is told from a savepoint by one label and nothing else, so the
// predicate is the whole of that distinction.
func TestSavepoint_IsSafetySnapshot(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{name: "the label a rollback stamps", labels: map[string]string{SavepointLabelSafetySnapshot: SavepointLabelSafetySnapshotValue}, want: true},
		{
			name:   "beside labels of the user's own",
			labels: map[string]string{"team": "payments", SavepointLabelSafetySnapshot: SavepointLabelSafetySnapshotValue},
			want:   true,
		},
		{name: "no labels at all", labels: nil},
		{name: "other labels only", labels: map[string]string{"team": "payments"}},
		// A key match is not enough: only the value arcave writes counts, so a user
		// label that happens to share the key never hides a savepoint.
		{name: "the key with another value", labels: map[string]string{SavepointLabelSafetySnapshot: "false"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Labels: tt.labels}.IsSafetySnapshot()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// A savepoint ref reads as its name, as its id when it had none.
func TestSavepointRef_String(t *testing.T) {
	tests := []struct {
		name string
		ref  SavepointRef
		want string
	}{
		{name: "a named savepoint", ref: SavepointRef{ID: "019e843e-1650-7000-8000-000000000000", Name: "seed"}, want: "seed"},
		{name: "an unnamed one, by its full id", ref: SavepointRef{ID: "019e843e-1650-7000-8000-000000000000"}, want: "019e843e-1650-7000-8000-000000000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.ref.String()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// taken_before_rollback_to is a safety snapshot's alone: absent from any other record, and its name
// key present even when the target had none.
func TestSavepoint_JSON_TakenBeforeRollbackTo(t *testing.T) {
	tests := []struct {
		name      string
		savepoint Savepoint
		want      map[string]any
	}{
		{name: "an ordinary savepoint", savepoint: Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "seed"}},
		{
			name: "a safety snapshot of an unnamed target",
			savepoint: Savepoint{
				ID:                    "019e844b-7818-7000-8000-000000000000",
				Labels:                map[string]string{SavepointLabelSafetySnapshot: SavepointLabelSafetySnapshotValue},
				TakenBeforeRollbackTo: &SavepointRef{ID: "019e843e-1650-7000-8000-000000000000"},
			},
			want: map[string]any{"id": "019e843e-1650-7000-8000-000000000000", "name": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			data, err := json.Marshal(tt.savepoint)

			// Assert
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(data, &got))
			field, present := got["taken_before_rollback_to"]
			if tt.want == nil {
				assert.False(t, present)
				return
			}
			assert.Equal(t, tt.want, field)
		})
	}
}
