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

package gc

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/utils"
)

// newMockDaemonCollector is a daemon collector resolving dataKind, named after it.
func newMockDaemonCollector(dataKind model.GarbageDataKind) *mockDaemonCollector {
	collector := &mockDaemonCollector{}
	collector.expectNames(dataKind)
	return collector
}

// newMockComposeProjectCollector is a compose-project collector resolving dataKind, named after it.
func newMockComposeProjectCollector(dataKind model.GarbageDataKind) *mockComposeProjectCollector {
	collector := &mockComposeProjectCollector{}
	collector.expectNames(dataKind)
	return collector
}

func (m *mockCollector) expectNames(dataKind model.GarbageDataKind) {
	m.On("Kind").Return(model.GarbageCollectorKind("mock-" + dataKind)).Maybe()
	m.On("DataKind").Return(dataKind).Maybe()
}

var (
	gcNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	// noTrees matches the savepoint trees of a compose project store holding none.
	noTrees   = mock.MatchedBy(func(savepoints []model.SavepointID) bool { return len(savepoints) == 0 })
	gcSession = mustParseSessionID("019e843e-1650-7000-8000-00000000000a")
	// gcDeadSavepoint is a savepoint tree no record commits, whose capture is dead.
	gcDeadSavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000002")
)

func mustParseSessionID(s string) model.SessionID {
	id, err := model.ParseSessionID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// newDeps is a gc over store's session, stamped at gcNow; the collectors and the savepoint
// readers are the test's.
func newDeps(
	session model.SessionID, store storages.Navigator, sessions *mockSessions, locker *mockLocker, inflight *mockInflight,
) Deps {
	return Deps{
		Store:    store,
		Session:  session,
		Sessions: sessions,
		Locker:   locker,
		Inflight: inflight,
		Now:      func() time.Time { return gcNow },
	}
}

// newGCStore is an arcave project store holding, in session, the compose projects names.
func newGCStore(t *testing.T, session model.SessionID, names ...model.DockerComposeProjectName) storages.Storager {
	t.Helper()
	store := inmemory.New("")
	for _, name := range names {
		key := "sessions/" + session.String() + "/compose/" + name.String() + "/.keep"
		require.NoError(t, store.PutObject(t.Context(), key, strings.NewReader("")))
	}
	return store
}

// unlistableStore refuses every directory listing, as a store whose credentials stop at reading.
type unlistableStore struct{ storages.Storager }

func (s *unlistableStore) SubStorage(subPath string, relative bool) (storages.Storager, error) {
	sub, err := s.Storager.SubStorage(subPath, relative)
	if err != nil {
		return nil, err
	}
	return &unlistableStore{Storager: sub}, nil
}

func (s *unlistableStore) ListDir(context.Context) ([]string, []storages.Storager, error) {
	return nil, nil, errors.New("access denied")
}

// newSavepointsInOrder opens repos in turn, one per compose project the gc reads.
func newSavepointsInOrder(t *testing.T, repos ...*mockSavepointReader) func(storages.Storager) SavepointReader {
	next := 0
	return func(storages.Storager) SavepointReader {
		require.Less(t, next, len(repos), "a compose project read past the repositories given")
		repo := repos[next]
		next++
		return repo
	}
}

func newDirStore(t *testing.T) storages.Storager {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// newHeldLocker is a lock acquired and released once.
func newHeldLocker() *mockLocker {
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	locker.On("Release").Return(nil).Once().NotBefore(acquire)
	return locker
}

// newNoMarker is an inflight marker read once and not found.
func newNoMarker() *mockInflight {
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	return inflight
}

func TestGarbageCollector_GarbageCollect_RefusesToDelete(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	svc := New(newDeps(model.DefaultSession, inmemory.New(""), &mockSessions{}, locker, &mockInflight{}))

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrGarbageCollectNotImplemented)
	locker.AssertNotCalled(t, "Acquire", mock.Anything)
}

func TestGarbageCollector_GarbageCollect_RefusedWhileLocked(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	locker.On("Acquire", mock.Anything).Return(model.ErrProjectLocked).Once()
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	deps := newDeps(model.DefaultSession, inmemory.New(""), &mockSessions{}, locker, &mockInflight{})
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
	svc := New(deps)

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert: a capture may be reusing a stored blob its record does not name yet.
	require.ErrorIs(t, err, model.ErrProjectLocked)
	blobs.AssertNotCalled(t, "Find", mock.Anything, mock.Anything)
	locker.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect(t *testing.T) {
	// Arrange
	ctx := context.Background()
	savepoints := []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000001"}}
	sessions := &mockSessions{}
	sessions.On("LoadSession", mock.Anything, gcSession).Return(model.Session{ID: gcSession}, true, nil).Once()
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).
		Return(savepoints, nil).Once()
	inflight := newNoMarker()

	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	blobs.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool {
		opened := scope.Store != nil && scope.ReadCaptureStatus != nil
		scope.Store, scope.Objects, scope.ReadCaptureStatus = nil, nil, nil
		return opened && assert.ObjectsAreEqual(ComposeProjectScope{
			ComposeProject: "demo", Savepoints: savepoints,
		}, scope)
	})).Return(model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{
		{Action: model.GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/x", Bytes: utils.New(int64(7))},
		{Action: model.GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/y", Bytes: utils.New(int64(3)), Savepoint: gcDeadSavepoint},
	}}}, nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	trees.On("Find", mock.Anything, mock.Anything).Return(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{
		{Action: model.GarbageActionDeleteTree, Target: gcDeadSavepoint.String(), Bytes: utils.New(int64(10)), Savepoint: gcDeadSavepoint},
	}}}, nil).Once()
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	tags.On("Find", mock.Anything, DaemonScope{
		ComposeProjects: []model.DockerComposeProjectName{"demo"}, Now: gcNow,
	}).Return(model.DockerLayerTagStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
	locker := newHeldLocker()
	deps := newDeps(gcSession, newGCStore(t, gcSession, "demo"), sessions, locker, inflight)
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
	deps.DaemonCollectors = []DaemonCollector{tags}
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs, trees}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, gcSession, result.Session)
	require.Len(t, result.Reports, 3)
	assert.Equal(t, model.GarbageDataKindRestoredLayers, result.Reports[0].DataKind)
	assert.Equal(t, model.GarbageDataKindImageLayers, result.Reports[1].DataKind)
	assert.Equal(t, model.DockerComposeProjectName("demo"), result.Reports[1].ComposeProject)
	assert.Equal(t, model.GarbageTotal{Found: 3, Bytes: 20, BytesComplete: true}, result.Total)
	assert.Equal(t, []model.DeadSavepoint{
		{ComposeProject: "demo", Savepoint: gcDeadSavepoint, Found: 2, Bytes: utils.New(int64(13))},
	}, result.Savepoints, "the per-savepoint section gathers its tree and the blobs only it lists")
	assert.Empty(t, result.Warnings)
	locker.AssertExpectations(t)
	sessions.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	inflight.AssertExpectations(t)
	blobs.AssertExpectations(t)
	trees.AssertExpectations(t)
	tags.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect_SessionRemovedBeforeTheLock(t *testing.T) {
	// Arrange
	sessions := &mockSessions{}
	sessions.On("LoadSession", mock.Anything, gcSession).Return(model.Session{}, false, nil).Once()
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	deps := newDeps(gcSession, inmemory.New(""), sessions, newHeldLocker(), &mockInflight{})
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
	svc := New(deps)

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionNotFound)
	blobs.AssertNotCalled(t, "Find", mock.Anything, mock.Anything)
	sessions.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect_FailuresStayInTheirReport(t *testing.T) {
	// Arrange
	ctx := context.Background()
	brokenSavepoints := &mockSavepointReader{}
	brokenSavepoints.On("LoadSavepoints", mock.Anything, noTrees).
		Return([]model.Savepoint(nil), errors.New("corrupt record")).Once()
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).
		Return([]model.Savepoint(nil), nil).Once()

	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	blobs.On("Find", mock.Anything, mock.Anything).Return(nil, errors.New("listing failed")).Once()
	images := newMockComposeProjectCollector(model.GarbageDataKindUpstreamImages)
	images.On("Find", mock.Anything, mock.Anything).
		Return(model.OCIImageStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
	store := newGCStore(t, model.DefaultSession, "broken", "demo")
	deps := newDeps(model.DefaultSession, store, &mockSessions{}, newHeldLocker(), newNoMarker())
	deps.NewSavepoints = newSavepointsInOrder(t, brokenSavepoints, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs, images}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
	require.Len(t, result.Reports, 4)
	// the first compose project's mark could not be read: every compose kind is aborted, none runs
	assert.NotEmpty(t, result.Reports[0].Summary.Aborted)
	assert.NotEmpty(t, result.Reports[1].Summary.Aborted)
	// the second's blob collector failed; the image collector after it still ran
	assert.Equal(t, "listing failed", result.Reports[2].Error)
	assert.Equal(t, model.GarbageDataKindUpstreamImages, result.Reports[3].DataKind)
	assert.Empty(t, result.Reports[3].Error)
	assert.Equal(t, model.GarbageTotal{Aborted: 2, Failed: 1}, result.Total)
	brokenSavepoints.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	blobs.AssertExpectations(t)
	images.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect_WarnsOfAStandingMarker(t *testing.T) {
	// Arrange
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{
		Session: model.DefaultSession, ComposeProject: "demo", Target: "019e843e-1650-7000-8000-000000000001",
	}, true, nil).Once()
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	deps := newDeps(model.DefaultSession, inmemory.New(""), &mockSessions{}, newHeldLocker(), inflight)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "interrupted rollback")
	inflight.AssertExpectations(t)
}

