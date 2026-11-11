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

// Every overwritten leftover volume comes first, severe; the writable layers after.
func TestRollbackResult_Notices(t *testing.T) {
	layers := Notice{
		Message: "the containers run on the savepoint's writable layers",
		Hint:    "`docker compose up` of your compose file recreates them on its images and drops those layers",
	}
	tests := []struct {
		name   string
		result RollbackResult
		want   []Notice
	}{
		{name: "nothing to say", result: RollbackResult{}},
		{
			name:   "the writable layers alone",
			result: RollbackResult{WritableLayers: []WritableLayerRestoreResult{{Service: "app", Replica: 1}}},
			want:   []Notice{layers},
		},
		{
			name: "the leftover volumes, then the writable layers",
			result: RollbackResult{
				WritableLayers:  []WritableLayerRestoreResult{{Service: "app", Replica: 1}},
				LeftoverVolumes: []string{"shop_pgdata", "shop_cache"},
			},
			want: []Notice{
				{
					Message: "volume shop_pgdata outlived the compose project's containers and was overwritten: " +
						"no safety snapshot holds what it held",
					Hint:   "rollback.leftover_volumes: refuse refuses such a rollback before anything is written",
					Severe: true,
				},
				{
					Message: "volume shop_cache outlived the compose project's containers and was overwritten: " +
						"no safety snapshot holds what it held",
					Hint:   "rollback.leftover_volumes: refuse refuses such a rollback before anything is written",
					Severe: true,
				},
				layers,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.result.Notices()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
