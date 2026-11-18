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
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/utils"
)

var (
	gcSession = mustParseSessionID("019e843e-1650-7000-8000-00000000000a")
	// gcDeadSavepoint is a savepoint tree no record commits, whose capture is dead.
	gcDeadSavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000002")
	// blobKinds asks for the image layers alone: no retention runs.
	blobKinds = []model.GarbageDataKind{model.GarbageDataKindImageLayers}

	deadTreeOp = model.GarbageOp{
		Action: model.GarbageActionDeleteTree, Target: gcDeadSavepoint.String(), Bytes: utils.New(int64(10)),
		Savepoint: gcDeadSavepoint,
	}
	deadBlobOp = model.GarbageOp{
		Action: model.GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/" + strings.Repeat("b", 64),
		Bytes: utils.New(int64(3)),
	}
	deadTreeStat = model.GarbageStat(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadTreeOp}}})
	deadBlobStat = model.GarbageStat(model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadBlobOp}}})
	notRunOne    = &model.GarbageDeletion{NotRun: 1}
)

func mustParseSessionID(s string) model.SessionID {
	id, err := model.ParseSessionID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// newDeps is a gc over session; the provider's session is the test's.
func newDeps(
	session model.SessionID, sessions *mockSessions, garbage *mockSession, locker *mockLocker, inflight *mockInflight,
) Deps {
	garbage.id = session
	return Deps{
		Sessions:        sessions,
		ProviderSession: garbage,
		Locker:          locker,
		WriteAdmission:  fakeWriteAdmission{},
		SessionLocker:   fakeSessionLocker{},
		Inflight:        inflight,
		RetainRecent:    1,
	}
}

// newHeldLocker is a lock acquired and released once, asserted when the test ends.
func newHeldLocker(t *testing.T) *mockLocker {
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	locker.On("Release").Return(nil).Once().NotBefore(acquire)
	t.Cleanup(func() { locker.AssertExpectations(t) })
	return locker
}

// newNoMarker is an inflight marker read once and not found, asserted when the test ends.
func newNoMarker(t *testing.T) *mockInflight {
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	t.Cleanup(func() { inflight.AssertExpectations(t) })
	return inflight
}

// newGrantingSessionLocker is a session locker granting grantedLock once, asserted when the test
// ends.
func newGrantingSessionLocker(t *testing.T, grantedLock *mockGrantedSessionLock) *mockSessionLocker {
	sessionLocker := &mockSessionLocker{}
	sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationGarbageCollect).Return(grantedLock, nil).Once()
	t.Cleanup(func() { sessionLocker.AssertExpectations(t) })
	return sessionLocker
}

// newReport is what a collector of dataKind found in composeProject, as stat.
func newReport(
	dataKind model.GarbageDataKind, composeProject model.DockerComposeProjectName, stat model.GarbageStat,
) model.GarbageReport {
	return model.GarbageReport{
		DataKind:       dataKind,
		Collector:      model.GarbageCollectorKind("mock-" + dataKind),
		ComposeProject: composeProject,
		Summary:        stat.Summary(),
		Detail:         stat,
	}
}

// newMockGarbage is a finding reporting report.
func newMockGarbage(report model.GarbageReport) *mockGarbage {
	garbage := &mockGarbage{}
	garbage.On("Report").Return(report).Maybe()
	return garbage
}

// newFindingSession is a session finding found among dataKinds once.
func newFindingSession(dataKinds []model.GarbageDataKind, found ...provider.Garbage) *mockSession {
	session := &mockSession{}
	session.On("FindGarbage", mock.Anything, dataKinds).Return(found).Once()
	return session
}

// newDeletedOps is a deletion of every op of ops.
func newDeletedOps(ops ...model.GarbageOp) model.GarbageDeletion {
	var deleted []model.GarbageDeleted
	for _, op := range ops {
		deleted = append(deleted, model.GarbageDeleted{Target: op.Target, Bytes: op.Bytes})
	}
	return model.GarbageDeletion{Deleted: deleted}
}

// deletionsOf are the deletions of result's reports, in order.
func deletionsOf(result model.GarbageCollectResult) []*model.GarbageDeletion {
	var deletions []*model.GarbageDeletion
	for _, report := range result.Reports {
		deletions = append(deletions, report.Deletion)
	}
	return deletions
}

