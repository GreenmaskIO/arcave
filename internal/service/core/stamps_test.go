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
	"context"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// mountingProject is a spec whose db mounts pgdata at /data, its labels as the case says.
func mountingProject(labels types.Labels) *types.Project {
	return &types.Project{Services: types.Services{"db": {
		Name:   "db",
		Labels: labels,
		Volumes: []types.ServiceVolumeConfig{
			{Type: types.VolumeTypeVolume, Source: "pgdata", Target: "/data"},
			{Type: types.VolumeTypeVolume, Target: "/cache"},
		},
	}}}
}

func TestCheckCaptureStamps(t *testing.T) {
	record := model.Savepoint{
		Stats: []model.SnapshotStat{{Volume: model.Volume{ObjectID: "volume-named-demo_pgdata", Named: &model.VolumePropertiesNamed{
			VolumeName: "demo_pgdata", UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}},
		}}}},
	}
	tests := []struct {
		name      string
		labels    types.Labels
		wantErr   string
		wantErrIs error
	}{
		{name: "a spec with no capture stamps checks nothing"},
		{name: "every mount the spec captures is held", labels: types.Labels{
			model.MountStamp("/data"):  "true; backend=fullcopy; origin=config",
			model.MountStamp("/cache"): "false; origin=label; source=arcave.mounts.exclude=/cache on service db",
		}},
		{name: "a mount the spec captures and the record lacks is refused", labels: types.Labels{
			model.MountStamp("/cache"): "true; backend=fullcopy; origin=config",
		}, wantErr: "the mount at /cache of service db", wantErrIs: errSpecRecordMismatch},
		{name: "a layer the spec leaves out needs nothing of the record", labels: types.Labels{
			model.StampWritableLayer: "false; origin=config; source=capture.writable_layers=false in the config",
		}},
		{name: "a layer the spec captures and the record lacks is refused", labels: types.Labels{
			model.StampWritableLayer: "true; backend=dedupcopy; origin=config",
		}, wantErr: "the writable layer of service db", wantErrIs: errSpecRecordMismatch},
		{name: "a stamp arcave cannot read is refused", labels: types.Labels{
			model.StampWritableLayer: "yes",
		}, wantErr: "want true or false first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := checkCaptureStamps(zerolog.Nop().WithContext(context.Background()), record, mountingProject(tt.labels))

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
			}
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// The stamps go onto the services they name, beside the user's labels, and nowhere else.
func TestStampServices(t *testing.T) {
	// Arrange
	project := &types.Project{Services: types.Services{"db": {Name: "db", Labels: types.Labels{"tier": "data"}}}}

	// Act
	stampServices(project, map[string]map[string]string{
		"db":   {model.StampWritableLayer: "true; origin=config"},
		"gone": {model.StampWritableLayer: "false; origin=config"},
	})

	// Assert
	assert.Equal(t, types.Labels{"tier": "data", model.StampWritableLayer: "true; origin=config"}, project.Services["db"].Labels)
	assert.NotContains(t, project.Services, "gone")
}
