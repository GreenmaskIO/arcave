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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every valid status lands at the heartbeat key, stamped with the wall clock.
func TestWriter_Write(t *testing.T) {
	tests := []struct {
		name   string
		status Status
	}{
		{name: "done", status: StatusDone},
		{name: "in progress", status: StatusInProgress},
		{name: "failed", status: StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)

			// Act
			err := NewWriter(store).Write(context.Background(), tt.status)

			// Assert
			require.NoError(t, err)
			got := readHeartbeat(t, store)
			assert.Equal(t, tt.status, got.Status)
			assert.WithinDuration(t, time.Now(), got.UpdatedAt, time.Minute)
		})
	}
}

// The writer and the reader are two halves of one object protocol, so what one
// writes the other must read back.
func TestWriter_Write_ReadBack(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store := newStore(t)

	// Act
	writeErr := NewWriter(store).Write(ctx, StatusInProgress)
	got, readErr := NewReader(store).Read(ctx)

	// Assert
	require.NoError(t, writeErr)
	require.NoError(t, readErr)
	assert.Equal(t, StatusInProgress, got)
}

// The worker rewrites the same key on every tick, so a write replaces what is
// there rather than accumulating.
func TestWriter_Write_Overwrites(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store := newStore(t)
	writer := NewWriter(store)
	require.NoError(t, writer.Write(ctx, StatusInProgress))

	// Act
	err := writer.Write(ctx, StatusDone)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, StatusDone, readHeartbeat(t, store).Status)
}

// A status outside the vocabulary is refused before the store is touched: a
// heartbeat nobody can judge is worse than none.
func TestWriter_Write_InvalidStatus(t *testing.T) {
	// Arrange
	store := newStore(t)

	// Act
	err := NewWriter(store).Write(context.Background(), "unknown")

	// Assert
	require.ErrorIs(t, err, errInvalidStatus)
	assert.False(t, exists(t, store), "an invalid status may not reach the store")
}

// A failing store surfaces as the write's own error.
func TestWriter_Write_StorageError(t *testing.T) {
	// Arrange
	boom := errors.New("store boom")

	// Act
	err := NewWriter(failingStore{err: boom}).Write(context.Background(), StatusDone)

	// Assert
	require.ErrorIs(t, err, boom)
}
