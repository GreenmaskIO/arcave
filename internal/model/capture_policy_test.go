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
	"github.com/stretchr/testify/require"
)

func TestCaptureStamp_String(t *testing.T) {
	tests := []struct {
		name  string
		stamp CaptureStamp
		want  string
	}{
		{name: "captured by the config", stamp: CaptureStamp{Capture: true, Backend: "fullcopy", Origin: OriginConfig},
			want: "true; backend=fullcopy; origin=config"},
		{name: "left out by a label", stamp: CaptureStamp{Origin: OriginLabel, Source: "arcave.capture=false on service app"},
			want: "false; origin=label; source=arcave.capture=false on service app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.stamp.String()

			// Assert
			assert.Equal(t, tt.want, got)
			back, err := ParseCaptureStamp(got)
			require.NoError(t, err)
			assert.Equal(t, tt.stamp, back)
		})
	}
}

func TestParseCaptureStamp(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    CaptureStamp
		wantErr string
	}{
		{name: "a source holding the separator is kept whole",
			value: "false; origin=label; source=arcave.mounts.exclude=/a; b on service app",
			want:  CaptureStamp{Origin: OriginLabel, Source: "arcave.mounts.exclude=/a; b on service app"}},
		{name: "not a bool first", value: "yes; origin=config", wantErr: "want true or false first"},
		{name: "an unknown field", value: "true; colour=red", wantErr: `unknown field "colour"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ParseCaptureStamp(tt.value)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
