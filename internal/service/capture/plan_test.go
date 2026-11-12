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

package capture

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestPlan_newNotCovered(t *testing.T) {
	containers := []model.ContainerRef{{
		ID:      "c1",
		Name:    "demo-db-1",
		Service: "db",
		Replica: 1,
	}}
	mounts := []model.SkippedVolume{{Service: "db", Target: "/tmp"}}

	tests := []struct {
		name       string
		notCovered model.NotCovered
		killed     []string
		want       *model.NotCovered
	}{
		{
			name: "nothing left out is nil",
		},
		{
			name:       "what the plan leaves out is kept",
			notCovered: model.NotCovered{Mounts: mounts},
			want:       &model.NotCovered{Mounts: mounts},
		},
		{
			name:   "a killed container is named by its replica",
			killed: []string{"c1"},
			want: &model.NotCovered{KilledContainers: []model.KilledContainer{{
				Service:   "db",
				Replica:   1,
				Container: "demo-db-1",
			}}},
		},
		{
			name:   "an unknown id is kept as is",
			killed: []string{"gone"},
			want:   &model.NotCovered{KilledContainers: []model.KilledContainer{{Container: "gone"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			plan := Plan{
				Containers: containers,
				NotCovered: tt.notCovered,
			}

			// Act
			got := plan.newNotCovered(tt.killed)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
