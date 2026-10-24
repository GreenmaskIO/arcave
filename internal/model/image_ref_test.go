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

func TestImageRef_RestoredLayerSavepoint(t *testing.T) {
	tests := []struct {
		ref  ImageRef
		want SavepointID
	}{
		{ref: layerRef, want: "019e7044-8fd0-7000-8000-000000000000"},
		{ref: "arcave/demo-db-1:sp-019e7044-8be8-7000-8000-000000000000", want: "019e7044-8be8-7000-8000-000000000000"},
		{ref: "postgres:16"},
		{ref: "arcave/demo-db-1:latest"},
		{ref: "arcave/demo-db-1:sp-abc"},
		{ref: "registry:5000/arcave/x:sp-019e7044-8be8-7000-8000-000000000000"},
	}

	for _, tt := range tests {
		t.Run(string(tt.ref), func(t *testing.T) {
			// Act
			got, ok := tt.ref.RestoredLayerSavepoint()

			// Assert
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want != "", ok)
			assert.Equal(t, ok, tt.ref.IsRestoredLayerTag())
		})
	}
}