func TestGarbageCollector_GarbageCollect_RefusedWhileLocked(t *testing.T) {
	// Arrange: the session expects nothing, so a finding would fail the mock.
	locker := &mockLocker{}
	locker.On("Acquire", mock.Anything).Return(model.ErrProjectLocked).Once()
	session := &mockSession{}
	svc := New(newDeps(model.DefaultSession, &mockSessions{}, session, locker, &mockInflight{}))

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert: a capture may be reusing a stored blob its record does not name yet.
	require.ErrorIs(t, err, model.ErrProjectLocked)
	session.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect_HoldsTheSessionLock(t *testing.T) {
	// Arrange: the session is checked, and the garbage found, under the session's lock: a tree no
	// record commits is dead only while no capture can run.
	locker := &mockLocker{}
	sessionLocker := &mockSessionLocker{}
	sessions := &mockSessions{}
	grantedLock := &mockGrantedSessionLock{}
	session := &mockSession{}
	admission := &mockWriteAdmission{}
	mock.InOrder(
		locker.On("Acquire", mock.Anything).Return(nil).Once(),
		// A dry run writes too: the session's lock.
		admission.On("AdmitWrite", mock.Anything).Return(nil).Once(),
		sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationGarbageCollect).Return(grantedLock, nil).Once(),
		sessions.On("LoadSession", mock.Anything, gcSession).Return(model.Session{ID: gcSession}, true, nil).Once(),
		session.On("FindGarbage", mock.Anything, []model.GarbageDataKind(nil)).Return([]provider.Garbage{}).Once(),
		session.On("Workloads", mock.Anything).Return([]provider.WorkloadName{}, nil).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
		locker.On("Release").Return(nil).Once(),
	)
	deps := newDeps(gcSession, sessions, session, locker, newNoMarker(t))
	deps.WriteAdmission = admission
	deps.SessionLocker = sessionLocker
	svc := New(deps)

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	locker.AssertExpectations(t)
	admission.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	sessions.AssertExpectations(t)
	session.AssertExpectations(t)
}

// A run that loses the session's lock while finding returns the loss, not what it judged past it,
// and a run that deletes deletes nothing: no retention, no Verify, no Delete.
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
			trees := newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
			session := &mockSession{}
			session.On("FindGarbage", mock.Anything, []model.GarbageDataKind(nil)).
				Run(func(mock.Arguments) { loseLock(model.ErrSessionLockLost) }).
				Return([]provider.Garbage{trees}).Once()
			deps := newDeps(gcSession, sessions, session, newHeldLocker(t), newNoMarker(t))
			deps.SessionLocker = sessionLocker
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
			session.AssertExpectations(t)
			trees.AssertExpectations(t)
		})
	}
}

func TestGarbageCollector_GarbageCollect_AWriteRefused(t *testing.T) {
	// Arrange: the admission refuses under the project lock, so the session is never locked.
	locker := &mockLocker{}
	admission := &mockWriteAdmission{}
	mock.InOrder(
		locker.On("Acquire", mock.Anything).Return(nil).Once(),
		admission.On("AdmitWrite", mock.Anything).Return(model.ErrStoreHoldsPlainData).Once(),
		locker.On("Release").Return(nil).Once(),
	)
	sessionLocker := &mockSessionLocker{}
	deps := newDeps(gcSession, &mockSessions{}, &mockSession{}, locker, &mockInflight{})
	deps.WriteAdmission = admission
	deps.SessionLocker = sessionLocker
	svc := New(deps)

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.ErrorIs(t, err, model.ErrStoreHoldsPlainData)
	locker.AssertExpectations(t)
	admission.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect_RefusedWhileSessionLocked(t *testing.T) {
	// Arrange
	sessionLocker := &mockSessionLocker{}
	sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationGarbageCollect).
		Return(nil, model.ErrSessionLocked).Once()
	locker := newHeldLocker(t)
	sessions := &mockSessions{}
	session := &mockSession{}
	deps := newDeps(gcSession, sessions, session, locker, &mockInflight{})
	deps.SessionLocker = sessionLocker
	svc := New(deps)

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert: another host's operation may be writing what the gc would judge.
	require.ErrorIs(t, err, model.ErrSessionLocked)
	session.AssertExpectations(t)
	locker.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	sessions.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect_SessionRemovedBeforeTheLock(t *testing.T) {
	// Arrange
	sessions := &mockSessions{}
	sessions.On("LoadSession", mock.Anything, gcSession).Return(model.Session{}, false, nil).Once()
	session := &mockSession{}
	svc := New(newDeps(gcSession, sessions, session, newHeldLocker(t), &mockInflight{}))

	// Act
	_, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionNotFound)
	session.AssertExpectations(t)
	sessions.AssertExpectations(t)
}

