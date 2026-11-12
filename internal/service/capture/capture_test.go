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

package capture

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
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// newTestContext is a context logging nowhere.
func newTestContext() context.Context {
	return zerolog.Nop().WithContext(context.Background())
}

// The heartbeat is written before the first object and stays in progress until the record;
// done follows it.
func TestService_Capture_HeartbeatFramesTheCapture(t *testing.T) {
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
	commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return savepoint.ID.Validate() == nil && savepoint.Name == "sp"
	})).
		Run(func(args mock.Arguments) {
			atCommit = readCaptureHeartbeat(t, store, args.Get(1).(model.Savepoint).ID).Status
		}).
		Return(nil).Once()
	mock.InOrder(snapshot, spec, commit)
	runner := heartbeat.RunnerInfo{Host: "laptop", PID: 42, ArcaveVersion: "v1"}
	svc := New(Deps{SavepointRepo: repo, Store: store, RunnerInfo: runner})
	plan := Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}

	// Act
	savepoint, err := svc.Capture(ctx, fakeGrantedSessionLock{}, plan, nil, Request{Name: "sp"})

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
func TestService_Capture_HeartbeatLostCommitsNothing(t *testing.T) {
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
		SavepointRepo:       repo,
		Store:               &failingAfterFirstPutStore{Storager: store},
		HeartbeatInterval:   10 * time.Millisecond,
		HeartbeatStaleAfter: 30 * time.Millisecond,
	})
	plan := Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}

	// Act
	savepoint, err := svc.Capture(ctx, fakeGrantedSessionLock{}, plan, nil, Request{Name: "sp"})

	// Assert
	require.ErrorIs(t, err, model.ErrHeartbeatLost)
	assert.Zero(t, savepoint)
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A tree that already holds anything is refused before the first beat: the heartbeat would
// otherwise vouch for another run's remains.
func TestService_Capture_RefusesAnOccupiedTreeBeforeTheHeartbeat(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store := &occupiedStore{Storager: newDirStore(t)}
	backend := &mockBackend{volume: demoVolume()}
	repo := &mockSavepointRepo{}
	svc := New(Deps{SavepointRepo: repo, Store: store})
	plan := Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}

	// Act
	_, err := svc.Capture(ctx, fakeGrantedSessionLock{}, plan, nil, Request{Name: "sp"})

	// Assert
	require.ErrorIs(t, err, errCaptureDirectoryNotEmpty)
	assert.Empty(t, objectNames(t, store.Storager), "no heartbeat is written")
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
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

// A capture commits its record only while the session's lock is still its own.
func TestService_Capture_SessionLock(t *testing.T) {
	tests := []struct {
		name      string
		verifyErr error
		wantBeat  heartbeat.Status
	}{
		{
			name:     "a held lock lets the record land",
			wantBeat: heartbeat.StatusDone,
		},
		{
			name:      "a lost lock commits nothing",
			verifyErr: model.ErrSessionLockLost,
			wantBeat:  heartbeat.StatusFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newDirStore(t)
			backend := &mockBackend{volume: demoVolume()}
			var id model.SavepointID
			snapshot := backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
				Run(func(args mock.Arguments) { id = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
				Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
			repo := &mockSavepointRepo{}
			spec := expectSpecCommit(repo)
			grantedLock := &mockGrantedSessionLock{}
			verify := grantedLock.On("Verify", mock.Anything).Return(tt.verifyErr).Once()
			calls := []*mock.Call{snapshot, spec, verify}
			if tt.verifyErr == nil {
				calls = append(calls, repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
					return savepoint.ID == id && savepoint.Name == "sp"
				})).Return(nil).Once())
			}
			mock.InOrder(calls...)
			svc := New(Deps{
				SavepointRepo: repo,
				Store:         store,
			})
			plan := Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}

			// Act
			savepoint, err := svc.Capture(newTestContext(), grantedLock, plan, nil, Request{Name: "sp"})

			// Assert
			if tt.verifyErr != nil {
				require.ErrorIs(t, err, tt.verifyErr)
				assert.Zero(t, savepoint)
			} else {
				require.NoError(t, err)
				assert.Equal(t, id, savepoint.ID)
			}
			assert.Equal(t, tt.wantBeat, readCaptureHeartbeat(t, store, id).Status)
			backend.AssertExpectations(t)
			repo.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}

// An orphan tree a crashed run left under the id stops the capture: it is the
// only copy of whatever that run captured, so it is reported, never deleted.
func TestService_requireEmptyTree(t *testing.T) {
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
			const id = model.SavepointID("019e843e-1650-7000-8000-000000000000")
			store := newDirStore(t)
			want := make([]string, 0, len(tt.planted))
			for _, name := range tt.planted {
				key := id.String() + "/" + name
				require.NoError(t, store.PutObject(context.Background(), key, strings.NewReader("stale")))
				want = append(want, key)
			}
			svc := New(Deps{Store: store})

			// Act
			err := svc.requireEmptyTree(zerolog.Nop().WithContext(context.Background()), id)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.ElementsMatch(t, want, objectNames(t, store), "nothing is deleted")
		})
	}
}
