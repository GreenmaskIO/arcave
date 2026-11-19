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

package inflightmarker

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func newTestInflightMarkerVolume(safety model.SavepointID) inflightMarkerVolume {
	return inflightMarkerVolume{
		ArcaveProject: "demo-arcave",
		Marker: model.InflightMarker{
			Phase:          model.InflightRestoring,
			Session:        model.DefaultSession,
			ComposeProject: "demo",
			Target:         "019e843e-1650-7000-8000-000000000001",
			Safety:         safety,
			At:             time.Date(2026, 11, 19, 10, 0, 0, 123456789, time.UTC),
		},
	}
}

// A marker survives its labels whole, a missing safety snapshot included.
func TestInflightMarkerVolume_Labels(t *testing.T) {
	tests := []struct {
		name   string
		safety model.SavepointID
	}{
		{name: "with a safety snapshot", safety: "019e843e-1650-7000-8000-000000000002"},
		{name: "without one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			markerVolume := newTestInflightMarkerVolume(tt.safety)

			// Act
			parsed, err := parseInflightMarkerVolume(markerVolume.Labels())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, markerVolume, parsed)
		})
	}
}

// A marker reads back from its labels; a label it requires that is blank or does not read is an
// error naming the label.
func TestParseInflightMarkerVolume(t *testing.T) {
	valid := newTestInflightMarkerVolume("019e843e-1650-7000-8000-000000000002")
	tests := []struct {
		name    string
		label   model.SystemLabel
		value   string
		want    inflightMarkerVolume
		wantErr string
	}{
		{name: "every label reads", want: valid},
		{name: "a blank description", label: labelDescription, want: valid},
		{name: "no arcave project", label: labelArcaveProject, wantErr: "arcave-project"},
		{name: "no compose project", label: labelComposeProject, wantErr: "compose-project"},
		{name: "a session that is no id", label: labelSession, value: "feat", wantErr: "session"},
		{name: "no target", label: labelTarget, wantErr: "target"},
		{name: "a safety snapshot that is no id", label: labelSafety, value: "x", wantErr: "safety"},
		{name: "an unknown phase", label: labelPhase, value: "done", wantErr: "phase"},
		{name: "no time", label: labelAt, wantErr: "at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			labels := valid.Labels()
			if tt.label != "" {
				labels[tt.label.String()] = tt.value
			}

			// Act
			parsed, err := parseInflightMarkerVolume(labels)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "io.arcave.inflight."+tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, parsed)
		})
	}
}

// The compose project is cut short in the name, so a long one still makes a name the daemon takes.
func TestNewInflightMarkerVolumeName(t *testing.T) {
	tests := []struct {
		name           string
		composeProject model.DockerComposeProjectName
		want           string
	}{
		{name: "a short compose project", composeProject: "demo", want: "arcave-inflight-demo-s"},
		{
			name:           "a long compose project",
			composeProject: model.DockerComposeProjectName(strings.Repeat("p", 300)),
			want:           "arcave-inflight-" + strings.Repeat("p", 64) + "-s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newInflightMarkerVolumeName(tt.composeProject, "s")

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