// A request refused for what it asks stops before the lock.
func TestGarbageCollector_GarbageCollect_InvalidRequest(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	svc := New(newDeps(model.DefaultSession, &mockSessions{}, &mockSession{}, locker, &mockInflight{}))
	req := model.GarbageCollectRequest{DryRun: true, DataKinds: []model.GarbageDataKind{"restic"}}

	// Act
	_, err := svc.GarbageCollect(context.Background(), req)

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidGarbageCollectRequest)
	locker.AssertExpectations(t)
}

func TestGarbageCollector_GarbageCollect_WarnsOfAStandingMarker(t *testing.T) {
	// Arrange
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{
		Session: model.DefaultSession, ComposeProject: "demo", Target: "019e843e-1650-7000-8000-000000000001",
	}, true, nil).Once()
	session := newFindingSession(blobKinds)
	svc := New(newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), inflight))

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true, DataKinds: blobKinds})

	// Assert
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "interrupted rollback")
	inflight.AssertExpectations(t)
	session.AssertExpectations(t)
}

// The provider's reports come first, the retention's after them, each compose project's safety
// snapshots past the newest retained; the result adds them up and groups what names a dead savepoint.
func TestGarbageCollector_GarbageCollect(t *testing.T) {
	// Arrange
	var (
		oldest   = model.SavepointID("019e843e-1650-7000-8000-00000000000b")
		newest   = model.SavepointID("019e843e-1650-7000-8000-00000000000c")
		plain    = model.SavepointID("019e843e-1650-7000-8000-00000000000d")
		safety   = map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}
		blobStat = model.GarbageStat(model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{
			{Action: model.GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/y", Bytes: utils.New(int64(3)), Savepoint: gcDeadSavepoint},
		}}})
		trees = newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
		blobs = newMockGarbage(newReport(model.GarbageDataKindImageLayers, "demo", blobStat))
	)
	demoSavepoints := &mockSavepoints{}
	demoSavepoints.On("List", mock.Anything).Return([]model.Savepoint{
		{ID: oldest, Labels: safety}, {ID: newest, Labels: safety}, {ID: plain},
	}, nil).Once()
	session := newFindingSession(nil, trees, blobs)
	session.On("Workloads", mock.Anything).Return([]provider.WorkloadName{"demo"}, nil).Once()
	session.On("Savepoints", provider.WorkloadName("demo")).Return(demoSavepoints, nil).Once()
	sessions := &mockSessions{}
	sessions.On("LoadSession", mock.Anything, gcSession).Return(model.Session{ID: gcSession}, true, nil).Once()
	inflight := newNoMarker(t)
	svc := New(newDeps(gcSession, sessions, session, newHeldLocker(t), inflight))

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, gcSession, result.Session)
	retention := model.SafetySnapshotStat{
		GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{{
			Action: model.GarbageActionDeleteSavepoint, Target: oldest.String(), Reason: model.GarbageReasonBeyondRetention,
		}}},
		Retained: 1,
	}
	assert.Equal(t, []model.GarbageReport{
		newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat),
		newReport(model.GarbageDataKindImageLayers, "demo", blobStat),
		{
			DataKind:       model.GarbageDataKindSafetySnapshots,
			Collector:      model.GarbageCollectorKindSafetySnapshotRetention,
			ComposeProject: "demo",
			Summary:        retention.Summary(),
			Detail:         retention,
		},
	}, result.Reports)
	assert.Equal(t, model.GarbageTotal{Found: 3, Bytes: 13, BytesComplete: false}, result.Total,
		"a retired safety snapshot's size is not known")
	assert.Equal(t, []model.DeadSavepoint{
		{ComposeProject: "demo", Savepoint: gcDeadSavepoint, Found: 2, Bytes: utils.New(int64(13))},
	}, result.Savepoints, "the per-savepoint section gathers its tree and the blobs only it lists")
	assert.Empty(t, result.Warnings)
	session.AssertExpectations(t)
	sessions.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// What the provider could not judge, or failed at, stays in its report and makes the run partial.
