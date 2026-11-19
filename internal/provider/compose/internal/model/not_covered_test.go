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
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestNotCovered_Notices(t *testing.T) {
	tests := []struct {
		name       string
		notCovered NotCovered
		want       []model.Notice
	}{
		{
			name: "nothing left out",
		},
		{
			name: "a killed replica names the knobs that would have spared it",
			notCovered: NotCovered{KilledContainers: []KilledContainer{
				{Service: "db", Replica: 1, Container: "0123456789abcdef0123"},
				{Container: "0123456789abcdef0123"},
			}},
			want: []model.Notice{
				{
					Message: "db-1 was killed at the stop deadline: its volumes may hold a crash state",
					Hint:    "raise compose.stop_timeout or the service's stop_grace_period",
					Severe:  true,
				},
				{
					Message: "0123456789ab was killed at the stop deadline: its volumes may hold a crash state",
					Hint:    "raise compose.stop_timeout or the service's stop_grace_period",
					Severe:  true,
				},
			},
		},
		{
			name: "a failed hook goes right after the killed, severe, naming the label that would refuse it",
			notCovered: NotCovered{
				ReplicaGaps: []ReplicaGap{{Service: "web", Replicas: []int{1, 3}, Missing: []int{2}}},
				FailedHooks: []FailedHook{{
					Service:   "db",
					Replica:   1,
					Container: "demo-db-1",
					Hook:      HookPhasePreCapture,
					ExitCode:  new(3),
					Error:     "exit 3: psql: connection refused",
				}},
				KilledContainers: []KilledContainer{{Service: "app", Replica: 1, Container: "demo-app-1"}},
			},
			want: []model.Notice{
				{
					Message: "app-1 was killed at the stop deadline: its volumes may hold a crash state",
					Hint:    "raise compose.stop_timeout or the service's stop_grace_period",
					Severe:  true,
				},
				{
					Message: "db-1: pre-capture hook failed (exit 3: psql: connection refused), captured anyway: " +
						"the application may not have been prepared",
					Hint:   "fix the hook, or set arcave.hook.pre-capture.on-error: fail to refuse such a capture",
					Severe: true,
				},
				{Message: "web: replica 2 gone, only 1, 3 held", Severe: true},
			},
		},
		{
			name: "an excluded mount names what left it out, any other its reason",
			notCovered: NotCovered{Mounts: []SkippedVolume{
				{Service: "app", Target: "/cache", Reason: SkipReasonExcluded, Detail: "arcave.mounts.exclude=/cache on service app"},
				{Volume: "demo_tmp", Reason: SkipReasonExcluded},
				{Service: "app", Replica: 2, Target: "/run", Reason: SkipReasonTmpfs, Detail: "non-persistent (tmpfs lives in RAM)"},
				{Target: "/srv", Reason: SkipReasonUnknownType, Detail: "unknown mount type"},
			}},
			want: []model.Notice{
				{
					Message: "app /cache is left out",
					Hint:    "arcave.mounts.exclude=/cache on service app leaves it out",
				},
				{
					Message: "volume demo_tmp is left out",
					Hint:    "an arcave.capture=false or arcave.mounts.exclude label leaves it out",
				},
				{Message: "app-2 /run is not held: non-persistent (tmpfs lives in RAM)"},
				{Message: "mount /srv is not held: unknown mount type"},
			},
		},
		{
			name: "the facts carry no hint, a replica gap going first",
			notCovered: NotCovered{
				ServicesWithoutVolumes: []string{"app"},
				ReplicaGaps:            []ReplicaGap{{Service: "web", Replicas: []int{1, 3}, Missing: []int{2}}},
				UnresolvedImages:       []UnresolvedImage{{Service: "web", Replica: 1, Container: "demo-web-1", Image: "sha256:ab"}},
				WritableLayers: []SkippedWritableLayer{
					{Service: "web", Replica: 3, Container: "demo-web-3", Reason: SkipReasonExcluded, Detail: "arcave.writable-layer.capture=false on service web"},
				},
				Images: []SkippedImage{{Service: "web", Image: "nginx:1", Reason: SkipReasonExcluded}},
			},
			want: []model.Notice{
				{Message: "web: replica 2 gone, only 1, 3 held", Severe: true},
				{Message: "app has no volumes: only its writable layer is held"},
				{Message: "web-1 runs on an image arcave cannot name (sha256:ab): only its writable layer brings it back"},
				{Message: "web-3: writable layer left out by arcave.writable-layer.capture=false on service web, " +
					"a rollback creates it fresh from its image"},
				{Message: "web: image nginx:1 left out"},
			},
		},
		{
			name: "a service without volumes whose layer is left out claims no layer held",
			notCovered: NotCovered{
				ServicesWithoutVolumes: []string{"web"},
				WritableLayers: []SkippedWritableLayer{
					{Service: "web", Replica: 1, Container: "demo-web-1", Reason: SkipReasonExcluded},
				},
			},
			want: []model.Notice{
				{Message: "web has no volumes"},
				{Message: "web-1: writable layer left out, a rollback creates it fresh from its image"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.notCovered.Notices()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNotCovered_IsEmpty(t *testing.T) {
	tests := []struct {
		name       string
		notCovered NotCovered
		want       bool
	}{
		{
			name: "the zero value",
			want: true,
		},
		{
			name:       "a failed hook alone",
			notCovered: NotCovered{FailedHooks: []FailedHook{{Service: "db"}}},
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.notCovered.IsEmpty()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// What is left out travels in the record, so its keys are the wire contract.
func TestNotCovered_JSONKeys(t *testing.T) {
	tests := []struct {
		name   string
		result any
		want   []string
	}{
		{
			name:   "a failed hook without an exit code omits it",
			result: FailedHook{},
			want:   []string{"service", "replica", "container", "hook", "error"},
		},
		{
			name:   "a failed hook that exited",
			result: FailedHook{ExitCode: new(3)},
			want:   []string{"service", "replica", "container", "hook", "exit_code", "error"},
		},
		{
			name:   "not covered by a failed hook",
			result: NotCovered{FailedHooks: []FailedHook{{}}},
			want:   []string{"failed_hooks"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			data, err := json.Marshal(tt.result)

			// Assert
			require.NoError(t, err)
			var got map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &got))
			assert.ElementsMatch(t, tt.want, slices.Collect(maps.Keys(got)))
		})
	}
}
