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

func TestSavepointID_Validate(t *testing.T) {
	const canonical = "019e843e-1650-7000-8000-000000000000"

	tests := []struct {
		name    string
		id      SavepointID
		wantErr bool
	}{
		{name: "a canonical uuid", id: canonical},
		{name: "empty", id: "", wantErr: true},
		{name: "a unix second", id: "1780335122", wantErr: true},
		{name: "the shared sibling", id: "shared", wantErr: true},
		{name: "a nested path", id: "a/b", wantErr: true},
		{name: "an escaping segment", id: "..", wantErr: true},
		// uuid.Parse takes all three; a storage key may carry only one spelling.
		{name: "a braced uuid", id: "{" + canonical + "}", wantErr: true},
		{name: "a urn-prefixed uuid", id: "urn:uuid:" + canonical, wantErr: true},
		{name: "an upper-case uuid", id: "019E843E-1650-7000-8000-000000000000", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.id.Validate()

			// Assert
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidSavepointID)
		})
	}
}

func TestNewSavepointID(t *testing.T) {
	// Act
	first := NewSavepointID()
	second := NewSavepointID()

	// Assert
	require.NoError(t, first.Validate())
	require.NoError(t, second.Validate())
	assert.Less(t, first.String(), second.String(), "a later id sorts after an earlier one")
}