func TestGarbageCollector_GarbageCollect_Partial(t *testing.T) {
	tests := []struct {
		name      string
		report    model.GarbageReport
		wantTotal model.GarbageTotal
	}{
		{
			name: "an unjudged object",
			report: newReport(model.GarbageDataKindImageLayers, "demo", model.OCIBlobStat{GarbageFindings: model.GarbageFindings{
				Ops:      []model.GarbageOp{},
				Unjudged: []model.GarbageSkip{{Target: "shared/oci/blobs/sha256/x", Reason: model.GarbageReasonBackendUnreadable}},
			}}),
			wantTotal: model.GarbageTotal{BytesComplete: true, Unjudged: 1},
		},
		{
			name: "an aborted report",
			report: model.GarbageReport{
				DataKind: model.GarbageDataKindImageLayers, ComposeProject: "demo",
				Summary: model.GarbageSummary{Aborted: "cannot read the scope: corrupt record"},
			},
			wantTotal: model.GarbageTotal{Aborted: 1},
		},
		{
			name:      "a failed report",
			report:    model.GarbageReport{DataKind: model.GarbageDataKindImageLayers, ComposeProject: "demo", Error: "listing failed"},
			wantTotal: model.GarbageTotal{Failed: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			session := newFindingSession(blobKinds, newMockGarbage(tt.report))
			svc := New(newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t)))

			// Act
			result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true, DataKinds: blobKinds})

			// Assert
			require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
			assert.Equal(t, tt.wantTotal, result.Total)
			assert.Empty(t, result.Warnings)
			session.AssertExpectations(t)
		})
	}
}

// The retention runs when the request asks for safety snapshots, or for no kind at all; what it
// cannot read aborts its report, and makes the run partial.
func TestGarbageCollector_GarbageCollect_Retention(t *testing.T) {
	readErr := errors.New("access denied")

	tests := []struct {
		name      string
		dataKinds []model.GarbageDataKind
		// listsWorkloads, opensSavepoints and listsSavepoints are the reads made, each failing
		// with its error.
		listsWorkloads  bool
		workloadsErr    error
		opensSavepoints bool
		savepointsErr   error
		listsSavepoints bool
		listErr         error
		wantReports     []model.GarbageReport
		wantPartial     bool
	}{
		{
			name:        "another kind alone",
			dataKinds:   blobKinds,
			wantReports: []model.GarbageReport{},
		},
		{
			name:            "the safety snapshots alone",
			dataKinds:       []model.GarbageDataKind{model.GarbageDataKindSafetySnapshots},
			listsWorkloads:  true,
			opensSavepoints: true,
			listsSavepoints: true,
			wantReports: []model.GarbageReport{{
				DataKind:       model.GarbageDataKindSafetySnapshots,
				Collector:      model.GarbageCollectorKindSafetySnapshotRetention,
				ComposeProject: "demo",
				Summary:        model.GarbageSummary{Bytes: utils.New(int64(0))},
				Detail:         model.SafetySnapshotStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
			}},
		},
		{
			name:           "the compose projects cannot be listed",
			listsWorkloads: true,
			workloadsErr:   readErr,
			wantReports: []model.GarbageReport{{
				DataKind:  model.GarbageDataKindSafetySnapshots,
				Collector: model.GarbageCollectorKindSafetySnapshotRetention,
				Summary: model.GarbageSummary{
					Aborted: "cannot read the scope: list compose projects of session " + model.DefaultSession.String() + ": access denied",
				},
			}},
			wantPartial: true,
		},
		{
			name:            "a compose project cannot be opened",
			listsWorkloads:  true,
			opensSavepoints: true,
			savepointsErr:   readErr,
			wantReports: []model.GarbageReport{{
				DataKind:       model.GarbageDataKindSafetySnapshots,
				Collector:      model.GarbageCollectorKindSafetySnapshotRetention,
				ComposeProject: "demo",
				Summary:        model.GarbageSummary{Aborted: "cannot read the scope: open compose project demo: access denied"},
			}},
			wantPartial: true,
		},
		{
			name:            "its savepoints cannot be read",
			listsWorkloads:  true,
			opensSavepoints: true,
			listsSavepoints: true,
			listErr:         readErr,
			wantReports: []model.GarbageReport{{
				DataKind:       model.GarbageDataKindSafetySnapshots,
				Collector:      model.GarbageCollectorKindSafetySnapshotRetention,
				ComposeProject: "demo",
				Summary:        model.GarbageSummary{Aborted: "cannot read the scope: read savepoints of compose project demo: access denied"},
			}},
			wantPartial: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			session := newFindingSession(tt.dataKinds)
			demoSavepoints := &mockSavepoints{}
			if tt.listsWorkloads {
				session.On("Workloads", mock.Anything).Return([]provider.WorkloadName{"demo"}, tt.workloadsErr).Once()
			}
			if tt.opensSavepoints {
				session.On("Savepoints", provider.WorkloadName("demo")).Return(demoSavepoints, tt.savepointsErr).Once()
			}
			if tt.listsSavepoints {
				demoSavepoints.On("List", mock.Anything).Return([]model.Savepoint{}, tt.listErr).Once()
			}
			svc := New(newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t)))

			// Act
			result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{
				DryRun:    true,
				DataKinds: tt.dataKinds,
			})

			// Assert
			if tt.wantPartial {
				require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantReports, result.Reports)
			session.AssertExpectations(t)
			demoSavepoints.AssertExpectations(t)
		})
	}
}

