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
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestReader_Read(t *testing.T) {
	tests := []struct {
		name       string
		status     Status
		age        time.Duration
		wantStatus Status
	}{
		{name: "fresh in-progress", status: StatusInProgress, age: time.Minute, wantStatus: StatusInProgress},
		{name: "stale in-progress", status: StatusInProgress, age: time.Hour, wantStatus: StatusFailed},
		{name: "done", status: StatusDone, age: time.Hour, wantStatus: StatusDone},
		{name: "failed", status: StatusFailed, age: time.Minute, wantStatus: StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: UpdatedAt is always fresh; the file's mtime is the store's clock.
			ctx := context.Background()
			st, root := newStore(t)
			written := Heartbeat{Operation: "fork", Status: tt.status, UpdatedAt: time.Now().UTC().Round(0)}
			require.NoError(t, NewWriter(st).Write(ctx, written))
			modified := time.Now().Add(-tt.age)
			require.NoError(t, os.Chtimes(filepath.Join(root, FileName), modified, modified))

			// Act
			got, err := NewReader(st).Read(ctx)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.True(t, written.UpdatedAt.Equal(got.UpdatedAt), "the rest is returned as written")
		})
	}
}

func TestReader_Read_SetStaleAfter(t *testing.T) {
	// Arrange
	ctx := context.Background()
	st, root := newStore(t)
	require.NoError(t, NewWriter(st).Write(ctx, Heartbeat{Status: StatusInProgress}))
	modified := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(root, FileName), modified, modified))

	// Act
	got, err := NewReader(st).SetStaleAfter(time.Minute).Read(ctx)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, got.Status)
}

func TestReader_Read_Missing(t *testing.T) {
	// Arrange
	st, _ := newStore(t)

	// Act
	_, err := NewReader(st).Read(context.Background())

	// Assert
	require.ErrorIs(t, err, model.ErrHeartbeatNotFound)
}

func TestReader_Read_Unreadable(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "corrupt", body: `{"status":`, wantErr: "decode heartbeat"},
		{name: "an unknown status", body: `{"status":"paused"}`, wantErr: "invalid heartbeat status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			st, root := newStore(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, FileName), []byte(tt.body), 0o644))

			// Act
			_, err := NewReader(st).Read(context.Background())

			// Assert
			require.ErrorContains(t, err, tt.wantErr)
			assert.NotErrorIs(t, err, model.ErrHeartbeatNotFound)
		})
	}
}
