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
package model

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

// Volumes are counted always and layers when restored; the leftover volumes are said first, the
// writable layers' hint after.
func TestNewRestoreSummary(t *testing.T) {
	layersHint := model.Notice{
		Message: "the containers run on the savepoint's writable layers",
		Hint:    "`docker compose up` of your compose file recreates them on its images and drops those layers",
	}
	tests := []struct {
		name     string
		detail   RestoreDetail
		leftover []string
		want     model.RestoreSummary
	}{
		{name: "nothing restored", want: model.RestoreSummary{Units: []model.UnitTally{{Kind: model.UnitKindVolumes}}}},
		{
			name:   "the writable layers alone",
			detail: RestoreDetail{Volumes: []Volume{{}}, WritableLayers: []WritableLayerRestoreResult{{}}},
			want: model.RestoreSummary{
				Units:   []model.UnitTally{{Kind: model.UnitKindVolumes, Count: 1}, {Kind: model.UnitKindLayers, Count: 1}},
				Notices: []model.Notice{layersHint},
			},
		},
		{
			name:     "the leftover volumes, then the writable layers",
			detail:   RestoreDetail{WritableLayers: []WritableLayerRestoreResult{{}}},
			leftover: []string{"shop_pgdata", "shop_cache"},
			want: model.RestoreSummary{
				Units:   []model.UnitTally{{Kind: model.UnitKindVolumes}, {Kind: model.UnitKindLayers, Count: 1}},
				Notices: append(NewLeftoverVolumeNotices([]string{"shop_pgdata", "shop_cache"}), layersHint),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewRestoreSummary(tt.detail, tt.leftover)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewLeftoverVolumeNotices(t *testing.T) {
	// Act
	got := NewLeftoverVolumeNotices([]string{"shop_pgdata"})

	// Assert
	assert.Equal(t, []model.Notice{{
		Message: "volume shop_pgdata outlived the compose project's containers and was overwritten: " +
			"no safety snapshot holds what it held",
		Hint:   "compose.leftover_volumes: refuse refuses such a rollback before anything is written",
		Severe: true,
	}}, got)
}