// A dry run deletes nothing: no Verify, no Delete, no Remove.
func TestGarbageCollector_GarbageCollect_DryRunDeletesNothing(t *testing.T) {
	// Arrange: no Delete or Remove is expected, so one would fail the mock.
	safety := map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}
	trees := newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
	demoSavepoints := &mockSavepoints{}
	demoSavepoints.On("List", mock.Anything).Return([]model.Savepoint{
		{ID: "019e843e-1650-7000-8000-00000000000b", Labels: safety},
		{ID: "019e843e-1650-7000-8000-00000000000c", Labels: safety},
	}, nil).Once()
	session := newFindingSession(nil, trees)
	session.On("Workloads", mock.Anything).Return([]provider.WorkloadName{"demo"}, nil).Once()
	session.On("Savepoints", provider.WorkloadName("demo")).Return(demoSavepoints, nil).Once()
	grantedLock := &mockGrantedSessionLock{}
	grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	deps := newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t))
	deps.SessionLocker = newGrantingSessionLocker(t, grantedLock)
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.True(t, result.DryRun)
	assert.Equal(t, []*model.GarbageDeletion{nil, nil}, deletionsOf(result))
	assert.Nil(t, result.Total.FreedBytes)
	trees.AssertExpectations(t)
	session.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// A run that deletes finds everything first, then verifies its lock before each finding's deletion,
// in the order found: the provider's, then the safety snapshots past the retention, oldest last.
func TestGarbageCollector_GarbageCollect_Deletes(t *testing.T) {
	// Arrange
	var (
		older  = model.SavepointID("019e843e-1650-7000-8000-00000000000b")
		oldest = model.SavepointID("019e843e-1650-7000-8000-00000000000a")
		newest = model.SavepointID("019e843e-1650-7000-8000-00000000000c")
		safety = map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}
		trees  = newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
		blobs  = newMockGarbage(newReport(model.GarbageDataKindImageLayers, "demo", deadBlobStat))
	)
	grantedLock := &mockGrantedSessionLock{}
	demoSavepoints := &mockSavepoints{}
	session := &mockSession{}
	mock.InOrder(
		session.On("FindGarbage", mock.Anything, []model.GarbageDataKind(nil)).Return([]provider.Garbage{trees, blobs}).Once(),
		session.On("Workloads", mock.Anything).Return([]provider.WorkloadName{"demo"}, nil).Once(),
		session.On("Savepoints", provider.WorkloadName("demo")).Return(demoSavepoints, nil).Once(),
		demoSavepoints.On("List", mock.Anything).Return([]model.Savepoint{
			{ID: oldest, Labels: safety}, {ID: older, Labels: safety}, {ID: newest, Labels: safety},
		}, nil).Once(),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		trees.On("Delete", mock.Anything).Return(newDeletedOps(deadTreeOp)).Once(),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		blobs.On("Delete", mock.Anything).Return(newDeletedOps(deadBlobOp)).Once(),
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		demoSavepoints.On("Remove", mock.Anything, older).Return(nil).Once(),
		demoSavepoints.On("Remove", mock.Anything, oldest).Return(nil).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
	)
	locker, inflight := newHeldLocker(t), newNoMarker(t)
	deps := newDeps(model.DefaultSession, &mockSessions{}, session, locker, inflight)
	deps.SessionLocker = newGrantingSessionLocker(t, grantedLock)
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{})

	// Assert
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	assert.Equal(t, []*model.GarbageDeletion{
		utils.New(newDeletedOps(deadTreeOp)),
		utils.New(newDeletedOps(deadBlobOp)),
		{Deleted: []model.GarbageDeleted{{Target: older.String()}, {Target: oldest.String()}}},
	}, deletionsOf(result))
	assert.Equal(t, model.GarbageTotal{Found: 4, Bytes: 13, Deleted: 4}, result.Total,
		"what the safety snapshots freed is not known")
	assert.Empty(t, result.Warnings)
	grantedLock.AssertExpectations(t)
	session.AssertExpectations(t)
	demoSavepoints.AssertExpectations(t)
	trees.AssertExpectations(t)
	blobs.AssertExpectations(t)
	locker.AssertExpectations(t)
	inflight.AssertExpectations(t)
}