// A session whose compose projects cannot be listed gives the daemon's objects nothing to be
// judged against: the daemon kinds abort with the compose ones.
func TestGarbageCollector_GarbageCollect_UnlistedSessionAbortsTheDaemon(t *testing.T) {
	// Arrange
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	store := &unlistableStore{Storager: inmemory.New("")}
	deps := newDeps(model.DefaultSession, store, &mockSessions{}, newHeldLocker(), newNoMarker())
	deps.DaemonCollectors = []DaemonCollector{tags}
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
	require.Len(t, result.Reports, 2)
	assert.Equal(t, model.GarbageDataKindRestoredLayers, result.Reports[0].DataKind)
	assert.Contains(t, result.Reports[0].Summary.Aborted, "access denied")
	assert.Contains(t, result.Reports[1].Summary.Aborted, "access denied")
	tags.AssertNotCalled(t, "Find", mock.Anything, mock.Anything)
	blobs.AssertNotCalled(t, "Find", mock.Anything, mock.Anything)
}

// An object a kind could not judge makes the gc partial.
func TestGarbageCollector_GarbageCollect_UnjudgedIsPartial(t *testing.T) {
	// Arrange
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).
		Return([]model.Savepoint(nil), nil).Once()
	restic := newMockComposeProjectCollector(model.GarbageDataKindDeduplicatedVolumes)
	restic.On("Find", mock.Anything, mock.Anything).Return(model.ResticRepositoryStat{GarbageFindings: model.GarbageFindings{
		Ops:      []model.GarbageOp{},
		Unjudged: []model.GarbageSkip{{Target: "restic/volume-named-db", Reason: model.GarbageReasonBackendUnreadable}},
	}}, nil).Once()
	store := newGCStore(t, model.DefaultSession, "demo")
	deps := newDeps(model.DefaultSession, store, &mockSessions{}, newHeldLocker(), newNoMarker())
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{restic}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
	assert.Equal(t, model.GarbageTotal{Bytes: 0, BytesComplete: true, Unjudged: 1}, result.Total)
	assert.Empty(t, result.Warnings)
	restic.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
}

