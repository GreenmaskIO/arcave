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
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/utils"
)

var (
	errWorkflow  = errors.New("workflow failed")
	errListStore = errors.New("store unreachable")
	errStore     = errors.New("store hiccup")
)

func newLockTestContext() context.Context {
	return zerolog.Nop().WithContext(context.Background())
}

func TestWithSessionLock(t *testing.T) {
	tests := []struct {
		name string
		// lockErr fails the Lock; lost cancels the context it returns with ErrSessionLockLost.
		lockErr error
		lost    bool
		// fnErr fails the workflow; interrupted reports it as a workflow reports an interrupt.
		fnErr       error
		interrupted bool
		freeErr     error
		want        string
		wantErr     error
	}{
		{
			name: "the workflow runs under the lock and frees it",
			want: "done",
		},
		{
			name:    "a session held past the wait runs nothing",
			lockErr: model.ErrSessionLocked,
			wantErr: model.ErrSessionLocked,
		},
		{
			name:        "a workflow interrupted by the lost lock reports the lost lock",
			lost:        true,
			fnErr:       errWorkflow,
			interrupted: true,
			wantErr:     model.ErrSessionLockLost,
		},
		{
			name:    "a workflow failing on its own after the lock is lost reports its own failure",
			lost:    true,
			fnErr:   errWorkflow,
			wantErr: errWorkflow,
		},
		{
			name: "a workflow finishing after the lock is lost keeps its result",
			lost: true,
			want: "done",
		},
		{
			name:    "a workflow failing on its own reports its own failure",
			fnErr:   errWorkflow,
			wantErr: errWorkflow,
		},
		{
			name:    "a failed unlock is only logged",
			freeErr: errors.New("store hiccup"),
			want:    "done",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockSessionLocker{}
			grantedLock := &mockGrantedSessionLock{}
			if tt.lost {
				grantedLock.lostCause = model.ErrSessionLockLost
			}
			var ranWith model.GrantedSessionLock
			lock := locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).
				Return(grantedLock, tt.lockErr).Once()
			if tt.lockErr == nil {
				unlock := grantedLock.On("Unlock", mock.Anything).Return(tt.freeErr).Once().
					Run(func(mock.Arguments) { assert.Same(t, grantedLock, ranWith, "the workflow ran before the unlock") })
				mock.InOrder(lock, unlock)
			}

			// Act
			got, err := withSessionLock(newLockTestContext(), locker, model.SessionLockOperationSavepointCreate,
				func(ctx context.Context, grantedLock model.GrantedSessionLock) (string, error) {
					ranWith = grantedLock
					if tt.interrupted {
						return "", preferInterrupt(ctx, tt.fnErr)
					}
					if tt.fnErr != nil {
						return "", tt.fnErr
					}
					return "done", nil
				})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.lockErr == nil, ranWith != nil, "the workflow runs only under the lock")
			locker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}