// A Verify that refuses stops its deletion and every one after it: their ops are not run, and the
// refusal stops the run beside what was deleted before it.
func TestGarbageCollector_GarbageCollect_VerifyRefuses(t *testing.T) {
	// Arrange: the last two findings expect no Delete, so one would fail the mock.
	trees := newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
	blobs := newMockGarbage(newReport(model.GarbageDataKindImageLayers, "demo", deadBlobStat))
	otherBlobs := newMockGarbage(newReport(model.GarbageDataKindImageLayers, "other", deadBlobStat))
	grantedLock := &mockGrantedSessionLock{}
	session := newFindingSession(blobKinds, trees, blobs, otherBlobs)
	mock.InOrder(
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		trees.On("Delete", mock.Anything).Return(newDeletedOps(deadTreeOp)).Once(),
		grantedLock.On("Verify", mock.Anything).Return(model.ErrSessionLockNotHeld).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
	)
	deps := newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t))
	deps.SessionLocker = newGrantingSessionLocker(t, grantedLock)
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DataKinds: blobKinds})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockNotHeld)
	var interrupted *model.GarbageCollectInterruptedError
	require.ErrorAs(t, err, &interrupted)
	require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
	assert.Equal(t, []*model.GarbageDeletion{utils.New(newDeletedOps(deadTreeOp)), notRunOne, notRunOne}, deletionsOf(result))
	assert.Equal(t, 1, result.Total.Deleted)
	assert.Equal(t, 2, result.Total.NotRun)
	grantedLock.AssertExpectations(t)
	session.AssertExpectations(t)
	trees.AssertExpectations(t)
	blobs.AssertExpectations(t)
	otherBlobs.AssertExpectations(t)
}

// A failed or aborted report deletes nothing, one with no ops verifies nothing, an unjudged object
// holds no op back, and a failed or unstarted deletion makes the run partial.
func TestGarbageCollector_GarbageCollect_DeletesAReport(t *testing.T) {
	unknownSizeOp := model.GarbageOp{Action: model.GarbageActionDeleteObject, Target: deadBlobOp.Target}

	tests := []struct {
		name   string
		report model.GarbageReport
		// deletion is what Delete returns; nil expects no Verify and no Delete.
		deletion     *model.GarbageDeletion
		wantDeletion *model.GarbageDeletion
		wantTotal    model.GarbageTotal
		wantPartial  bool
	}{
		{
			name:        "a failed report",
			report:      model.GarbageReport{DataKind: model.GarbageDataKindImageLayers, ComposeProject: "demo", Error: "listing failed"},
			wantTotal:   model.GarbageTotal{Failed: 1},
			wantPartial: true,
		},
		{
			name: "an aborted report",
			report: newReport(model.GarbageDataKindImageLayers, "demo", model.OCIBlobStat{GarbageFindings: model.GarbageFindings{
				Ops: []model.GarbageOp{}, Aborted: "1 image manifests cannot be read",
			}}),
			wantTotal:   model.GarbageTotal{Aborted: 1},
			wantPartial: true,
		},
		{
			name: "a report with an unjudged object",
			report: newReport(model.GarbageDataKindImageLayers, "demo", model.OCIBlobStat{GarbageFindings: model.GarbageFindings{
				Ops: []model.GarbageOp{deadBlobOp}, Unjudged: []model.GarbageSkip{{Target: "shared/oci/blobs/sha256/x"}},
			}}),
			deletion:     utils.New(newDeletedOps(deadBlobOp)),
			wantDeletion: utils.New(newDeletedOps(deadBlobOp)),
			wantTotal: model.GarbageTotal{
				Found: 1, Bytes: 3, BytesComplete: true, Unjudged: 1, Deleted: 1, FreedBytes: utils.New(int64(3)),
			},
			wantPartial: true,
		},
		{
			name:         "a report with no ops",
			report:       newReport(model.GarbageDataKindImageLayers, "demo", model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}),
			wantDeletion: &model.GarbageDeletion{},
			wantTotal:    model.GarbageTotal{BytesComplete: true, FreedBytes: utils.New(int64(0))},
		},
		{
			name: "a deleted op of unknown size",
			report: newReport(model.GarbageDataKindImageLayers, "demo", model.OCIBlobStat{GarbageFindings: model.GarbageFindings{
				Ops: []model.GarbageOp{unknownSizeOp},
			}}),
			deletion:     utils.New(newDeletedOps(unknownSizeOp)),
			wantDeletion: utils.New(newDeletedOps(unknownSizeOp)),
			wantTotal:    model.GarbageTotal{Found: 1, Deleted: 1},
		},
		{
			name:   "an op failed",
			report: newReport(model.GarbageDataKindImageLayers, "demo", deadBlobStat),
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
			report:       newReport(model.GarbageDataKindImageLayers, "demo", deadBlobStat),
			deletion:     notRunOne,
			wantDeletion: notRunOne,
			wantTotal:    model.GarbageTotal{Found: 1, Bytes: 3, BytesComplete: true, NotRun: 1, FreedBytes: utils.New(int64(0))},
			wantPartial:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			blobs := newMockGarbage(tt.report)
			grantedLock := &mockGrantedSessionLock{}
			var chain []*mock.Call
			if tt.deletion != nil {
				chain = append(chain,
					grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
					blobs.On("Delete", mock.Anything).Return(*tt.deletion).Once(),
				)
			}
			chain = append(chain, grantedLock.On("Unlock", mock.Anything).Return(nil).Once())
			mock.InOrder(chain...)
			session := newFindingSession(blobKinds, blobs)
			deps := newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t))
			deps.SessionLocker = newGrantingSessionLocker(t, grantedLock)
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(context.Background(), model.GarbageCollectRequest{DataKinds: blobKinds})

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
			grantedLock.AssertExpectations(t)
			blobs.AssertExpectations(t)
			session.AssertExpectations(t)
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
			// Arrange: the session expects no finding, so one would fail the mock.
			inflight := &mockInflight{}
			inflight.On("Get", mock.Anything).Return(tt.marker, tt.found, tt.readErr).Once()
			session := &mockSession{}
			locker := newHeldLocker(t)
			svc := New(newDeps(model.DefaultSession, &mockSessions{}, session, locker, inflight))

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
			session.AssertExpectations(t)
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
	trees := newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
	grantedLock := &mockGrantedSessionLock{}
	mock.InOrder(
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		trees.On("Delete", mock.Anything).
			Run(func(mock.Arguments) { loseLock(model.ErrSessionLockLost) }).
			Return(newDeletedOps(deadTreeOp)).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
	)
	session := newFindingSession(blobKinds, trees)
	deps := newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t))
	deps.SessionLocker = newGrantingSessionLocker(t, grantedLock)
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{DataKinds: blobKinds})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []*model.GarbageDeletion{utils.New(newDeletedOps(deadTreeOp))}, deletionsOf(result))
	assert.Equal(t, 1, result.Total.Deleted)
	grantedLock.AssertExpectations(t)
	trees.AssertExpectations(t)
	session.AssertExpectations(t)
}

