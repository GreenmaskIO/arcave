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

// A result is what a command prints, so its keys are the wire contract.
func TestResults_JSONKeys(t *testing.T) {
	tests := []struct {
		name   string
		result any
		want   []string
	}{
		{
			name:   "an empty rollback omits what it did not do",
			result: RollbackResult{},
			want:   []string{"rolled_back_to", "restored"},
		},
		{
			name: "a whole rollback",
			result: RollbackResult{
				WritableLayers: []WritableLayerRestoreResult{{}},
				SafetySnapshot: "019e843e-1650-7000-8000-000000000000",
			},
			want: []string{"rolled_back_to", "restored", "writable_layers", "safety_snapshot"},
		},
		{
			name:   "a cold rollback names the leftover volumes it overwrote",
			result: RollbackResult{LeftoverVolumes: []string{"shop_pgdata"}},
			want:   []string{"rolled_back_to", "restored", "leftover_volumes"},
		},
		{
			name:   "a savepoint ref names an unnamed savepoint all the same",
			result: SavepointRef{ID: "019e843e-1650-7000-8000-000000000000"},
			want:   []string{"id", "name"},
		},
		{
			name:   "a capture drift",
			result: CaptureDrift{},
			want:   []string{"since", "changes"},
		},
		{
			name:   "a capture change",
			result: CaptureChange{},
			want:   []string{"service", "stamp", "was", "now"},
		},
		{
			name:   "a decision leaves out the backend of a unit left out",
			result: CaptureStampDecision{},
			want:   []string{"capture"},
		},
		{
			name:   "a remove",
			result: RemoveResult{},
			want:   []string{"dry_run", "removed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			data, err := json.Marshal(tt.result)

			// Assert
			require.NoError(t, err)
			var got map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &got))
			keys := make([]string, 0, len(got))
			for key := range got {
				keys = append(keys, key)
			}
			assert.ElementsMatch(t, tt.want, keys)
		})
	}
}