// A collector that returns neither a finding nor an error fails its own report, not the gc.
func TestGarbageCollector_GarbageCollect_NoFinding(t *testing.T) {
	// Arrange
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	tags.On("Find", mock.Anything, mock.Anything).Return(nil, nil).Once()
	deps := newDeps(model.DefaultSession, inmemory.New(""), &mockSessions{}, newHeldLocker(), newNoMarker())
	deps.DaemonCollectors = []DaemonCollector{tags}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
	require.Len(t, result.Reports, 1)
	assert.Contains(t, result.Reports[0].Error, "no finding")
	tags.AssertExpectations(t)
}

// Every tree no record commits is sorted by its heartbeat, read off the store: a record beside
// one outranks it, none means dead.
func TestGarbageCollector_GarbageCollect_ReadsCaptureStatuses(t *testing.T) {
	const tree = model.SavepointID("019e843e-1650-7000-8000-000000000003")
	beat := func(operation model.HeartbeatOperation, status heartbeat.Status) *heartbeat.Heartbeat {
		return &heartbeat.Heartbeat{Operation: operation, Status: status}
	}

	tests := []struct {
		name string
		// heartbeat is written into the tree when set; raw instead when not empty.
		heartbeat  *heartbeat.Heartbeat
		raw        string
		committed  bool
		staleAfter time.Duration
		// recordNow is what the record check past the listing answers; nil expects no check.
		recordNow *bool
		recordErr error
		want      ComposeProjectScope
	}{
		{
			name:      "a fresh in-progress heartbeat is a live capture",
			heartbeat: beat(model.HeartbeatOperationSavepoint, heartbeat.StatusInProgress),
			want:      ComposeProjectScope{InProgress: []model.SavepointID{tree}},
		},
		{
			name:       "a stale in-progress heartbeat is dead",
			heartbeat:  beat(model.HeartbeatOperationSavepoint, heartbeat.StatusInProgress),
			staleAfter: time.Nanosecond,
			recordNow:  utils.New(false),
			want:       ComposeProjectScope{Dead: []model.SavepointID{tree}},
		},
		{
			name:      "a failed heartbeat is dead",
			heartbeat: beat(model.HeartbeatOperationSavepoint, heartbeat.StatusFailed),
			recordNow: utils.New(false),
			want:      ComposeProjectScope{Dead: []model.SavepointID{tree}},
		},
		{
			name:      "a done heartbeat with no record is a removal's remains",
			heartbeat: beat(model.HeartbeatOperationSavepoint, heartbeat.StatusDone),
			recordNow: utils.New(false),
			want:      ComposeProjectScope{Dead: []model.SavepointID{tree}},
		},
		{
			name:      "a done heartbeat whose record committed since the listing is a live capture",
			heartbeat: beat(model.HeartbeatOperationSavepoint, heartbeat.StatusDone),
			recordNow: utils.New(true),
			want:      ComposeProjectScope{InProgress: []model.SavepointID{tree}},
		},
		{
			name:      "a record that cannot be checked is unknown",
			heartbeat: beat(model.HeartbeatOperationSavepoint, heartbeat.StatusDone),
			recordNow: utils.New(false),
			recordErr: errors.New("stat savepoint record: timeout"),
			want: ComposeProjectScope{Unknown: []UnknownCapture{
				{ID: tree, Error: "stat savepoint record: timeout"},
			}},
		},
		{
			name: "no heartbeat is dead: a capture writes it before its first object",
			want: ComposeProjectScope{Dead: []model.SavepointID{tree}},
		},
		{
			name: "an unreadable heartbeat is unknown",
			raw:  "{torn",
			want: ComposeProjectScope{Unknown: []UnknownCapture{{ID: tree, Error: "decode heartbeat: invalid character 't' looking for beginning of object key string"}}},
		},
		{
			name:      "another operation's heartbeat is unknown",
			heartbeat: beat(model.HeartbeatOperationFork, heartbeat.StatusInProgress),
			want: ComposeProjectScope{Unknown: []UnknownCapture{
				{ID: tree, Error: `heartbeat of operation "fork", not a capture's`},
			}},
		},
		{
			name:      "a record beside an in-progress heartbeat is committed",
			heartbeat: beat(model.HeartbeatOperationSavepoint, heartbeat.StatusInProgress),
			committed: true,
			want:      ComposeProjectScope{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := context.Background()
			store := newDirStore(t)
			composeProjectStore, err := store.SubStorage("sessions/"+model.DefaultSession.String()+"/compose/demo", true)
			require.NoError(t, err)
			require.NoError(t, composeProjectStore.PutObject(ctx, tree.String()+"/volume-named-db/data.tar", strings.NewReader("x")))
			treeStore, err := composeProjectStore.SubStorage(tree.String(), true)
			require.NoError(t, err)
			switch {
			case tt.raw != "":
				require.NoError(t, treeStore.PutObject(ctx, heartbeat.FileName, strings.NewReader(tt.raw)))
			case tt.heartbeat != nil:
				require.NoError(t, heartbeat.NewWriter(treeStore).Write(ctx, *tt.heartbeat))
			}
			var savepoints []model.Savepoint
			if tt.committed {
				savepoints = []model.Savepoint{{ID: tree}}
			}
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, []model.SavepointID{tree}).Return(savepoints, nil).Once()
			if tt.recordNow != nil {
				demoSavepoints.On("IsSavepointCommitted", mock.Anything, tree).Return(*tt.recordNow, tt.recordErr).Once()
			}
			var got ComposeProjectScope
			trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
			trees.On("Find", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) { got = args.Get(1).(ComposeProjectScope) }).
				Return(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
			deps := newDeps(model.DefaultSession, store, &mockSessions{}, newHeldLocker(), newNoMarker())
			deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
			deps.Concurrency = 2
			deps.HeartbeatStaleAfter = tt.staleAfter
			svc := New(deps)

			// Act
			_, err = svc.GarbageCollect(ctx, model.GarbageCollectRequest{DryRun: true})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want.InProgress, got.InProgress)
			assert.Equal(t, tt.want.Dead, got.Dead)
			assert.Equal(t, tt.want.Unknown, got.Unknown)
			demoSavepoints.AssertExpectations(t)
			trees.AssertExpectations(t)
		})
	}
}

