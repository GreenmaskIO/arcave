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
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// The heartbeat is written before the first object and stays in progress until the record;
// done follows it.
func TestService_performSavepoint_HeartbeatFramesTheCapture(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := newDirStore(t)
	backend := &mockBackend{volume: demoVolume()}
	repo := &mockSavepointRepo{}
	var atSnapshot, atCommit heartbeat.Status
	snapshot := backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) {
			atSnapshot = readCaptureHeartbeat(t, store, args.Get(1).(model.BackendSnapshotRequest).SnapshotID).Status
		}).
		Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
	spec := expectSpecCommit(repo)
	commit := repo.On("SaveSavepoint", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			atCommit = readCaptureHeartbeat(t, store, args.Get(1).(model.Savepoint).ID).Status
		}).
		Return(nil).Once()
	mock.InOrder(snapshot, spec, commit)
	runner := heartbeat.RunnerInfo{Host: "laptop", PID: 42, ArcaveVersion: "v1"}
	svc := New(Deps{SavepointRepo: repo, Store: store, RunnerInfo: runner})
	working := workingSet{backends: []model.VolumeSnapshotBackend{backend}, spec: demoSpecBytes}

	// Act
	savepoint, err := svc.performSavepoint(ctx, working, nil, "sp", "", nil)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, heartbeat.StatusInProgress, atSnapshot, "the heartbeat comes before the first object")
	assert.Equal(t, heartbeat.StatusInProgress, atCommit, "the record comes before done")
	beat := readCaptureHeartbeat(t, store, savepoint.ID)
	assert.Equal(t, heartbeat.StatusDone, beat.Status)
	assert.Equal(t, model.HeartbeatOperationSavepoint, beat.Operation)
	assert.Equal(t, runner, beat.RunnerInfo)
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A capture whose heartbeat cannot be kept alive is cancelled and never committed.
func TestService_performSavepoint_HeartbeatLostCommitsNothing(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := newDirStore(t)
	backend := &mockBackend{volume: demoVolume()}
	backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { <-args.Get(0).(context.Context).Done() }).
		Return(model.SnapshotStat{}, context.Canceled).Once()
	// Every beat but the first fails, so the deadline passes on a tick.
	repo := &mockSavepointRepo{}
	svc := New(Deps{
		SavepointRepo: repo, Store: &failingAfterFirstPutStore{Storager: store},
		HeartbeatInterval: 10 * time.Millisecond, HeartbeatStaleAfter: 30 * time.Millisecond,
	})
	working := workingSet{backends: []model.VolumeSnapshotBackend{backend}, spec: demoSpecBytes}

	// Act
	savepoint, err := svc.performSavepoint(ctx, working, nil, "sp", "", nil)

	// Assert
	require.ErrorIs(t, err, model.ErrHeartbeatLost)
	assert.Zero(t, savepoint)
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A tree that already holds anything is refused before the first beat: the heartbeat would
// otherwise vouch for another run's remains.
func TestService_performSavepoint_RefusesAnOccupiedTreeBeforeTheHeartbeat(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := &occupiedStore{Storager: newDirStore(t)}
	backend := &mockBackend{volume: demoVolume()}
	repo := &mockSavepointRepo{}
	svc := New(Deps{SavepointRepo: repo, Store: store})
	working := workingSet{backends: []model.VolumeSnapshotBackend{backend}, spec: demoSpecBytes}

	// Act
	_, err := svc.performSavepoint(ctx, working, nil, "sp", "", nil)

	// Assert
	require.ErrorIs(t, err, errCaptureDirectoryNotEmpty)
	assert.Empty(t, objectNames(t, store.Storager), "no heartbeat is written")
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// readCaptureHeartbeat reads the heartbeat of id's tree.
func readCaptureHeartbeat(t *testing.T, store storages.Storager, id model.SavepointID) heartbeat.Heartbeat {
	t.Helper()
	tree, err := store.SubStorage(id.String(), true)
	require.NoError(t, err)
	beat, err := heartbeat.NewReader(tree).Read(context.Background())
	require.NoError(t, err)
	return beat
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

// occupiedStore lists an interrupted run's remains under any tree: the id comes off the clock.
type occupiedStore struct{ storages.Storager }

func (s *occupiedStore) List(_ context.Context, prefix string) ([]storages.ObjectStat, error) {
	return []storages.ObjectStat{{Name: prefix + "/volume-named-gone/data.tar"}}, nil
}
