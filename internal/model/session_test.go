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

func TestSession_JSON_FromNamesASessionOnly(t *testing.T) {
	// Arrange
	record := Session{ID: DefaultSession, Name: "feat", From: &SessionOrigin{Session: DefaultSession}}

	// Act
	data, err := json.Marshal(record)

	// Assert
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"`+DefaultSession.String()+`","name":"feat","from":{"session":"`+DefaultSession.String()+`"}}`, string(data))
}

func TestFindSessionByRef(t *testing.T) {
	var (
		feat  = Session{ID: SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x01}, Name: "feat"}
		fix   = Session{ID: SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x02}, Name: "fix"}
		other = Session{ID: SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x03}, Name: "feat"}
	)
	// shadow carries fix's id as its name: a name is looked up before an id.
	shadow := Session{ID: SessionID{0x01, 0x9a, 0x70, 0, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 0x04}, Name: fix.ID.String()}
	tests := []struct {
		name          string
		sessions      []Session
		ref           string
		want          Session
		wantErr       error
		wantAmbiguous *AmbiguousSessionNameError
	}{
		{name: "by name", sessions: []Session{feat, fix}, ref: "fix", want: fix},
		{name: "by id", sessions: []Session{feat, fix}, ref: feat.ID.String(), want: feat},
		{name: "a name shadows another session's id", sessions: []Session{fix, shadow}, ref: fix.ID.String(), want: shadow},
		{name: "none", sessions: []Session{feat}, ref: "nope", wantErr: ErrSessionNotFound},
		{
			name:          "two carrying the name",
			sessions:      []Session{feat, fix, other},
			ref:           "feat",
			wantAmbiguous: &AmbiguousSessionNameError{Name: "feat", IDs: []SessionID{feat.ID, other.ID}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := FindSessionByRef(tt.sessions, tt.ref)

			// Assert
			switch {
			case tt.wantAmbiguous != nil:
				var ambiguous *AmbiguousSessionNameError
				require.ErrorAs(t, err, &ambiguous)
				assert.Equal(t, tt.wantAmbiguous, ambiguous)
			case tt.wantErr != nil:
				assert.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