// A request refused for what it asks stops before the lock.
func TestGarbageCollector_GarbageCollect_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  model.GarbageCollectRequest
	}{
		{
			name: "a negative restored layer grace",
			req:  model.GarbageCollectRequest{DryRun: true, RestoredLayerGrace: utils.New(-time.Hour)},
		},
		{
			name: "an unknown data kind",
			req:  model.GarbageCollectRequest{DryRun: true, DataKinds: []model.GarbageDataKind{"restic"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
			deps := newDeps(model.DefaultSession, inmemory.New(""), &mockSessions{}, locker, &mockInflight{})
			deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
			svc := New(deps)

			// Act
			_, err := svc.GarbageCollect(context.Background(), tt.req)

			// Assert
			require.ErrorIs(t, err, model.ErrInvalidGarbageCollectRequest)
			locker.AssertNotCalled(t, "Acquire", mock.Anything)
		})
	}
}

// The request's data kinds pick the collectors, and its grace replaces the service's; left out,
// every collector runs with the service's grace. No compose-project collector, no scope read.
func TestGarbageCollector_GarbageCollect_SelectsDataKindsAndGrace(t *testing.T) {
	tests := []struct {
		name      string
		req       model.GarbageCollectRequest
		wantKinds []model.GarbageDataKind
		wantGrace time.Duration
		// readsScopes is whether the compose projects' scopes are read.
		readsScopes bool
	}{
		{
			name: "every data kind by default",
			req:  model.GarbageCollectRequest{DryRun: true},
			wantKinds: []model.GarbageDataKind{
				model.GarbageDataKindRestoredLayers, model.GarbageDataKindSavepointData, model.GarbageDataKindUpstreamImages,
				model.GarbageDataKindImageLayers, model.GarbageDataKindDeduplicatedVolumes,
			},
			wantGrace:   time.Hour,
			readsScopes: true,
		},
		{
			name: "restored layers alone, with a grace of zero",
			req: model.GarbageCollectRequest{
				DryRun:             true,
				RestoredLayerGrace: utils.New(time.Duration(0)),
				DataKinds:          []model.GarbageDataKind{model.GarbageDataKindRestoredLayers},
			},
			wantKinds: []model.GarbageDataKind{model.GarbageDataKindRestoredLayers},
		},
		{
			name: "two data kinds, in the collectors' order",
			req: model.GarbageCollectRequest{
				DryRun: true,
				DataKinds: []model.GarbageDataKind{
					model.GarbageDataKindDeduplicatedVolumes, model.GarbageDataKindSavepointData,
				},
			},
			wantKinds: []model.GarbageDataKind{
				model.GarbageDataKindSavepointData, model.GarbageDataKindDeduplicatedVolumes,
			},
			readsScopes: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			demoSavepoints := &mockSavepointReader{}
			var readers []*mockSavepointReader
			if tt.readsScopes {
				demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
				readers = append(readers, demoSavepoints)
			}
			grace := time.Duration(-1)
			tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
			tags.On("Find", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) { grace = args.Get(1).(DaemonScope).RestoredLayerGrace }).
				Return(model.DockerLayerTagStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Maybe()
			deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, newHeldLocker(), newNoMarker())
			deps.NewSavepoints = newSavepointsInOrder(t, readers...)
			deps.DaemonCollectors = []DaemonCollector{tags}
			deps.ComposeProjectCollectors = []ComposeProjectCollector{
				newEmptyCollector(model.GarbageDataKindSavepointData, model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}),
				newEmptyCollector(model.GarbageDataKindUpstreamImages, model.OCIImageStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}),
				newEmptyCollector(model.GarbageDataKindImageLayers, model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}),
				newEmptyCollector(model.GarbageDataKindDeduplicatedVolumes, model.ResticRepositoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}),
			}
			deps.RestoredLayerGrace = time.Hour
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(context.Background(), tt.req)

			// Assert
			require.NoError(t, err)
			var kinds []model.GarbageDataKind
			for _, report := range result.Reports {
				kinds = append(kinds, report.DataKind)
			}
			assert.Equal(t, tt.wantKinds, kinds, "the daemon collectors run first, then the compose-project ones in their order")
			if slices.Contains(tt.wantKinds, model.GarbageDataKindRestoredLayers) {
				assert.Equal(t, tt.wantGrace, grace)
			}
			demoSavepoints.AssertExpectations(t)
		})
	}
}

