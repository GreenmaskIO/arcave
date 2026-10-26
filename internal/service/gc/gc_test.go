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
	"fmt"
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
		Store:         store,
		Session:       session,
		Sessions:      sessions,
		Locker:        locker,
		SessionLocker: fakeSessionLocker{},
		Inflight:      inflight,
		Now:           func() time.Time { return gcNow },
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

func TestGarbageCollector_GarbageCollect_HoldsTheSessionLock(t *testing.T) {
	// Arrange: the session is checked, and the records read, under the session's lock: a tree no
	// record commits is dead only while no capture can run.
	locker := &mockLocker{}
	sessionLocker := &mockSessionLocker{}
	sessions := &mockSessions{}
	grantedLock := &mockGrantedSessionLock{}
	demoSavepoints := &mockSavepointReader{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationGarbageCollect).Return(grantedLock, nil).Once()
	under := sessions.On("LoadSession", mock.Anything, gcSession).Return(model.Session{ID: gcSession}, true, nil).Once()
	records := demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint{}, nil).Once()
	free := grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, lock, under, records, free, release)
	trees := newEmptyCollector(model.GarbageDataKindSavepointData, model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}})
	deps := newDeps(gcSession, newGCStore(t, gcSession, "demo"), sessions, locker, newNoMarker())
	deps.SessionLocker = sessionLocker
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
	svc := New(deps)

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	locker.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	sessions.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	trees.AssertExpectations(t)
}

// A run that loses the session's lock while finding returns the loss, not what it judged past it,
// and a run that deletes deletes nothing: no Verify, no Erase.
func TestGarbageCollector_GarbageCollect_LockLostMidway(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry run %t", dryRun), func(t *testing.T) {
			// Arrange: the mock locker hands the caller's context back as the lock's.
			ctx, loseLock := context.WithCancelCause(context.Background())
			defer loseLock(nil)
			sessionLocker := &mockSessionLocker{}
			grantedLock := &mockGrantedSessionLock{}
			sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationGarbageCollect).Return(grantedLock, nil).Once()
			grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
			sessions := &mockSessions{}
			sessions.On("LoadSession", mock.Anything, gcSession).Return(model.Session{ID: gcSession}, true, nil).Once()
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint{}, nil).Once()
			trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
			trees.On("Find", mock.Anything, inComposeProject("demo")).
				Run(func(mock.Arguments) { loseLock(model.ErrSessionLockLost) }).
				Return(deadTreeStat, nil).Once()
			treeEraser := newMockEraser(model.GarbageActionDeleteTree)
			deps := newDeps(gcSession, newGCStore(t, gcSession, "demo"), sessions, newHeldLocker(), newNoMarker())
			deps.SessionLocker = sessionLocker
			deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
			deps.Erasers = []GarbageEraser{treeEraser}
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{DryRun: dryRun})

			// Assert
			require.ErrorIs(t, err, model.ErrSessionLockLost)
			assert.NotErrorAs(t, err, new(*model.GarbageCollectInterruptedError))
			assert.Zero(t, result)
			sessionLocker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			sessions.AssertExpectations(t)
			demoSavepoints.AssertExpectations(t)
			trees.AssertExpectations(t)
			treeEraser.AssertExpectations(t)
		})
	}
}

func TestGarbageCollector_GarbageCollect_RefusedWhileSessionLocked(t *testing.T) {
	// Arrange
	sessionLocker := &mockSessionLocker{}
	sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationGarbageCollect).
		Return(nil, model.ErrSessionLocked).Once()
	locker := newHeldLocker()
	sessions := &mockSessions{}
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	deps := newDeps(gcSession, newGCStore(t, gcSession, "demo"), sessions, locker, &mockInflight{})
	deps.SessionLocker = sessionLocker
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
	svc := New(deps)

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert: another host's operation may be writing what the gc would judge.
	require.ErrorIs(t, err, model.ErrSessionLocked)
	blobs.AssertExpectations(t)
	locker.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	sessions.AssertExpectations(t)
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
		opened := scope.Store != nil
		scope.Store, scope.Objects = nil, nil
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

