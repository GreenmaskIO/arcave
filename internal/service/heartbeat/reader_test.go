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

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What the reader answers is the *judged* status, not the stored one: a run that
// stopped refreshing its heartbeat reads as failed, while a terminal status is
// whatever it was written as, however old.
func TestReader_Read(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		status       Status
		updatedAt    time.Time
		staleTimeout time.Duration
		want         Status
	}{
		{
			name:         "a done heartbeat reads as done",
			status:       StatusDone,
			updatedAt:    now,
			staleTimeout: time.Minute,
			want:         StatusDone,
		},
		{
			name:         "a failed heartbeat reads as failed",
			status:       StatusFailed,
			updatedAt:    now,
			staleTimeout: time.Minute,
			want:         StatusFailed,
		},
		{
			name:         "an in-progress heartbeat refreshed within the timeout is still in progress",
			status:       StatusInProgress,
			updatedAt:    now.Add(-30 * time.Second),
			staleTimeout: time.Minute,
			want:         StatusInProgress,
		},
		{
			name:         "an in-progress heartbeat older than the timeout reads as failed",
			status:       StatusInProgress,
			updatedAt:    now.Add(-2 * time.Minute),
			staleTimeout: time.Minute,
			want:         StatusFailed,
		},
		{
			name:         "a terminal status is never stale",
			status:       StatusDone,
			updatedAt:    now.Add(-100 * time.Minute),
			staleTimeout: time.Minute,
			want:         StatusDone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)
			putHeartbeat(t, store, tt.status, tt.updatedAt)

			// Act
			got, err := NewReader(store).SetStaleTimeout(tt.staleTimeout).Read(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A reader built without SetStaleTimeout judges staleness by the interval the
// worker writes at, so the default is what an unconfigured caller gets.
func TestReader_Read_DefaultStaleTimeout(t *testing.T) {
	// Arrange
	store := newStore(t)
	putHeartbeat(t, store, StatusInProgress, time.Now().Add(-DefaultWriteInterval-time.Minute))

	// Act
	got, err := NewReader(store).Read(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, got)
}

// A status outside the vocabulary is refused rather than passed through: the
// reader's answer steers a workflow, so an unreadable one must not look like a
// state.
func TestReader_Read_InvalidStatus(t *testing.T) {
	// Arrange
	store := newStore(t)
	putHeartbeat(t, store, "unknown", time.Now())

	// Act
	_, err := NewReader(store).Read(context.Background())

	// Assert
	require.ErrorIs(t, err, errInvalidStatus)
}

// A torn object is a decode failure, not an absent heartbeat.
func TestReader_Read_CorruptObject(t *testing.T) {
	// Arrange
	store := newStore(t)
	putObject(t, store, "{torn")

	// Act
	_, err := NewReader(store).Read(context.Background())

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode heartbeat")
}

// No heartbeat at all surfaces as the store's own sentinel, so a caller can tell
// "nothing ran here" from "the store is broken".
func TestReader_Read_MissingObject(t *testing.T) {
	// Arrange
	store := newStore(t)

	// Act
	_, err := NewReader(store).Read(context.Background())

	// Assert
	require.ErrorIs(t, err, storages.ErrFileNotFound)
	assert.Contains(t, err.Error(), "get object")
}

// A store that fails the lookup itself is reported as-is.
func TestReader_Read_StorageError(t *testing.T) {
	// Arrange
	boom := errors.New("store boom")

	// Act
	_, err := NewReader(failingStore{err: boom}).Read(context.Background())

	// Assert
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "get object")
}
