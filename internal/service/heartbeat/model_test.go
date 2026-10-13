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

package heartbeat

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var testAt = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func TestHeartbeat_JSON(t *testing.T) {
	// Arrange
	h := Heartbeat{
		Operation:  model.HeartbeatOperationFork,
		Status:     StatusFailed,
		RunnerInfo: RunnerInfo{Host: "box", PID: 42, ArcaveVersion: "dev"},
		StartedAt:  testAt,
		UpdatedAt:  testAt,
		FinishedAt: testAt,
		Error:      "disk full",
		Data:       json.RawMessage(`{"from":"` + model.DefaultSession.String() + `"}`),
	}

	// Act
	got, err := json.Marshal(h)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, `{"operation":"fork","status":"failed",`+
		`"runner_info":{"host":"box","pid":42,"arcave_version":"dev"},`+
		`"started_at":"2026-10-12T09:00:00Z","updated_at":"2026-10-12T09:00:00Z",`+
		`"finished_at":"2026-10-12T09:00:00Z","error":"disk full",`+
		`"data":{"from":"`+model.DefaultSession.String()+`"}}`, string(got))
}

func TestHeartbeat_JSON_InProgressOmitsTheEnd(t *testing.T) {
	// Act
	got, err := json.Marshal(Heartbeat{Status: StatusInProgress, StartedAt: testAt, UpdatedAt: testAt})

	// Assert
	require.NoError(t, err)
	assert.NotContains(t, string(got), "finished_at")
	assert.NotContains(t, string(got), "error")
	assert.NotContains(t, string(got), "data")
}

func TestHeartbeat_StatusAt(t *testing.T) {
	tests := []struct {
		name    string
		status  Status
		age     time.Duration
		want    Status
		wantErr bool
	}{
		{name: "fresh in-progress", status: StatusInProgress, age: time.Minute, want: StatusInProgress},
		{name: "in-progress just short of stale", status: StatusInProgress, age: 30*time.Minute - time.Second,
			want: StatusInProgress},
		{name: "in-progress at stale", status: StatusInProgress, age: 30 * time.Minute, want: StatusFailed},
		{name: "done never goes stale", status: StatusDone, age: 24 * time.Hour, want: StatusDone},
		{name: "failed stays failed", status: StatusFailed, age: time.Minute, want: StatusFailed},
		{name: "an unknown status", status: "paused", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: UpdatedAt says fresh; only the store's clock counts.
			h := Heartbeat{Status: tt.status, UpdatedAt: testAt}

			// Act
			got, err := h.StatusAt(testAt.Add(-tt.age), testAt, 30*time.Minute)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidStatus)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestData_RoundTrips(t *testing.T) {
	// Arrange
	data, err := EncodeData(model.ForkHeartbeatData{From: model.DefaultSession})
	require.NoError(t, err)
	h := Heartbeat{Operation: model.HeartbeatOperationFork, Data: data}

	// Act
	got, err := DecodeData[model.ForkHeartbeatData](h)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.ForkHeartbeatData{From: model.DefaultSession}, got)
	assert.JSONEq(t, `{"from":"`+model.DefaultSession.String()+`"}`, string(data))
}

func TestDecodeData_Malformed(t *testing.T) {
	// Arrange
	h := Heartbeat{Operation: model.HeartbeatOperationFork, Data: []byte(`{"from":"not-a-uuid"}`)}

	// Act
	_, err := DecodeData[model.ForkHeartbeatData](h)

	// Assert
	require.ErrorContains(t, err, "decode fork heartbeat data")
}