// Every tree no record commits is dead whatever its heartbeat says.
func TestGarbageCollector_GarbageCollect_UncommittedTreesAreDead(t *testing.T) {
	const tree = model.SavepointID("019e843e-1650-7000-8000-000000000003")

	tests := []struct {
		name string
		// raw is written into the tree as its heartbeat when not empty.
		raw       string
		committed bool
		wantDead  []model.SavepointID
	}{
		{
			name:     "a tree without a heartbeat is dead",
			wantDead: []model.SavepointID{tree},
		},
		{
			name:     "a fresh in-progress heartbeat is dead",
			raw:      `{"operation":"savepoint","status":"in-progress"}`,
			wantDead: []model.SavepointID{tree},
		},
		{
			name:     "an unreadable heartbeat is dead",
			raw:      "{torn",
			wantDead: []model.SavepointID{tree},
		},
		{
			name:      "a record commits its tree",
			raw:       `{"operation":"savepoint","status":"in-progress"}`,
			committed: true,
			wantDead:  []model.SavepointID{},
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
			if tt.raw != "" {
				require.NoError(t, composeProjectStore.PutObject(ctx, tree.String()+"/"+heartbeat.FileName, strings.NewReader(tt.raw)))
			}
			var savepoints []model.Savepoint
			if tt.committed {
				savepoints = []model.Savepoint{{ID: tree}}
			}
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, []model.SavepointID{tree}).Return(savepoints, nil).Once()
			trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
			trees.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool {
				return scope.ComposeProject == "demo" && assert.ObjectsAreEqual(tt.wantDead, scope.Dead)
			})).Return(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
			deps := newDeps(model.DefaultSession, store, &mockSessions{}, newHeldLocker(), newNoMarker())
			deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
			svc := New(deps)

			// Act
			_, err = svc.GarbageCollect(ctx, model.GarbageCollectRequest{DryRun: true})

			// Assert
			require.NoError(t, err)
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

// Every report is logged with what it found, and their sum after them.
func TestLogGarbageFound(t *testing.T) {
	// Arrange
	logs := &strings.Builder{}
	ctx := zerolog.New(logs).WithContext(context.Background())
	reports := []model.GarbageReport{
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
	}

	// Act
	logGarbageFound(ctx, reports)

	// Assert
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[0], `"data_kind":"deduplicated-volumes","compose_project":"demo","found":2,"bytes_complete":false`)
	assert.NotContains(t, lines[0], `"bytes":`)
	assert.Contains(t, lines[1], `"found":1,"bytes_complete":true,"bytes":10`)
	assert.Contains(t, lines[2], `"found":3,"bytes":10,"bytes_complete":false,"message":"garbage found in the scope"`)
}

// newMockEraser is an eraser carrying out action.
func newMockEraser(action model.GarbageAction) *mockEraser {
	eraser := &mockEraser{}
	eraser.On("Action").Return(action).Maybe()
	return eraser
}

// newDeletedOps is a deletion of every op of ops.
func newDeletedOps(ops ...model.GarbageOp) model.GarbageDeletion {
	var deleted []model.GarbageDeleted
	for _, op := range ops {
		deleted = append(deleted, model.GarbageDeleted{Target: op.Target, Bytes: op.Bytes})
	}
	return model.GarbageDeletion{Deleted: deleted}
}

// inComposeProject matches the scope of compose project name.
func inComposeProject(name model.DockerComposeProjectName) any {
	return mock.MatchedBy(func(scope ComposeProjectScope) bool { return scope.ComposeProject == name })
}

// storeOf matches the store of compose project name.
func storeOf(name model.DockerComposeProjectName) any {
	return mock.MatchedBy(func(store storages.Navigator) bool { return store.Dirname() == string(name) })
}

// newGrantingSessionLocker is a session locker granting grantedLock once.
func newGrantingSessionLocker(grantedLock *mockGrantedSessionLock) *mockSessionLocker {
	sessionLocker := &mockSessionLocker{}
	sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationGarbageCollect).Return(grantedLock, nil).Once()
	return sessionLocker
}

