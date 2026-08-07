// Copyright 2025 Greenmask
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

package heartbeat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Staleness applies to in-progress alone: a terminal status is the run's own
// last word, and no timeout may overrule it.
func TestHeartbeat_GetStatus(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		since   time.Duration
		beat    Heartbeat
		want    Status
		wantErr error
	}{
		{
			name:  "done is terminal",
			since: time.Minute,
			beat:  Heartbeat{Status: StatusDone, UpdatedAt: now},
			want:  StatusDone,
		},
		{
			name:  "failed is terminal",
			since: time.Minute,
			beat:  Heartbeat{Status: StatusFailed, UpdatedAt: now},
			want:  StatusFailed,
		},
		{
			name:  "done stays done however old",
			since: time.Minute,
			beat:  Heartbeat{Status: StatusDone, UpdatedAt: now.Add(-100 * time.Minute)},
			want:  StatusDone,
		},
		{
			name:  "in progress, refreshed within the timeout",
			since: 3 * time.Minute,
			beat:  Heartbeat{Status: StatusInProgress, UpdatedAt: now.Add(-1 * time.Minute)},
			want:  StatusInProgress,
		},
		{
			name:  "in progress, not refreshed for longer than the timeout",
			since: time.Minute,
			beat:  Heartbeat{Status: StatusInProgress, UpdatedAt: now.Add(-3 * time.Minute)},
			want:  StatusFailed,
		},
		{
			name:    "a status outside the vocabulary is refused",
			since:   time.Minute,
			beat:    Heartbeat{Status: "unknown", UpdatedAt: now},
			wantErr: errInvalidStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			beat := tt.beat

			// Act
			got, err := beat.GetStatus(tt.since)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The vocabulary is closed, and Validate is the gate both the writer and the
// reader stand behind.
func TestStatus_Validate(t *testing.T) {
	tests := []struct {
		name    string
		status  Status
		wantErr bool
	}{
		{name: "done", status: StatusDone},
		{name: "in progress", status: StatusInProgress},
		{name: "failed", status: StatusFailed},
		{name: "an unknown token", status: "unknown", wantErr: true},
		{name: "the empty status", status: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.status.Validate()

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidStatus)
				return
			}
			assert.NoError(t, err)
		})
	}
}
