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

	"github.com/google/uuid"
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

// The record takes its name, description, labels and rollback target from the request, and
// lands after the spec.
func TestService_Capture_RecordsTheRequest(t *testing.T) {
	// Arrange
	req := Request{
		Name:        "sp",
		Description: "before the migration",
		Labels:      map[string]string{"team": "core"},
		TakenBefore: &model.SavepointRef{ID: "019e843e-1650-7000-8000-000000000000", Name: "v1"},
	}
	repo := &mockSavepointRepo{}
	spec := expectSpecCommit(repo)
	commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return savepoint.ID.Validate() == nil
	})).Return(nil).Once()
	mock.InOrder(spec, commit)
	svc := New(Deps{SavepointRepo: repo, Store: newDirStore(t)})

	// Act
	savepoint, err := svc.Capture(newTestContext(), fakeGrantedSessionLock{}, Plan{Spec: demoSpecBytes}, nil, req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, req.Name, savepoint.Name)
	assert.Equal(t, req.Description, savepoint.Description)
	assert.Equal(t, req.Labels, savepoint.Labels)
	assert.Equal(t, req.TakenBefore, savepoint.TakenBeforeRollbackTo)
	assert.Equal(t, &model.SpecRef{Format: model.SpecFormatComposeYAML}, savepoint.Spec)
	repo.AssertExpectations(t)
}

// A volume that fails commits nothing: the savepoint stays invisible rather than list with
// half its volumes.
func TestService_Capture_UncommittedOnFailure(t *testing.T) {
	// Arrange
	backend := &mockBackend{volume: demoVolume()}
	backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{}, errors.New("capture boom")).Once()
	repo := &mockSavepointRepo{}
	svc := New(Deps{SavepointRepo: repo, Store: newDirStore(t)})
	plan := Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}

	// Act
	savepoint, err := svc.Capture(newTestContext(), fakeGrantedSessionLock{}, plan, nil, Request{Name: "sp"})

	// Assert
	require.EqualError(t, err, "snapshot volume-named-pgdata: capture volume: capture boom")
	assert.Zero(t, savepoint)
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// What a half-finished fan-out wrote stays for a later gc, logged with its id: no record names
// it, and a failure path never deletes data.
func TestService_Capture_RetainsWhatAPartialCaptureWrote(t *testing.T) {
	// Arrange
	store := newDirStore(t)
	var id model.SavepointID
	written := &mockBackend{volume: demoVolume()}
	written.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) {
			id = args.Get(1).(model.BackendSnapshotRequest).SnapshotID
			key := id.String() + "/" + demoVolume().ObjectID + "/data.tar"
			require.NoError(t, store.PutObject(context.Background(), key, strings.NewReader("pgdata bytes")))
		}).
		Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
	failed := &mockBackend{volume: model.Volume{ObjectID: "volume-service-app-etc-cfg"}}
	failed.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{}, errors.New("capture boom")).Once()
	repo := &mockSavepointRepo{}
	// One at a time, so the failure comes after the first volume is written.
	svc := New(Deps{SavepointRepo: repo, Store: store, Concurrency: 1})
	plan := Plan{Volumes: []model.VolumeSnapshotBackend{written, failed}, Spec: demoSpecBytes}
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	savepoint, err := svc.Capture(ctx, fakeGrantedSessionLock{}, plan, nil, Request{Name: "sp"})

	// Assert
	require.ErrorContains(t, err, "capture boom")
	assert.Zero(t, savepoint)
	assert.ElementsMatch(t,
		[]string{
			id.String() + "/" + heartbeat.FileName,
			id.String() + "/" + demoVolume().ObjectID + "/data.tar",
		},
		objectNames(t, store),
		"the archive the surviving volume wrote is kept beside its heartbeat")
	assert.Equal(t, heartbeat.StatusFailed, readCaptureHeartbeat(t, store, id).Status)
	assert.Contains(t, logs.String(), `"snapshot_id":"`+id.String()+`","message":"the partial capture is retained for a later gc"`)
	written.AssertExpectations(t)
	failed.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// The id comes off the clock, a UUIDv7, and is the one every backend writes under and the
// record and its tree are committed under.
func TestService_Capture_CreatesTheIDFromTheClock(t *testing.T) {
	// Arrange
	var volumeID, layerID, specID model.SavepointID
	backend := &mockBackend{volume: demoVolume()}
	backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Run(func(args mock.Arguments) { volumeID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
	images := &mockImageBackend{}
	images.On("CaptureWritableLayer", mock.Anything, mock.MatchedBy(func(req model.WritableLayerCaptureRequest) bool {
		return req.Container == demoContainerRef()
	})).
		Run(func(args mock.Arguments) { layerID = args.Get(1).(model.WritableLayerCaptureRequest).SnapshotID }).
		Return(demoWritableLayer(), nil).Once()
	repo := &mockSavepointRepo{}
	repo.On("SaveSpec", mock.Anything, mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil }), demoSpecBytes).
		Run(func(args mock.Arguments) { specID = args.Get(1).(model.SavepointID) }).
		Return(nil).Once()
	repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return savepoint.ID.Validate() == nil
	})).Return(nil).Once()
	store := newDirStore(t)
	svc := New(Deps{SavepointRepo: repo, ImageBackend: images, Store: store})
	plan := Plan{
		Spec:           demoSpecBytes,
		Volumes:        []model.VolumeSnapshotBackend{backend},
		WritableLayers: []model.WritableLayerCaptureRequest{{Container: demoContainerRef()}},
	}

	// Act
	savepoint, err := svc.Capture(newTestContext(), fakeGrantedSessionLock{}, plan, nil, Request{Name: "sp"})

	// Assert
	require.NoError(t, err)
	parsed, err := uuid.Parse(savepoint.ID.String())
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version())
	assert.WithinDuration(t, time.Now(), savepoint.CreatedAt, time.Minute)
	assert.Equal(t, savepoint.ID, volumeID)
	assert.Equal(t, savepoint.ID, layerID)
	assert.Equal(t, savepoint.ID, specID)
	assert.Equal(t, []string{savepoint.ID.String() + "/" + heartbeat.FileName}, objectNames(t, store),
		"the heartbeat lands in the tree the id names")
	backend.AssertExpectations(t)
	images.AssertExpectations(t)
	repo.AssertExpectations(t)
}