var (
	deadTreeOp = model.GarbageOp{
		Action: model.GarbageActionDeleteTree, Target: gcDeadSavepoint.String(), Bytes: utils.New(int64(10)),
		Savepoint: gcDeadSavepoint,
	}
	deadImageOp = model.GarbageOp{
		Action: model.GarbageActionDeleteImage, Target: "shared/oci/images/sha256/" + strings.Repeat("2", 64),
		Bytes: utils.New(int64(4)),
	}
	deadBlobOp = model.GarbageOp{
		Action: model.GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/" + strings.Repeat("b", 64),
		Bytes: utils.New(int64(3)),
	}
	deadSnapshotOp = model.GarbageOp{Action: model.GarbageActionForget, Target: "restic/volume-named-db:abc"}
	deadTreeStat   = model.GarbageStat(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadTreeOp}}})
	deadImageStat  = model.GarbageStat(model.OCIImageStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadImageOp}}})
	deadBlobStat   = model.GarbageStat(model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadBlobOp}}})
	deadSnapStat   = model.GarbageStat(model.ResticRepositoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadSnapshotOp}}})
	notRunOne      = &model.GarbageDeletion{NotRun: 1}
)

// A dry run deletes nothing, and warns of no kind it would not delete.
func TestGarbageCollector_GarbageCollect_DryRunDeletesNothing(t *testing.T) {
	// Arrange: no Erase is expected, so one would fail the mock.
	treeEraser := newMockEraser(model.GarbageActionDeleteTree)
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	trees.On("Find", mock.Anything, inComposeProject("demo")).Return(deadTreeStat, nil).Once()
	restic := newMockComposeProjectCollector(model.GarbageDataKindDeduplicatedVolumes)
	restic.On("Find", mock.Anything, inComposeProject("demo")).Return(deadSnapStat, nil).Once()
	locker, inflight := newHeldLocker(), newNoMarker()
	deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, locker, inflight)
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees, restic}
	deps.Erasers = []GarbageEraser{treeEraser}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.True(t, result.DryRun)
	for _, report := range result.Reports {
		assert.Nil(t, report.Deletion, report.DataKind)
	}
	assert.Empty(t, result.Warnings)
	assert.Nil(t, result.Total.FreedBytes)
	treeEraser.AssertExpectations(t)
	trees.AssertExpectations(t)
	restic.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// A run that deletes looks with every collector of every compose project first, then verifies its
