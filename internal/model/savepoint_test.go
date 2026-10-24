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
	"testing"

	"github.com/stretchr/testify/assert"
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
