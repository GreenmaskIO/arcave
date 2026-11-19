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

func TestPlan_NewNotCovered(t *testing.T) {
	containers := []ContainerRef{{
		ID:      "c1",
		Name:    "demo-db-1",
		Service: "db",
		Replica: 1,
	}}
	mounts := []SkippedVolume{{Service: "db", Target: "/tmp"}}

	tests := []struct {
		name        string
		notCovered  NotCovered
		killed      []string
		failedHooks []FailedHook
		want        *NotCovered
	}{
		{
			name: "nothing left out is nil",
		},
		{
			name:       "what the plan leaves out is kept",
			notCovered: NotCovered{Mounts: mounts},
			want:       &NotCovered{Mounts: mounts},
		},
		{
			name:   "a killed container is named by its replica",
			killed: []string{"c1"},
			want: &NotCovered{KilledContainers: []KilledContainer{{
				Service:   "db",
				Replica:   1,
				Container: "demo-db-1",
			}}},
		},
		{
			name:   "an unknown id is kept as is",
			killed: []string{"gone"},
			want:   &NotCovered{KilledContainers: []KilledContainer{{Container: "gone"}}},
		},
		{
			name:        "a failed hook alone leaves something out",
			failedHooks: []FailedHook{{Service: "db", Replica: 1, Hook: HookPhasePreCapture}},
			want:        &NotCovered{FailedHooks: []FailedHook{{Service: "db", Replica: 1, Hook: HookPhasePreCapture}}},
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
			got := plan.NewNotCovered(tt.killed, tt.failedHooks)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