// A run that loses its lock after a deletion returns the loss beside what it had deleted, and starts
// no deletion past the loss.
func TestGarbageCollector_GarbageCollect_LockLostWhileDeleting(t *testing.T) {
	// Arrange: the mock locker hands the caller's context back as the lock's; blobs expects no Delete.
	ctx, loseLock := context.WithCancelCause(context.Background())
	defer loseLock(nil)
	trees := newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
	blobs := newMockGarbage(newReport(model.GarbageDataKindImageLayers, "demo", deadBlobStat))
	grantedLock := &mockGrantedSessionLock{}
	mock.InOrder(
		grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
		trees.On("Delete", mock.Anything).
			Run(func(mock.Arguments) { loseLock(model.ErrSessionLockLost) }).
			Return(newDeletedOps(deadTreeOp)).Once(),
		grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
	)
	session := newFindingSession(blobKinds, trees, blobs)
	deps := newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t))
	deps.SessionLocker = newGrantingSessionLocker(t, grantedLock)
	svc := New(deps)

	// Act
	result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{DataKinds: blobKinds})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	var interrupted *model.GarbageCollectInterruptedError
	require.ErrorAs(t, err, &interrupted)
	assert.Equal(t, []*model.GarbageDeletion{utils.New(newDeletedOps(deadTreeOp)), notRunOne}, deletionsOf(result))
	assert.Equal(t, 1, result.Total.Deleted)
	assert.Equal(t, 1, result.Total.NotRun)
	grantedLock.AssertExpectations(t)
	trees.AssertExpectations(t)
	blobs.AssertExpectations(t)
	session.AssertExpectations(t)
}

