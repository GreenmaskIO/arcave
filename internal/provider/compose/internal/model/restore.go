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
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
)

// RestoreDetail is the compose provider's body of what building a savepoint restores.
type RestoreDetail struct {
	Volumes        []Volume                     `json:"volumes"`
	WritableLayers []WritableLayerRestoreResult `json:"writable_layers,omitempty"`
}

// NewRestoreSummary sums detail up: volumes always, layers when it restores any, and its notices,
// the leftover volumes it overwrites first.
func NewRestoreSummary(detail RestoreDetail, leftover []string) model.RestoreSummary {
	units := []model.UnitTotal{{Kind: model.UnitKindVolumes, Count: len(detail.Volumes)}}
	if len(detail.WritableLayers) > 0 {
		units = append(units, model.UnitTotal{Kind: model.UnitKindLayers, Count: len(detail.WritableLayers)})
	}
	notices := NewLeftoverVolumeNotices(leftover)
	if len(detail.WritableLayers) > 0 {
		notices = append(notices, model.Notice{
			Message: "the containers run on the savepoint's writable layers",
			Hint:    "`docker compose up` of your compose file recreates them on its images and drops those layers",
		})
	}
	return model.RestoreSummary{
		Units:   units,
		Notices: notices,
	}
}

// NewLeftoverVolumeNotices say, one per volume, that a rollback overwrote a volume no safety
// snapshot holds.
func NewLeftoverVolumeNotices(leftover []string) []model.Notice {
	var notices []model.Notice
	for _, name := range leftover {
		notices = append(notices, model.Notice{
			Message: fmt.Sprintf("volume %s outlived the compose project's containers and was overwritten: "+
				"no safety snapshot holds what it held", name),
			Hint:   "compose.leftover_volumes: refuse refuses such a rollback before anything is written",
			Severe: true,
		})
	}
	return notices
}