// newEmptyCollector is a compose-project collector of kind that finds nothing, as stat.
func newEmptyCollector(kind model.GarbageDataKind, stat model.GarbageStat) *mockComposeProjectCollector {
	collector := newMockComposeProjectCollector(kind)
	collector.On("Find", mock.Anything, mock.Anything).Return(stat, nil).Maybe()
	return collector
}

// A collector that cannot reach what it reads is aborted, with a warning, at either level and
// whether or not the request named its data kind: a report that looked at nothing would read as clean.
func TestGarbageCollector_GarbageCollect_ProbeFails(t *testing.T) {
	tests := []struct {
		name      string
		dataKinds []model.GarbageDataKind
	}{
		{
			name: "no data kind named",
		},
		{
			name: "the data kinds named",
			dataKinds: []model.GarbageDataKind{
				model.GarbageDataKindRestoredLayers, model.GarbageDataKindImageLayers, model.GarbageDataKindDeduplicatedVolumes,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).
				Return([]model.Savepoint(nil), nil).Once()
			tags := &mockProbeCollector{}
			tags.On("Kind").Return(model.GarbageCollectorKindDockerLayerTags).Maybe()
			tags.On("DataKind").Return(model.GarbageDataKindRestoredLayers).Maybe()
			tags.On("Probe", mock.Anything).Return(errors.New("connection refused")).Once()
			restic := &mockComposeProjectProbeCollector{}
			restic.On("Kind").Return(model.GarbageCollectorKindResticRepositories).Maybe()
			restic.On("DataKind").Return(model.GarbageDataKindDeduplicatedVolumes).Maybe()
			restic.On("Probe", mock.Anything).Return(errors.New("connection refused")).Once()
			blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
			blobs.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool { return scope.ComposeProject == "demo" })).
				Return(model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
			deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, newHeldLocker(), newNoMarker())
			deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
			deps.DaemonCollectors = []DaemonCollector{tags}
			deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs, restic}
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{
				DryRun:    true,
				DataKinds: tt.dataKinds,
			})

			// Assert
			require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
			aborted := map[model.GarbageDataKind]string{}
			var kinds []model.GarbageDataKind
			for _, report := range result.Reports {
				kinds = append(kinds, report.DataKind)
				aborted[report.DataKind] = report.Summary.Aborted
			}
			assert.Equal(t, []model.GarbageDataKind{
				model.GarbageDataKindRestoredLayers, model.GarbageDataKindImageLayers, model.GarbageDataKindDeduplicatedVolumes,
			}, kinds)
			assert.Equal(t, map[model.GarbageDataKind]string{
				model.GarbageDataKindRestoredLayers:      "connection refused",
				model.GarbageDataKindImageLayers:         "",
				model.GarbageDataKindDeduplicatedVolumes: "connection refused",
			}, aborted)
			assert.Equal(t, 2, result.Total.Aborted)
			assert.Equal(t, []string{
				"restored-layers is not judged: connection refused",
				"deduplicated-volumes is not judged: connection refused",
			}, result.Warnings)
			tags.AssertExpectations(t)
			restic.AssertExpectations(t)
			blobs.AssertExpectations(t)
			demoSavepoints.AssertExpectations(t)
		})
	}
}

