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

func demoSummary() SavepointSummary {
	return SavepointSummary{
		Units: []UnitTotal{
			{Kind: UnitKindVolumes, Count: 2, SizeBytes: 10, StoredBytes: 4},
			{Kind: UnitKindLayers, Count: 1, SizeBytes: 7, StoredBytes: 3},
		},
	}
}

func TestSavepointSummary_Unit(t *testing.T) {
	tests := []struct {
		name string
		kind UnitKind
		want UnitTotal
	}{
		{name: "a kind held", kind: UnitKindLayers, want: UnitTotal{Kind: UnitKindLayers, Count: 1, SizeBytes: 7, StoredBytes: 3}},
		{name: "a kind not held is its zero tally", kind: UnitKindImages, want: UnitTotal{Kind: UnitKindImages}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := demoSummary().Unit(tt.kind)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSavepointSummary_StoredBytes(t *testing.T) {
	// Act
	got := demoSummary().StoredBytes()

	// Assert
	assert.Equal(t, int64(7), got, "every kind together")
}