// lock before each deletion, in the collectors' order, one compose project after the other; a kind
// with no deletion yet is warned of once, however many reports found it, and does not make the run
// partial.
func TestGarbageCollector_GarbageCollect_Deletes(t *testing.T) {
	// Arrange
	grantedLock := &mockGrantedSessionLock{}
	sessionLocker := newGrantingSessionLocker(grantedLock)
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	otherSavepoints := &mockSavepointReader{}
	otherSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	images := newMockComposeProjectCollector(model.GarbageDataKindUpstreamImages)
	restic := newMockComposeProjectCollector(model.GarbageDataKindDeduplicatedVolumes)
	treeEraser := newMockEraser(model.GarbageActionDeleteTree)
	imageEraser := newMockEraser(model.GarbageActionDeleteImage)
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	tags.On("Find", mock.Anything, mock.MatchedBy(func(scope DaemonScope) bool { return len(scope.ComposeProjects) == 2 })).
		Return(model.DockerLayerTagStat{GarbageFindings: model.GarbageFindings{
			Ops: []model.GarbageOp{{Action: model.GarbageActionUntag, Target: "arcave/demo-db-1:sp-x"}},
		}}, nil).Once()
	var chain []*mock.Call
	composeProjects := []model.DockerComposeProjectName{"demo", "other"}
	for _, name := range composeProjects {
		chain = append(chain,
			trees.On("Find", mock.Anything, inComposeProject(name)).Return(deadTreeStat, nil).Once(),
			images.On("Find", mock.Anything, inComposeProject(name)).Return(deadImageStat, nil).Once(),
			restic.On("Find", mock.Anything, inComposeProject(name)).Return(deadSnapStat, nil).Once(),
		)
	}
	for _, name := range composeProjects {
		chain = append(chain,
			grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
			treeEraser.On("Erase", mock.Anything, storeOf(name), []model.GarbageOp{deadTreeOp}).Return(newDeletedOps(deadTreeOp)).Once(),
			grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
			imageEraser.On("Erase", mock.Anything, storeOf(name), []model.GarbageOp{deadImageOp}).Return(newDeletedOps(deadImageOp)).Once(),
		)
	}
	chain = append(chain, grantedLock.On("Unlock", mock.Anything).Return(nil).Once())
	mock.InOrder(chain...)
	locker, inflight := newHeldLocker(), newNoMarker()
	deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo", "other"), &mockSessions{}, locker, inflight)
	deps.SessionLocker = sessionLocker
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints, otherSavepoints)
	deps.DaemonCollectors = []DaemonCollector{tags}
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees, images, restic}
	deps.Erasers = []GarbageEraser{treeEraser, imageEraser}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{})

	// Assert
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	var deletions []*model.GarbageDeletion
	for _, report := range result.Reports {
		deletions = append(deletions, report.Deletion)
	}
	treeDeleted, imageDeleted := utils.New(newDeletedOps(deadTreeOp)), utils.New(newDeletedOps(deadImageOp))
	assert.Equal(t, []*model.GarbageDeletion{
		nil, // a daemon collector only reports
		treeDeleted, imageDeleted, nil,
		treeDeleted, imageDeleted, nil,
	}, deletions)
	assert.Equal(t, model.GarbageTotal{
		Found: 7, Bytes: 28, BytesComplete: false, Deleted: 4, FreedBytes: utils.New(int64(28)),
	}, result.Total)
	assert.Equal(t, []string{
		"restored-layers is reported, not deleted, yet",
		"deduplicated-volumes is reported, not deleted, yet",
	}, result.Warnings)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	trees.AssertExpectations(t)
	images.AssertExpectations(t)
	restic.AssertExpectations(t)
	tags.AssertExpectations(t)
	treeEraser.AssertExpectations(t)
	imageEraser.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	otherSavepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// A Verify that refuses stops its deletion and every one after it, in later compose projects too:
// their ops are not run, and the refusal stops the run beside what was deleted before it.
func TestGarbageCollector_GarbageCollect_VerifyRefuses(t *testing.T) {
	// Arrange
	grantedLock := &mockGrantedSessionLock{}
	sessionLocker := newGrantingSessionLocker(grantedLock)
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	otherSavepoints := &mockSavepointReader{}
	otherSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	images := newMockComposeProjectCollector(model.GarbageDataKindUpstreamImages)
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	treeEraser := newMockEraser(model.GarbageActionDeleteTree)
	erasers := []GarbageEraser{
		treeEraser, newMockEraser(model.GarbageActionDeleteImage), newMockEraser(model.GarbageActionDeleteObject),
	}
	mock.InOrder(
		trees.On("Find", mock.Anything, inComposeProject("demo")).Return(deadTreeStat, nil).Once(),
		images.On("Find", mock.Anything, inComposeProject("demo")).Return(deadImageStat, nil).Once(),
		blobs.On("Find", mock.Anything, inComposeProject("demo")).Return(deadBlobStat, nil).Once(),
		trees.On("Find", mock.Anything, inComposeProject("other")).Return(deadTreeStat, nil).Once(),
		images.On("Find", mock.Anything, inComposeProject("other")).Return(deadImageStat, nil).Once(),
		blobs.On("Find", mock.Anything, inComposeProject("other")).Return(deadBlobStat, nil).Once(),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		treeEraser.On("Erase", mock.Anything, storeOf("demo"), []model.GarbageOp{deadTreeOp}).Return(newDeletedOps(deadTreeOp)).Once(),
		grantedLock.On("Verify", mock.Anything).Return(model.ErrSessionLockNotHeld).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
	)
	locker, inflight := newHeldLocker(), newNoMarker()
	deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo", "other"), &mockSessions{}, locker, inflight)
	deps.SessionLocker = sessionLocker
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints, otherSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees, images, blobs}
	deps.Erasers = erasers
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockNotHeld)
	var interrupted *model.GarbageCollectInterruptedError
	require.ErrorAs(t, err, &interrupted)
	require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
	var deletions []*model.GarbageDeletion
	for _, report := range result.Reports {
		deletions = append(deletions, report.Deletion)
	}
	assert.Equal(t, []*model.GarbageDeletion{
		utils.New(newDeletedOps(deadTreeOp)), notRunOne, notRunOne,
		notRunOne, notRunOne, notRunOne,
	}, deletions)
	assert.Equal(t, 1, result.Total.Deleted)
	assert.Equal(t, 5, result.Total.NotRun)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	trees.AssertExpectations(t)
	images.AssertExpectations(t)
	blobs.AssertExpectations(t)
	for _, eraser := range erasers {
		eraser.(*mockEraser).AssertExpectations(t)
	}
	demoSavepoints.AssertExpectations(t)
	otherSavepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// What one report's deletion does to the result: a failed or aborted report deletes nothing, an
