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

func TestArcaveProjectName_Validate(t *testing.T) {
	tests := []struct {
		name    string
		project ArcaveProjectName
		wantErr bool
	}{
		{name: "a plain name", project: "demo"},
		{name: "digits, dashes and underscores", project: "demo_2-x"},
		{name: "a leading digit", project: "0demo"},
		{name: "empty", project: "", wantErr: true},
		{name: "upper case", project: "MyApp", wantErr: true},
		{name: "a nested path", project: "a/b", wantErr: true},
		{name: "an escaping segment", project: "..", wantErr: true},
		{name: "a leading dash", project: "-demo", wantErr: true},
		{name: "a leading underscore", project: "_demo", wantErr: true},
		{name: "a space", project: "my app", wantErr: true},
		{name: "non-ascii", project: "日本語", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.project.Validate()

			// Assert
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "invalid arcave project name")
		})
	}
}

// The normalization a default name goes through: a directory arcave is run in
// is whatever the user called it, and the tenant segment has to be addressable.
// Whatever it produces must pass Validate, or be empty and say so.
func TestNormalizeArcaveProjectName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ArcaveProjectName
	}{
		{name: "already normal", in: "demo", want: "demo"},
		{name: "mixed case", in: "MyApp", want: "myapp"},
		{name: "punctuation and spaces", in: "My App!", want: "myapp"},
		{name: "a leading underscore", in: "_x", want: "x"},
		{name: "a leading dash", in: "--x-y", want: "x-y"},
		{name: "a dotted directory name", in: "arcave.demo.v2", want: "arcavedemov2"},
		{name: "nothing in the charset", in: "日本語", want: ""},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NormalizeArcaveProjectName(tt.in)

			// Assert
			assert.Equal(t, tt.want, got)
			if got == "" {
				require.Error(t, got.Validate(), "an empty result must be caught by the gate")
				return
			}
			assert.NoError(t, got.Validate(), "a normalized name must pass the gate")
		})
	}
}
