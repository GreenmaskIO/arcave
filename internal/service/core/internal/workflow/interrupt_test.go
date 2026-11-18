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

package workflow

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	demoTarget model.SavepointID = "019e843e-1650-7000-8000-000000000000"
	demoSafety model.SavepointID = "019e844b-7818-7000-8000-000000000000"
)

// demoMarker is the marker a rollback to demoTarget leaves, its safety snapshot demoSafety.
func demoMarker(phase model.InflightPhase) model.InflightMarker {
	return model.InflightMarker{
		Phase:          phase,
		Session:        model.DefaultSession,
		ComposeProject: "demo",
		Target:         demoTarget,
		Safety:         demoSafety,
		At:             time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}
}

func TestLogInterrupt(t *testing.T) {
	tests := []struct {
		name        string
		interrupted bool
		want        string
	}{
		{name: "a live context logs nothing"},
		{name: "an interrupted context logs the action", interrupted: true,
			want: `{"level":"info","message":"restarting services"}` + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var buf bytes.Buffer
			ctx, cancel := context.WithCancel(zerolog.New(&buf).WithContext(context.Background()))
			defer cancel()
			if tt.interrupted {
				cancel()
			}

			// Act
			LogInterrupt(ctx, "restarting services")

			// Assert
			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func TestRecoveryHint_NamesSessionAndComposeProject(t *testing.T) {
	// Arrange
	marker := demoMarker(model.InflightRestoring)
	marker.Session = demoSession
	marker.ComposeProject = "demo"

	// Act
	hint := RecoveryHint(marker)

	// Assert
	assert.Contains(t, hint, "arcave rollback --session "+demoSession.String()+" -p demo "+marker.Safety.String())
}

// With no safety snapshot to go back to, the hint names the same rollback again.
func TestRecoveryHint_NoSafetySnapshot(t *testing.T) {
	// Arrange
	marker := model.InflightMarker{
		Phase:          model.InflightRestoring,
		Session:        model.DefaultSession,
		ComposeProject: "demo",
		Target:         demoTarget,
	}

	// Act
	hint := RecoveryHint(marker)

	// Assert
	assert.Contains(t, hint, "a rollback that took no safety snapshot was interrupted; volumes may be half-restored")
	assert.Contains(t, hint, "-p demo "+demoTarget.String())
}

// What the hint says of a rollback that took no safety snapshot follows how far it got.
func TestInterruptedState_NoSafetySnapshot(t *testing.T) {
	tests := []struct {
		phase model.InflightPhase
		want  string
	}{
		{phase: model.InflightRecreating, want: "before it restored any volume; the containers may be gone"},
		{phase: model.InflightRestoring, want: "volumes may be half-restored"},
		// A failed cold start stands here too, what it restored into removed.
		{phase: model.InflightRestoreDone, want: "volumes may be half-restored"},
	}
	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			assert.Contains(t, interruptedState(model.InflightMarker{Phase: tt.phase}), tt.want)
		})
	}
}