// unjudged object holds none of its kind's ops back, a report with no ops verifies nothing, and a
// failed or unstarted deletion makes the run partial.
func TestGarbageCollector_GarbageCollect_DeletesAReport(t *testing.T) {
	unknownSizeOp := model.GarbageOp{Action: model.GarbageActionDeleteObject, Target: deadBlobOp.Target}

	tests := []struct {
		name    string
		stat    model.GarbageStat
		findErr error
		// deletion is what Erase returns; nil expects no Verify and no Erase.
		deletion     *model.GarbageDeletion
		wantDeletion *model.GarbageDeletion
		wantTotal    model.GarbageTotal
		wantPartial  bool
	}{
		{
			name:        "a failed report",
			findErr:     errors.New("listing failed"),
			wantTotal:   model.GarbageTotal{Failed: 1},
			wantPartial: true,
		},
		{
			name: "an aborted report",
			stat: model.OCIBlobStat{GarbageFindings: model.GarbageFindings{
				Ops: []model.GarbageOp{}, Aborted: "1 image manifests cannot be read",
			}},
			wantTotal:   model.GarbageTotal{Aborted: 1},
			wantPartial: true,
		},
		{
			name: "a report with an unjudged object",
			stat: model.OCIBlobStat{GarbageFindings: model.GarbageFindings{
				Ops: []model.GarbageOp{deadBlobOp}, Unjudged: []model.GarbageSkip{{Target: "shared/oci/blobs/sha256/x"}},
			}},
			deletion:     utils.New(newDeletedOps(deadBlobOp)),
			wantDeletion: utils.New(newDeletedOps(deadBlobOp)),
			wantTotal: model.GarbageTotal{
				Found: 1, Bytes: 3, BytesComplete: true, Unjudged: 1, Deleted: 1, FreedBytes: utils.New(int64(3)),
			},
			wantPartial: true,
		},
		{
			name:         "a report with no ops",
			stat:         model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
			wantDeletion: &model.GarbageDeletion{},
			wantTotal:    model.GarbageTotal{BytesComplete: true, FreedBytes: utils.New(int64(0))},
		},
		{
			name:         "a deleted op of unknown size",
			stat:         model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{unknownSizeOp}}},
			deletion:     utils.New(newDeletedOps(unknownSizeOp)),
			wantDeletion: utils.New(newDeletedOps(unknownSizeOp)),
			wantTotal:    model.GarbageTotal{Found: 1, Deleted: 1},
		},
		{
			name: "an op failed",
			stat: deadBlobStat,
			deletion: &model.GarbageDeletion{
				Failed: []model.GarbageDeleteFailure{{Target: deadBlobOp.Target, Error: "access denied"}},
			},
			wantDeletion: &model.GarbageDeletion{
				Failed: []model.GarbageDeleteFailure{{Target: deadBlobOp.Target, Error: "access denied"}},
			},
			wantTotal:   model.GarbageTotal{Found: 1, Bytes: 3, BytesComplete: true, DeleteFailed: 1, FreedBytes: utils.New(int64(0))},
			wantPartial: true,
		},
		{
			name:         "an op not run",
			stat:         deadBlobStat,
			deletion:     notRunOne,
			wantDeletion: notRunOne,
			wantTotal:    model.GarbageTotal{Found: 1, Bytes: 3, BytesComplete: true, NotRun: 1, FreedBytes: utils.New(int64(0))},
			wantPartial:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			grantedLock := &mockGrantedSessionLock{}
			sessionLocker := newGrantingSessionLocker(grantedLock)
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
			blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
			objectEraser := newMockEraser(model.GarbageActionDeleteObject)
			chain := []*mock.Call{blobs.On("Find", mock.Anything, inComposeProject("demo")).Return(tt.stat, tt.findErr).Once()}
			if tt.deletion != nil {
				chain = append(chain,
					grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
					objectEraser.On("Erase", mock.Anything, storeOf("demo"), tt.stat.Findings().Ops).Return(*tt.deletion).Once(),
				)
			}
			chain = append(chain, grantedLock.On("Unlock", mock.Anything).Return(nil).Once())
			mock.InOrder(chain...)
			locker, inflight := newHeldLocker(), newNoMarker()
			deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, locker, inflight)
			deps.SessionLocker = sessionLocker
			deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
			deps.Erasers = []GarbageEraser{objectEraser}
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{})

			// Assert
			if tt.wantPartial {
				require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, result.Reports, 1)
			assert.Equal(t, tt.wantDeletion, result.Reports[0].Deletion)
			assert.Equal(t, tt.wantTotal, result.Total)
			assert.Empty(t, result.Warnings)
			sessionLocker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			blobs.AssertExpectations(t)
			objectEraser.AssertExpectations(t)
			demoSavepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			inflight.AssertExpectations(t)
		})
	}
}

