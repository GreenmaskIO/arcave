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

func TestInflightMarker_AcceptsAnySavepoint(t *testing.T) {
	const (
		target = SavepointID("019e843e-1650-7000-8000-000000000000")
		safety = SavepointID("019e844b-7818-7000-8000-000000000000")
	)
	tests := []struct {
		name   string
		marker InflightMarker
		want   bool
	}{
		{name: "no safety snapshot", marker: InflightMarker{Target: target}, want: true},
		{name: "a safety snapshot not yet tried", marker: InflightMarker{Target: target, Safety: safety}, want: false},
		{name: "a recovery to the safety snapshot failed", marker: InflightMarker{Target: safety, Safety: safety}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.marker.AcceptsAnySavepoint()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// What the error says the rollback left follows how far it got.
func TestInterruptedRollbackError_Error(t *testing.T) {
	const (
		target = SavepointID("019e843e-1650-7000-8000-000000000000")
		safety = SavepointID("019e844b-7818-7000-8000-000000000000")
	)
	tests := []struct {
		name   string
		marker InflightMarker
		want   string
	}{
		{
			name:   "no safety snapshot, before any volume",
			marker: InflightMarker{Phase: InflightRecreating, Target: target},
			want: "rollback to savepoint " + target.String() + " was interrupted (recreating): a rollback that took " +
				"no safety snapshot was interrupted before it restored any volume; the containers may be gone",
		},
		{
			name:   "no safety snapshot, restoring",
			marker: InflightMarker{Phase: InflightRestoring, Target: target},
			want: "rollback to savepoint " + target.String() + " was interrupted (restoring): a rollback that took " +
				"no safety snapshot was interrupted; volumes may be half-restored",
		},
		{
			// A failed cold start stands here too, what it restored into removed.
			name:   "no safety snapshot, restore done",
			marker: InflightMarker{Phase: InflightRestoreDone, Target: target},
			want: "rollback to savepoint " + target.String() + " was interrupted (restore-done): a rollback that took " +
				"no safety snapshot was interrupted; volumes may be half-restored",
		},
		{
			name:   "restoring with a safety snapshot",
			marker: InflightMarker{Phase: InflightRestoring, Target: target, Safety: safety},
			want:   "rollback to savepoint " + target.String() + " was interrupted (restoring): volumes may mix states",
		},
		{
			name:   "a failed recovery to the safety snapshot",
			marker: InflightMarker{Phase: InflightRestoring, Target: safety, Safety: safety},
			want: "rollback to savepoint " + safety.String() + " was interrupted (restoring): the recovery to the " +
				"safety snapshot failed; volumes may be half-restored",
		},
		{
			name:   "inconsistent",
			marker: InflightMarker{Phase: InflightInconsistent, Target: target, Safety: safety},
			want: "rollback to savepoint " + target.String() + " was interrupted (inconsistent): the rollback failed " +
				"and so did its revert",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			err := &InterruptedRollbackError{Marker: tt.marker}

			// Act
			got := err.Error()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
