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

func TestSessionID_Validate(t *testing.T) {
	const canonical = "0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"

	tests := []struct {
		name    string
		id      SessionID
		wantErr bool
	}{
		{name: "the default session", id: DefaultSession},
		{name: "a canonical uuid", id: canonical},
		{name: "empty", id: "", wantErr: true},
		{name: "a name that is neither", id: "feature-42", wantErr: true},
		{name: "a nested path", id: "a/b", wantErr: true},
		{name: "an escaping segment", id: "..", wantErr: true},
		// uuid.Parse takes all three; a store root may carry only one spelling.
		{name: "a braced uuid", id: "{" + canonical + "}", wantErr: true},
		{name: "a urn-prefixed uuid", id: "urn:uuid:" + canonical, wantErr: true},
		{name: "an upper-case uuid", id: "0F9C2F4A-6D3E-4B6F-9A2C-1D5E8B7A4C31", wantErr: true},
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
			require.ErrorContains(t, err, "invalid session id")
		})
	}
}