// The scope lists every object of the compose project store, by its key from the store's root,
// except under an unlisted namespace.
func TestGarbageCollector_GarbageCollect_ListsAllButTheUnlistedNamespaces(t *testing.T) {
	// Arrange
	ctx := context.Background()
	const tree = model.SavepointID("019e843e-1650-7000-8000-000000000003")
	store := newDirStore(t)
	composeProjectStore, err := store.SubStorage("sessions/"+model.DefaultSession.String()+"/compose/demo", true)
	require.NoError(t, err)
	for _, key := range []string{
		"root.keep",
		tree.String() + "/volume-named-db/data.tar",
		"shared/oci/blobs/sha256/" + strings.Repeat("a", 64),
		"restic/volume-named-db/config",
		"restic/volume-named-db/data/00/" + strings.Repeat("0", 64),
	} {
		require.NoError(t, composeProjectStore.PutObject(ctx, key, strings.NewReader("x")))
	}
	wantNames := []string{
		tree.String() + "/volume-named-db/data.tar",
		"root.keep",
		"shared/oci/blobs/sha256/" + strings.Repeat("a", 64),
	}
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, []model.SavepointID{tree}).Return([]model.Savepoint(nil), nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	trees.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool {
		var names []string
		for _, object := range scope.Objects {
			if object.Size != 1 || object.LastModified.IsZero() {
				return false
			}
			names = append(names, object.Name)
		}
		return slices.Equal(wantNames, names)
	})).Return(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
	deps := newDeps(model.DefaultSession, store, &mockSessions{}, newHeldLocker(), newNoMarker())
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
	deps.UnlistedNamespaces = []string{"restic"}
	svc := New(deps)

	// Act
	_, err = svc.GarbageCollect(ctx, model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	demoSavepoints.AssertExpectations(t)
	trees.AssertExpectations(t)
}

// A scope reads, past its listing, the status of a savepoint whose tree appeared since, by the
// rule the listing's trees were sorted by.
func TestGarbageCollector_GarbageCollect_ReadsCaptureStatusPastTheListing(t *testing.T) {
	const tree = model.SavepointID("019e843e-1650-7000-8000-000000000009")

	tests := []struct {
		name string
		// status is the heartbeat written once the scope is read; empty writes none.
		status heartbeat.Status
		// recordNow is what the record check answers; nil expects no check.
		recordNow *bool
		recordErr error
		wantAlive bool
		wantErr   string
	}{
		{
			name:      "a capture started since is alive",
			status:    heartbeat.StatusInProgress,
			wantAlive: true,
		},
		{
			name:      "a capture committed since is alive",
			status:    heartbeat.StatusDone,
			recordNow: utils.New(true),
			wantAlive: true,
		},
		{
			name:      "a done capture with no record is dead",
			status:    heartbeat.StatusDone,
			recordNow: utils.New(false),
		},
		{
			name:      "a record that cannot be checked is an error",
			status:    heartbeat.StatusDone,
			recordNow: utils.New(false),
			recordErr: errors.New("stat savepoint record: timeout"),
			wantErr:   "stat savepoint record: timeout",
		},
		{
			name: "no tree at all is dead",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := context.Background()
			store := newDirStore(t)
			composeProjectStore, err := store.SubStorage("sessions/"+model.DefaultSession.String()+"/compose/demo", true)
			require.NoError(t, err)
			require.NoError(t, composeProjectStore.PutObject(ctx, ".keep", strings.NewReader("")))
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
			if tt.recordNow != nil {
				demoSavepoints.On("IsSavepointCommitted", mock.Anything, tree).Return(*tt.recordNow, tt.recordErr).Once()
			}
			var (
				alive   bool
				readErr error
			)
			trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
			trees.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool { return scope.ComposeProject == "demo" })).
				Run(func(args mock.Arguments) {
					if tt.status != "" {
						treeStore, err := composeProjectStore.SubStorage(tree.String(), true)
						require.NoError(t, err)
						require.NoError(t, heartbeat.NewWriter(treeStore).Write(ctx, heartbeat.Heartbeat{
							Operation: model.HeartbeatOperationSavepoint,
							Status:    tt.status,
						}))
					}
					alive, readErr = args.Get(1).(ComposeProjectScope).ReadCaptureStatus(ctx, tree)
				}).
				Return(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
			deps := newDeps(model.DefaultSession, store, &mockSessions{}, newHeldLocker(), newNoMarker())
			deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
			svc := New(deps)

			// Act
			_, err = svc.GarbageCollect(ctx, model.GarbageCollectRequest{DryRun: true})

			// Assert
			require.NoError(t, err)
			if tt.wantErr != "" {
				assert.EqualError(t, readErr, tt.wantErr)
			} else {
				assert.NoError(t, readErr)
			}
			assert.Equal(t, tt.wantAlive, alive)
			demoSavepoints.AssertExpectations(t)
			trees.AssertExpectations(t)
		})
	}
}

// Every report is logged with what it found, and the total after them.
func TestLogGarbageTotal(t *testing.T) {
	// Arrange
	logs := &strings.Builder{}
	ctx := zerolog.New(logs).WithContext(context.Background())
	result := model.GarbageCollectResult{
		Reports: []model.GarbageReport{
			{
				DataKind:       model.GarbageDataKindDeduplicatedVolumes,
				ComposeProject: "demo",
				Summary:        model.GarbageSummary{Found: 2},
			},
			{
				DataKind:       model.GarbageDataKindSavepointData,
				ComposeProject: "demo",
				Summary:        model.GarbageSummary{Found: 1, Bytes: utils.New(int64(10))},
			},
		},
		Total: model.GarbageTotal{Found: 3, Bytes: 10},
	}

	// Act
	logGarbageTotal(ctx, result)

	// Assert
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[0], `"data_kind":"deduplicated-volumes","compose_project":"demo","found":2,"bytes_complete":false`)
	assert.NotContains(t, lines[0], `"bytes":`)
	assert.Contains(t, lines[1], `"found":1,"bytes_complete":true,"bytes":10`)
	assert.Contains(t, lines[2], `"found":3,"bytes":10,"bytes_complete":false,"message":"garbage found in total"`)
}
