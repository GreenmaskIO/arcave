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

func TestCreateSavepointRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		reqName string
		wantErr bool
	}{
		{name: "no name", reqName: ""},
		{name: "a plain name", reqName: "before-migration"},
		{name: "a run of digits", reqName: "1780335122"},
		{name: "a slash", reqName: "feat/v1", wantErr: true},
		{name: "the reserved ref", reqName: RefLatest, wantErr: true},
		{name: "a savepoint id", reqName: "019e843e-1650-7000-8000-000000000000", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := CreateSavepointRequest{Name: tt.reqName}.Validate()

			// Assert
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidSavepointName)
		})
	}
}
