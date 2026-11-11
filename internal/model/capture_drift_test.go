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

// One notice per service, each unit spelled was → now; none for a nil drift.
func TestCaptureDrift_Notices(t *testing.T) {
	tests := []struct {
		name  string
		drift *CaptureDrift
		want  []Notice
	}{
		{name: "no drift"},
		{
			name: "grouped by service, the savepoint named by its name",
			drift: &CaptureDrift{
				Since: SavepointRef{ID: "019e843e-1650-7000-8000-000000000000", Name: "sp"},
				Changes: []CaptureChange{
					{Service: "app", Stamp: "mount./var/lib/postgresql/data",
						Was: CaptureStampDecision{Capture: true, Backend: "fullcopy"}, Now: CaptureStampDecision{Capture: true, Backend: "cdc"}},
					{Service: "app", Stamp: "image", Was: CaptureStampDecision{}, Now: CaptureStampDecision{Capture: true}},
					{Service: "db", Stamp: "writable-layer",
						Was: CaptureStampDecision{Capture: true, Backend: "dedupcopy"}, Now: CaptureStampDecision{}},
				},
			},
			want: []Notice{
				{Message: "app captured otherwise than in savepoint sp: " +
					"mount /var/lib/postgresql/data fullcopy → cdc, image left out → kept"},
				{Message: "db captured otherwise than in savepoint sp: writable-layer dedupcopy → left out"},
			},
		},
		{
			name: "an unnamed savepoint by its id",
			drift: &CaptureDrift{
				Since:   SavepointRef{ID: "019e843e-1650-7000-8000-000000000000"},
				Changes: []CaptureChange{{Service: "app", Stamp: "image", Was: CaptureStampDecision{Capture: true}}},
			},
			want: []Notice{{Message: "app captured otherwise than in savepoint 019e843e-1650-7000-8000-000000000000: " +
				"image kept → left out"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.drift.Notices()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
