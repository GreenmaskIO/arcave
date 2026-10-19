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

func TestInflightMarker_RecoveryID(t *testing.T) {
	const (
		target = SavepointID("019e843e-1650-7000-8000-000000000000")
		safety = SavepointID("019e844b-7818-7000-8000-000000000000")
	)
	tests := []struct {
		name   string
		marker InflightMarker
		want   SavepointID
	}{
		{name: "the safety snapshot", marker: InflightMarker{Target: target, Safety: safety}, want: safety},
		{name: "the target when no safety snapshot was taken", marker: InflightMarker{Target: target}, want: target},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.marker.RecoveryID())
		})
	}
}
