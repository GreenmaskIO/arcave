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
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSessionID(t *testing.T) {
	const canonical = "0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"

	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "the default session", in: "00000000-0000-0000-0000-000000000001"},
		{name: "a canonical uuid", in: canonical},
		{name: "empty", in: "", wantErr: true},
		{name: "the nil uuid", in: "00000000-0000-0000-0000-000000000000", wantErr: true},
		{name: "the default session's name", in: DefaultSessionName, wantErr: true},
		{name: "a name", in: "feature-42", wantErr: true},
		{name: "a nested path", in: "a/b", wantErr: true},
		{name: "an escaping segment", in: "..", wantErr: true},
		// uuid.Parse takes all three; a store root may carry only one spelling.
		{name: "a braced uuid", in: "{" + canonical + "}", wantErr: true},
		{name: "a urn-prefixed uuid", in: "urn:uuid:" + canonical, wantErr: true},
		{name: "an upper-case uuid", in: "0F9C2F4A-6D3E-4B6F-9A2C-1D5E8B7A4C31", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ParseSessionID(tt.in)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidSessionID)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.in, got.String())
		})
	}
}

func TestSessionID_Validate(t *testing.T) {
	assert.NoError(t, DefaultSession.Validate())
	assert.ErrorIs(t, SessionID{}.Validate(), ErrInvalidSessionID)
}

func TestSessionID_JSON(t *testing.T) {
	// Arrange
	type wrapper struct {
		ID SessionID `json:"id"`
	}

	// Act
	data, err := json.Marshal(wrapper{ID: DefaultSession})
	require.NoError(t, err)
	var back wrapper
	backErr := json.Unmarshal(data, &back)
	badErr := json.Unmarshal([]byte(`{"id":"baseline"}`), &back)

	// Assert
	assert.JSONEq(t, `{"id":"00000000-0000-0000-0000-000000000001"}`, string(data))
	require.NoError(t, backErr)
	assert.Equal(t, DefaultSession, back.ID)
	assert.ErrorIs(t, badErr, ErrInvalidSessionID)
}

func TestNewSessionID(t *testing.T) {
	// Act
	id := NewSessionID()

	// Assert
	require.NoError(t, id.Validate())
	assert.NotEqual(t, DefaultSession, id)
	assert.Equal(t, uuid.Version(7), uuid.UUID(id).Version())
}
