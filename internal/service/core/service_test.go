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

package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestFanOutLimit(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		want        int
	}{
		{name: "a configured concurrency is the limit", concurrency: 5, want: 5},
		{name: "one runs the fan-out serially", concurrency: 1, want: 1},
		{name: "zero leaves the fan-out unbounded", concurrency: 0, want: -1},
		{name: "a negative concurrency leaves it unbounded", concurrency: -3, want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := fanOutLimit(tt.concurrency)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReadSessionRef(t *testing.T) {
	loadErr := errors.New("store unreachable")
	tests := []struct {
		name    string
		id      model.SessionID
		setup   func(sessions *mockSessionRepo)
		want    model.SessionRef
		wantErr error
	}{
		{
			name: "the default reads no record",
			id:   model.DefaultSession,
			want: model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
		},
		{
			name: "a session is named as its record names it",
			id:   demoSession,
			setup: func(sessions *mockSessionRepo) {
				sessions.On("LoadSession", mock.Anything, demoSession).
					Return(model.Session{ID: demoSession, Name: "exp"}, true, nil).Once()
			},
			want: model.SessionRef{ID: demoSession, Name: "exp"},
		},
		{
			name: "a session with no record is gone",
			id:   demoSession,
			setup: func(sessions *mockSessionRepo) {
				sessions.On("LoadSession", mock.Anything, demoSession).Return(model.Session{}, false, nil).Once()
			},
			wantErr: model.ErrSessionNotFound,
		},
		{
			name: "a record that cannot be read",
			id:   demoSession,
			setup: func(sessions *mockSessionRepo) {
				sessions.On("LoadSession", mock.Anything, demoSession).Return(model.Session{}, false, loadErr).Once()
			},
			wantErr: loadErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			sessions := &mockSessionRepo{}
			if tt.setup != nil {
				tt.setup(sessions)
			}

			// Act
			got, err := readSessionRef(context.Background(), sessions, tt.id)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, model.SessionRef{}, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			sessions.AssertExpectations(t)
		})
	}
}
