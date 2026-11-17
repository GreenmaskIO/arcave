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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// demoCaptureID is the id the heartbeat tests capture under.
const demoCaptureID model.SavepointID = "019e8488-3ac8-7000-8000-000000000000"

// The heartbeat is written before the hold and stays in progress through the capture and the
// record; done follows the record.
func TestService_captureHeld_HeartbeatFramesTheCapture(t *testing.T) {
	// Arrange
	var (
		store                       = newDirStore(t)
		runner                      = heartbeat.RunnerInfo{Host: "laptop", PID: 42, ArcaveVersion: "v1"}
		atHold, atCapture, atCommit heartbeat.Status
		req                         = provider.CaptureRequest{ID: demoCaptureID, Name: "sp"}
		committed                   = model.Savepoint{ID: demoCaptureID, Name: "sp"}
	)
	readStatus := func(into *heartbeat.Status) func(mock.Arguments) {
		return func(mock.Arguments) { *into = readSavepointHeartbeat(t, store, demoCaptureID).Status }
	}
	stages := newMockStages(false)
	grantedLock := &mockGrantedSessionLock{}
	mock.InOrder(
		stages.expectHold(nil).Run(readStatus(&atHold)),
		stages.expectCaptureOf(req, nil).Run(readStatus(&atCapture)),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		stages.expectCommit(committed, nil).Run(readStatus(&atCommit)),
		stages.expectRelease(time.Minute, nil),
	)
	svc := New(Deps{Store: store, RunnerInfo: runner})

	// Act
	savepoint, err := svc.captureHeld(zerolog.Nop().WithContext(context.Background()), grantedLock, &stages.plan,
		req, time.Minute)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	assert.Equal(t, heartbeat.StatusInProgress, atHold, "the heartbeat comes before the hold")
	assert.Equal(t, heartbeat.StatusInProgress, atCapture, "the capture runs inside the heartbeat")
	assert.Equal(t, heartbeat.StatusInProgress, atCommit, "the record comes before done")
	beat := readSavepointHeartbeat(t, store, demoCaptureID)
	assert.Equal(t, heartbeat.StatusDone, beat.Status)
	assert.Equal(t, model.HeartbeatOperationSavepoint, beat.Operation)
	assert.Equal(t, runner, beat.RunnerInfo)
	stages.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// A capture whose heartbeat cannot be kept alive is cancelled with the loss as its cause, and
// never committed; the services are released all the same.
func TestService_captureHeld_HeartbeatLostCommitsNothing(t *testing.T) {
	// Arrange
	req := provider.CaptureRequest{ID: demoCaptureID, Name: "sp"}
	stages := newMockStages(false)
	grantedLock := &mockGrantedSessionLock{}
	mock.InOrder(
		stages.expectHold(nil),
		stages.expectCaptureOf(req, context.Canceled).
			Run(func(args mock.Arguments) { <-args.Get(0).(context.Context).Done() }),
		stages.expectRelease(time.Minute, nil),
	)
	// Every beat but the first fails, so the deadline passes on a tick.
	svc := New(Deps{
		Store:               &failingAfterFirstPutStore{Storager: newDirStore(t)},
		HeartbeatInterval:   10 * time.Millisecond,
		HeartbeatStaleAfter: 30 * time.Millisecond,
	})

	// Act
	savepoint, err := svc.captureHeld(zerolog.Nop().WithContext(context.Background()), grantedLock, &stages.plan,
		req, time.Minute)

	// Assert
	require.ErrorIs(t, err, model.ErrHeartbeatLost)
	assert.Zero(t, savepoint)
	stages.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// A tree that already holds anything is refused before the first beat and before anything is
// held: the heartbeat would otherwise vouch for another run's remains.
func TestService_captureHeld_RefusesAnOccupiedTreeBeforeTheHeartbeat(t *testing.T) {
	// Arrange
	store := newDirStore(t)
	planted := demoCaptureID.String() + "/volume-named-gone/data.tar"
	require.NoError(t, store.PutObject(context.Background(), planted, strings.NewReader("stale")))
	stages := newMockStages(false)
	grantedLock := &mockGrantedSessionLock{}
	svc := New(Deps{Store: store})

	// Act
	savepoint, err := svc.captureHeld(zerolog.Nop().WithContext(context.Background()), grantedLock, &stages.plan,
		provider.CaptureRequest{ID: demoCaptureID, Name: "sp"}, time.Minute)

	// Assert
	require.ErrorIs(t, err, errCaptureDirectoryNotEmpty)
	assert.Zero(t, savepoint)
	objects, err := store.List(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, objects, 1, "no heartbeat is written and nothing is deleted")
	assert.Equal(t, planted, objects[0].Name)
	stages.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// An orphan tree a crashed run left under the id stops the capture: it is the only copy of
// whatever that run captured, so it is reported, never deleted.
func TestRequireEmptyTree(t *testing.T) {
	tests := []struct {
		name    string
		planted []string
		wantErr error
	}{
		{name: "an empty directory passes"},
		{name: "an orphan is refused", planted: []string{"volume-named-gone/data.tar"}, wantErr: errCaptureDirectoryNotEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			tree := newDirStore(t)
			for _, name := range tt.planted {
				require.NoError(t, tree.PutObject(context.Background(), name, strings.NewReader("stale")))
			}

			// Act
			err := requireEmptyTree(context.Background(), tree, demoCaptureID)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			objects, err := tree.List(context.Background(), "")
			require.NoError(t, err)
			assert.Len(t, objects, len(tt.planted), "nothing is deleted")
		})
	}
}

// failingAfterFirstPutStore lets the first heartbeat land and fails every later write.
type failingAfterFirstPutStore struct {
	storages.Storager
	puts int
}

func (s *failingAfterFirstPutStore) SubStorage(subPath string, relative bool) (storages.Storager, error) {
	sub, err := s.Storager.SubStorage(subPath, relative)
	if err != nil {
		return nil, err
	}
	return &failingAfterFirstPutStore{Storager: sub}, nil
}

func (s *failingAfterFirstPutStore) PutObject(ctx context.Context, filePath string, body io.Reader) error {
	s.puts++
	if s.puts > 1 {
		return errors.New("store unreachable")
	}
	return s.Storager.PutObject(ctx, filePath, body)
}
