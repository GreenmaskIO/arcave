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

func TestParseLayerOwner(t *testing.T) {
	origin := LayerOrigin{ArcaveProject: "app", ComposeProject: "demo"}

	tests := []struct {
		name   string
		labels map[string]string
		want   LayerOrigin
		wantOK bool
	}{
		{name: "both labels name the origin", labels: origin.Labels(), want: origin, wantOK: true},
		{name: "no labels name none", labels: nil},
		{name: "one label alone names none", labels: map[string]string{string(SystemLabelArcaveProject): "app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, ok := ParseLayerOrigin(tt.labels)

			// Assert
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