// A standing marker, or one that cannot be read, refuses a run that deletes before anything is
// looked at.
func TestGarbageCollector_GarbageCollect_InterruptedRollbackRefusesToDelete(t *testing.T) {
	marker := model.InflightMarker{
		Session: model.DefaultSession, ComposeProject: "demo", Target: "019e843e-1650-7000-8000-000000000001",
	}
	readErr := errors.New("permission denied")

	tests := []struct {
		name       string
		marker     model.InflightMarker
		found      bool
		readErr    error
		wantMarker bool
	}{
		{name: "a standing marker", marker: marker, found: true, wantMarker: true},
		{name: "a marker that cannot be read", readErr: readErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: blobs expects no Find, so one would fail the mock.
			inflight := &mockInflight{}
			inflight.On("Get", mock.Anything).Return(tt.marker, tt.found, tt.readErr).Once()
			blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
			locker := newHeldLocker()
			deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, locker, inflight)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{})

			// Assert
			if tt.wantMarker {
				var interrupted *model.InterruptedRollbackError
				require.ErrorAs(t, err, &interrupted)
				assert.Equal(t, marker, interrupted.Marker)
			} else {
				require.ErrorIs(t, err, readErr)
			}
			assert.Zero(t, result)
			blobs.AssertExpectations(t)
			inflight.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

// A run whose lock is lost once its last deletion is done cut nothing short: it is not interrupted.
func TestGarbageCollector_GarbageCollect_LockLostAfterTheLastDeletion(t *testing.T) {
	// Arrange: the mock locker hands the caller's context back as the lock's.
	ctx, loseLock := context.WithCancelCause(context.Background())
	defer loseLock(nil)
	grantedLock := &mockGrantedSessionLock{}
	sessionLocker := newGrantingSessionLocker(grantedLock)
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	treeEraser := newMockEraser(model.GarbageActionDeleteTree)
	mock.InOrder(
		trees.On("Find", mock.Anything, inComposeProject("demo")).Return(deadTreeStat, nil).Once(),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		treeEraser.On("Erase", mock.Anything, storeOf("demo"), []model.GarbageOp{deadTreeOp}).
			Run(func(mock.Arguments) { loseLock(model.ErrSessionLockLost) }).
			Return(newDeletedOps(deadTreeOp)).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
	)
	locker, inflight := newHeldLocker(), newNoMarker()
	deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, locker, inflight)
	deps.SessionLocker = sessionLocker
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
	deps.Erasers = []GarbageEraser{treeEraser, newMockEraser(model.GarbageActionDeleteImage)}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{})

	// Assert
	require.NoError(t, err)
	require.Len(t, result.Reports, 1)
	assert.Equal(t, utils.New(newDeletedOps(deadTreeOp)), result.Reports[0].Deletion)
	assert.Equal(t, 1, result.Total.Deleted)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	trees.AssertExpectations(t)
	treeEraser.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// A run that loses its lock after a deletion returns the loss beside what it had deleted, and starts
