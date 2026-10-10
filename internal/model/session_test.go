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

func TestCreateSessionRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		reqName string
		wantErr bool
	}{
		{name: "unnamed", reqName: ""},
		{name: "a plain name", reqName: "experiment"},
		{name: "a branch-like name", reqName: "feat/add-new-table"},
		{name: "blank", reqName: "  ", wantErr: true},
		{name: "a uuid-shaped name", reqName: "0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := CreateSessionRequest{Name: tt.reqName}.Validate()

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidSessionName)
				return
			}
			assert.NoError(t, err)
		})
	}
}