// Every workflow takes the project lock, then the session's lock under its own kind, and frees
// them in reverse.
func TestService_locked(t *testing.T) {
	tests := []struct {
		name      string
		operation model.SessionLockOperation
		run       func(svc *Service) error
	}{
		{
			name:      "savepoint create",
			operation: model.SessionLockOperationSavepointCreate,
			run: func(svc *Service) error {
				_, err := svc.CreateSavepoint(newLockTestContext(), model.CreateSavepointRequest{Name: "sp"})
				return err
			},
		},
		{
			name:      "savepoint remove",
			operation: model.SessionLockOperationSavepointRemove,
			run: func(svc *Service) error {
				_, err := svc.RemoveSavepoints(newLockTestContext(),
					model.RemoveRequest{IDs: []model.SavepointID{"019e843e-1650-7000-8000-000000000001"}})
				return err
			},
		},
		{
			name:      "rollback",
			operation: model.SessionLockOperationRollback,
			run: func(svc *Service) error {
				_, err := svc.Rollback(newLockTestContext(), model.RollbackRequest{Latest: utils.New(true)})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: the first read under the locks fails, so the workflow stops there.
			locker := &mockLocker{}
			sessionLocker := &mockSessionLocker{}
			repo := &mockSavepointRepo{}
			grantedLock := &mockGrantedSessionLock{}
			calls := []*mock.Call{
				locker.On("Acquire", mock.Anything).Return(nil).Once(),
				sessionLocker.On("Lock", mock.Anything, tt.operation).Return(grantedLock, nil).Once(),
			}
			calls = append(calls,
				repo.On("ListSavepoints", mock.Anything).Return(nil, errListStore).Once(),
				grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
				locker.On("Release").Return(nil).Once(),
			)
			mock.InOrder(calls...)
			svc := New(Deps{
				ProjectLocker: locker,
				SessionLocker: sessionLocker,
				SavepointRepo: repo,
				Inflight:      &fakeInflight{},
			})

			// Act
			err := tt.run(svc)

			// Assert
			require.ErrorIs(t, err, errListStore)
			locker.AssertExpectations(t)
			sessionLocker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

func TestService_CreateSavepoint_RefusedWhileSessionLocked(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	sessionLocker := &mockSessionLocker{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).
		Return(nil, model.ErrSessionLocked).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, lock, release)
	inspector := &mockInspector{}
	svc := New(Deps{
		ProjectLocker:    locker,
		SessionLocker:    sessionLocker,
		ComposeInspector: inspector,
		Inflight:         &fakeInflight{},
	})

	// Act
	_, err := svc.CreateSavepoint(newLockTestContext(), model.CreateSavepointRequest{Name: "sp"})

	// Assert: another host's operation holds the session; nothing is read or stopped.
	require.ErrorIs(t, err, model.ErrSessionLocked)
	locker.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	inspector.AssertExpectations(t)
}

// A capture commits its record only while the session's lock is still its own.
func TestService_performSavepoint_SessionLock(t *testing.T) {
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
			working := workingSet{backends: []model.VolumeSnapshotBackend{backend}, spec: demoSpecBytes}

			// Act
			savepoint, err := svc.performSavepoint(newLockTestContext(), grantedLock, working, nil, "sp", "", nil)

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

func TestSessionService_RemoveSession_SessionLock(t *testing.T) {
	tests := []struct {
		name    string
		lockErr error
		// verifyErr fails the check ahead of the delete.
		verifyErr error
		wantErr   error
	}{
		{
			name:    "a session another host works in is not deleted under it",
			lockErr: model.ErrSessionLocked,
			wantErr: model.ErrSessionLocked,
		},
		{
			name:      "a lost lock deletes nothing",
			verifyErr: model.ErrSessionLockLost,
			wantErr:   model.ErrSessionLockLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).
				Return([]model.Session{model.DefaultSessionRecord, sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
			sessionLocker := &mockSessionLocker{}
			grantedLock := &mockGrantedSessionLock{}
			sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSessionRemove).Return(grantedLock, tt.lockErr).Once()
			if tt.lockErr == nil {
				repo.On("SessionSize", mock.Anything, demoSession).Return(int64(4096), nil).Once()
				grantedLock.On("Verify", mock.Anything).Return(tt.verifyErr).Once()
				grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
			}
			svc := NewSessionService(SessionDeps{
				NewSessionLocker: func(model.SessionID) (model.SessionLocker, error) { return sessionLocker, nil },
				Current:          &fakePointer{},
				Sessions:         repo,
				Locker:           locker,
				Inflight:         &fakeInflight{},
			})

			// Act
			_, err := svc.RemoveSession(newLockTestContext(), model.RemoveSessionRequest{Session: demoSession.String()})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			repo.AssertExpectations(t)
			sessionLocker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

// A fork commits its record only while its source's lock is still its own.
func TestSessionService_CreateSession_CloneLostLockCommitsNothing(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	beats := &mockHeartbeats{}
	repo.On("ListSessions", mock.Anything).
		Return([]model.Session{model.DefaultSessionRecord, sessionRecord(otherSession, "exp", sessionNow)}, nil).Once()
	repo.On("IsSessionTreeEmpty", mock.Anything, mock.Anything).Return(true, nil).Once()
	beats.On("Write", mock.Anything, mock.Anything, forkBeatOf(heartbeat.StatusInProgress)).Return(nil).Once()
	repo.On("CopySession", mock.Anything, otherSession, mock.Anything, []string{heartbeat.FileName}).
		Return(int64(4096), nil).Once()
	beats.On("Write", mock.Anything, mock.Anything, forkBeatOf(heartbeat.StatusFailed)).Return(nil).Once()
	sessionLocker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{}
	sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSessionFork).Return(grantedLock, nil).Once()
	repo.On("LoadSession", mock.Anything, otherSession).Return(sessionRecord(otherSession, "exp", sessionNow), true, nil).Once()
	grantedLock.On("Verify", mock.Anything).Return(model.ErrSessionLockLost).Once()
	grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	svc := NewSessionService(SessionDeps{
		NewSessionLocker: func(model.SessionID) (model.SessionLocker, error) { return sessionLocker, nil },
		Current:          &fakePointer{},
		Sessions:         repo,
		Locker:           locker,
		RunnerInfo:       testRunnerInfo,
	})
	svc.heartbeatWriter = beats.writerFor

	// Act
	_, err := svc.CreateSession(newLockTestContext(), model.CreateSessionRequest{Name: "feat/x", From: "exp"})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	repo.AssertExpectations(t)
	beats.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// A source removed while the fork waited for its lock is not copied: the fork would commit an
// empty session claiming to come from it.
func TestSessionService_CreateSession_CloneOfASourceRemovedMeanwhile(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).
		Return([]model.Session{model.DefaultSessionRecord, sessionRecord(otherSession, "exp", sessionNow)}, nil).Once()
	sessionLocker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSessionFork).Return(grantedLock, nil).Once()
	check := repo.On("LoadSession", mock.Anything, otherSession).Return(model.Session{}, false, nil).Once()
	free := grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	mock.InOrder(lock, check, free)
	svc := NewSessionService(SessionDeps{
		NewSessionLocker: func(model.SessionID) (model.SessionLocker, error) { return sessionLocker, nil },
		Current:          &fakePointer{},
		Sessions:         repo,
		Locker:           locker,
	})

	// Act
	_, err := svc.CreateSession(newLockTestContext(), model.CreateSessionRequest{Name: "feat/x", From: "exp"})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionNotFound)
	repo.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// sweepFailingStore refuses to delete a savepoint's tree.
type sweepFailingStore struct {
	storages.Storager
}

func (s *sweepFailingStore) DeleteAll(context.Context, string) error { return errListStore }

// A rollback that succeeded names the safety snapshot it could not drop, so a caller reading
// only the result learns it is still there.
func TestService_releaseSafetySnapshot(t *testing.T) {
	safetySnapshot := model.Savepoint{ID: "019e843e-1650-7000-8000-0000000000bb"}
	tests := []struct {
		name string
		// clearErr fails the marker's clear, verifyErr the lock's check, deleteErr the uncommit.
		clearErr, verifyErr, deleteErr error
		// failSweep fails the removal of the tree once the record is gone.
		failSweep bool
		wantKept  model.SavepointID
	}{
		{name: "a dropped snapshot is not named"},
		{name: "a marker that cannot be cleared keeps it", clearErr: errStore, wantKept: safetySnapshot.ID},
		{name: "a lost session lock keeps it", verifyErr: model.ErrSessionLockLost, wantKept: safetySnapshot.ID},
		{name: "a record that cannot be uncommitted keeps it", deleteErr: errStore, wantKept: safetySnapshot.ID},
		{name: "an uncommitted snapshot is not named, its remains are gc's", failSweep: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			inflight := &mockInflight{}
			inflight.On("Clear", mock.Anything).Return(tt.clearErr).Once()
			grantedLock := &mockGrantedSessionLock{}
			repo := &mockSavepointRepo{}
			if tt.clearErr == nil {
				grantedLock.On("Verify", mock.Anything).Return(tt.verifyErr).Once()
			}
			if tt.clearErr == nil && tt.verifyErr == nil {
				repo.On("DeleteSavepoint", mock.Anything, safetySnapshot.ID).Return(tt.deleteErr).Once()
			}
			store := newDirStore(t)
			if tt.failSweep {
				store = &sweepFailingStore{Storager: store}
			}
			svc := New(Deps{
				SavepointRepo: repo,
				Inflight:      inflight,
				Store:         store,
			})

			// Act
			kept := svc.releaseSafetySnapshot(newLockTestContext(), grantedLock, safetySnapshot, nil)

			// Assert
			assert.Equal(t, tt.wantKept, kept)
			inflight.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

func TestPreferInterrupt(t *testing.T) {
	tests := []struct {
		name    string
		cause   error
		wantErr error
	}{
		{name: "a running workflow keeps its own failure", wantErr: errWorkflow},
		{name: "a Ctrl-C reports the interrupt", cause: context.Canceled, wantErr: context.Canceled},
		{name: "a lost session lock reports the lost lock", cause: model.ErrSessionLockLost, wantErr: model.ErrSessionLockLost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancelCause(newLockTestContext())
			defer cancel(nil)
			if tt.cause != nil {
				cancel(tt.cause)
			}

			// Act
			err := preferInterrupt(ctx, errWorkflow)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// A rollback aborted before its restore drops its safety snapshot only while the session's lock
// is still its own, and restarts the services either way.
func TestService_abortBeforeRestore(t *testing.T) {
	safetySnapshot := model.Savepoint{ID: "019e843e-1650-7000-8000-0000000000bb"}
	tests := []struct {
		name      string
		verifyErr error
	}{
		{name: "a held lock drops the snapshot"},
		{name: "a lost lock keeps the snapshot", verifyErr: model.ErrSessionLockLost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			grantedLock := &mockGrantedSessionLock{}
			repo := &mockSavepointRepo{}
			runner := &mockComposeRunner{}
			calls := []*mock.Call{grantedLock.On("Verify", mock.Anything).Return(tt.verifyErr).Once()}
			if tt.verifyErr == nil {
				calls = append(calls, repo.On("DeleteSavepoint", mock.Anything, safetySnapshot.ID).Return(nil).Once())
			}
			calls = append(calls, runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(nil).Once())
			mock.InOrder(calls...)
			svc := New(Deps{
				SavepointRepo: repo,
				ComposeRunner: runner,
				Store:         newDirStore(t),
			})
			working := workingSet{
				project:     demoSpec,
				waitTimeout: time.Minute,
			}

			// Act
			svc.abortBeforeRestore(newLockTestContext(), grantedLock, safetySnapshot, working)

			// Assert
			grantedLock.AssertExpectations(t)
			repo.AssertExpectations(t)
			runner.AssertExpectations(t)
		})
	}
}

// A removal checks the session's lock before it uncommits each savepoint: a lost lock removes
// nothing.
func TestService_RemoveSavepoints_LostLockRemovesNothing(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	list := repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(&mockBackend{volume: vol}, nil).Once()
	sessionLocker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSavepointRemove).Return(grantedLock, nil).Once()
	verify := grantedLock.On("Verify", mock.Anything).Return(model.ErrSessionLockLost).Once()
	unlock := grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	mock.InOrder(lock, list, verify, unlock)
	svc := New(Deps{
		ProjectLocker:   locker,
		SessionLocker:   sessionLocker,
		SavepointRepo:   repo,
		BackendRegistry: registry,
		Inflight:        &fakeInflight{},
		Store:           store,
	})

	// Act
	_, err := svc.RemoveSavepoints(newLockTestContext(), model.RemoveRequest{IDs: []model.SavepointID{record.ID}})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	locker.AssertExpectations(t)
}