// no deletion past the loss.
func TestGarbageCollector_GarbageCollect_LockLostWhileDeleting(t *testing.T) {
	// Arrange: the mock locker hands the caller's context back as the lock's.
	ctx, loseLock := context.WithCancelCause(context.Background())
	defer loseLock(nil)
	grantedLock := &mockGrantedSessionLock{}
	sessionLocker := newGrantingSessionLocker(grantedLock)
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	images := newMockComposeProjectCollector(model.GarbageDataKindUpstreamImages)
	treeEraser := newMockEraser(model.GarbageActionDeleteTree)
	mock.InOrder(
		trees.On("Find", mock.Anything, inComposeProject("demo")).Return(deadTreeStat, nil).Once(),
		images.On("Find", mock.Anything, inComposeProject("demo")).Return(deadImageStat, nil).Once(),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		treeEraser.On("Erase", mock.Anything, storeOf("demo"), []model.GarbageOp{deadTreeOp}).
			Run(func(mock.Arguments) { loseLock(model.ErrSessionLockLost) }).
			Return(newDeletedOps(deadTreeOp)).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
	)
	locker, inflight := newHeldLocker(), newNoMarker()
	deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, locker, inflight)
	deps.SessionLocker = sessionLocker
	deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees, images}
	deps.Erasers = []GarbageEraser{treeEraser, newMockEraser(model.GarbageActionDeleteImage)}
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	var interrupted *model.GarbageCollectInterruptedError
	require.ErrorAs(t, err, &interrupted)
	require.Len(t, result.Reports, 2)
	assert.Equal(t, utils.New(newDeletedOps(deadTreeOp)), result.Reports[0].Deletion)
	assert.Equal(t, notRunOne, result.Reports[1].Deletion)
	assert.Equal(t, 1, result.Total.Deleted)
	assert.Equal(t, 1, result.Total.NotRun)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	trees.AssertExpectations(t)
	treeEraser.AssertExpectations(t)
	images.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// A report whose ops no one eraser carries out — an action none has, or several actions — is only
// reported: no Verify, no Erase, and its kind is warned of.
func TestGarbageCollector_GarbageCollect_OnlyReportsWhatNoEraserCarriesOut(t *testing.T) {
	tests := []struct {
		name string
		stat model.GarbageStat
	}{
		{name: "an action no eraser carries out", stat: deadSnapStat},
		{
			name: "ops of several actions",
			stat: model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadBlobOp, deadTreeOp}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: neither eraser expects Erase, so one would fail the mock.
			grantedLock := &mockGrantedSessionLock{}
			sessionLocker := newGrantingSessionLocker(grantedLock)
			grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
			collector := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
			collector.On("Find", mock.Anything, inComposeProject("demo")).Return(tt.stat, nil).Once()
			objectEraser := newMockEraser(model.GarbageActionDeleteObject)
			treeEraser := newMockEraser(model.GarbageActionDeleteTree)
			locker, inflight := newHeldLocker(), newNoMarker()
			deps := newDeps(model.DefaultSession, newGCStore(t, model.DefaultSession, "demo"), &mockSessions{}, locker, inflight)
			deps.SessionLocker = sessionLocker
			deps.NewSavepoints = newSavepointsInOrder(t, demoSavepoints)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{collector}
			deps.Erasers = []GarbageEraser{objectEraser, treeEraser}
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{})

			// Assert
			require.NoError(t, err)
			require.Len(t, result.Reports, 1)
			assert.Nil(t, result.Reports[0].Deletion)
			assert.Equal(t, []string{"image-layers is reported, not deleted, yet"}, result.Warnings)
			sessionLocker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			collector.AssertExpectations(t)
			objectEraser.AssertExpectations(t)
			treeEraser.AssertExpectations(t)
			demoSavepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			inflight.AssertExpectations(t)
		})
	}
}