// A stop that fails the last report's ops cut that report short: the run is interrupted, not
// merely partial; a failure of its own, the stop coming after, cut nothing short.
func TestGarbageCollector_GarbageCollect_LockLostFailingTheLastDeletion(t *testing.T) {
	tests := []struct {
		name        string
		failure     model.GarbageDeleteFailure
		interrupted bool
	}{
		{
			name:        "a failure the stop cut short",
			failure:     model.GarbageDeleteFailure{Target: deadTreeOp.Target, Error: "context canceled", Interrupted: true},
			interrupted: true,
		},
		{
			name:    "a failure of its own",
			failure: model.GarbageDeleteFailure{Target: deadTreeOp.Target, Error: "permission denied"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: the mock locker hands the caller's context back as the lock's.
			ctx, loseLock := context.WithCancelCause(context.Background())
			defer loseLock(nil)
			failed := model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{tt.failure}}
			trees := newMockGarbage(newReport(model.GarbageDataKindSavepointData, "demo", deadTreeStat))
			grantedLock := &mockGrantedSessionLock{}
			mock.InOrder(
				grantedLock.On("Verify", mock.Anything).Return(nil).Once(),
				trees.On("Delete", mock.Anything).
					Run(func(mock.Arguments) { loseLock(model.ErrSessionLockLost) }).
					Return(failed).Once(),
				grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
			)
			session := newFindingSession(blobKinds, trees)
			deps := newDeps(model.DefaultSession, &mockSessions{}, session, newHeldLocker(t), newNoMarker(t))
			deps.SessionLocker = newGrantingSessionLocker(t, grantedLock)
			svc := New(deps)

			// Act
			result, err := svc.GarbageCollect(ctx, model.GarbageCollectRequest{DataKinds: blobKinds})

			// Assert
			var interrupted *model.GarbageCollectInterruptedError
			if tt.interrupted {
				require.ErrorAs(t, err, &interrupted)
				assert.ErrorIs(t, err, model.ErrSessionLockLost)
			} else {
				require.ErrorIs(t, err, model.ErrGarbageCollectPartial)
				assert.NotErrorAs(t, err, &interrupted)
			}
			assert.Equal(t, []*model.GarbageDeletion{&failed}, deletionsOf(result))
			grantedLock.AssertExpectations(t)
			trees.AssertExpectations(t)
			session.AssertExpectations(t)
		})
	}
}

// A retired safety snapshot is removed through its compose project's savepoints, in order; one
// that fails stays failed, and none starts once the run is stopped.
func TestRetired_Delete(t *testing.T) {
	var (
		first  = model.SavepointID("019e843e-1650-7000-8000-00000000000b")
		second = model.SavepointID("019e843e-1650-7000-8000-00000000000a")
		stat   = newRetentionStat([]model.Savepoint{
			{ID: first, Labels: map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}},
			{ID: second, Labels: map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}},
		}, 0)
	)

	tests := []struct {
		name string
		// removeErr fails the first Remove; stops stops the run during it.
		removeErr error
		stops     bool
		want      model.GarbageDeletion
	}{
		{
			name:      "one the stop cut short fails interrupted, the rest not run",
			removeErr: context.Canceled,
			stops:     true,
			want: model.GarbageDeletion{
				Failed: []model.GarbageDeleteFailure{{Target: first.String(), Error: "context canceled", Interrupted: true}},
				NotRun: 1,
			},
		},
		{
			name: "every one goes",
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{{Target: first.String()}, {Target: second.String()}}},
		},
		{
			name:      "one that cannot go fails, the next goes",
			removeErr: errors.New("delete savepoint record: access denied"),
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: second.String()}},
				Failed:  []model.GarbageDeleteFailure{{Target: first.String(), Error: "delete savepoint record: access denied"}},
			},
		},
		{
			name:  "a stop leaves the rest not run",
			stops: true,
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: first.String()}},
				NotRun:  1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			savepoints := &mockSavepoints{}
			removeFirst := savepoints.On("Remove", mock.Anything, first).Return(tt.removeErr).Once()
			if tt.stops {
				removeFirst.Run(func(mock.Arguments) { stop() })
			} else {
				savepoints.On("Remove", mock.Anything, second).Return(nil).Once().NotBefore(removeFirst)
			}
			garbage := retired{
				savepoints: savepoints,
				report:     newRetentionReport("demo", stat.Summary(), stat),
			}

			// Act
			got := garbage.Delete(ctx)

			// Assert
			assert.Equal(t, tt.want, got)
			savepoints.AssertExpectations(t)
		})
	}
}

// A retention report that found nothing to judge — aborted — deletes nothing, whoever calls it.
func TestRetired_Delete_NoFinding(t *testing.T) {
	// Arrange: no savepoints, so a Remove would panic the test.
	garbage := retired{report: newRetentionReport("demo", model.GarbageSummary{Aborted: "cannot read the scope: x"}, nil)}

	// Act
	got := garbage.Delete(context.Background())

	// Assert
	assert.Equal(t, model.GarbageDeletion{}, got)
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
