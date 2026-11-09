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
					model.RemoveRequest{Refs: []string{"019e843e-1650-7000-8000-000000000001"}})
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
			admission := &mockWriteAdmission{}
			sessionLocker := &mockSessionLocker{}
			repo := &mockSavepointRepo{}
			grantedLock := &mockGrantedSessionLock{}
			calls := []*mock.Call{
				locker.On("Acquire", mock.Anything).Return(nil).Once(),
				admission.On("AdmitWrite", mock.Anything).Return(nil).Once(),
				sessionLocker.On("Lock", mock.Anything, tt.operation).Return(grantedLock, nil).Once(),
			}
			calls = append(calls,
				repo.On("ListSavepoints", mock.Anything).Return(nil, errListStore).Once(),
				grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
				locker.On("Release").Return(nil).Once(),
			)
			mock.InOrder(calls...)
			svc := New(Deps{
				ArcaveProject:  demoArcaveProject,
				ProjectLocker:  locker,
				WriteAdmission: admission,
				SessionLocker:  sessionLocker,
				SavepointRepo:  repo,
				Inflight:       &fakeInflight{},
			})

			// Act
			err := tt.run(svc)

			// Assert
			require.ErrorIs(t, err, errListStore)
			locker.AssertExpectations(t)
			admission.AssertExpectations(t)
			sessionLocker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

func TestService_locked_AWriteRefused(t *testing.T) {
	// Arrange: the admission refuses under the project lock, so the session is never locked.
	locker := &mockLocker{}
	admission := &mockWriteAdmission{}
	sessionLocker := &mockSessionLocker{}
	mock.InOrder(
		locker.On("Acquire", mock.Anything).Return(nil).Once(),
		admission.On("AdmitWrite", mock.Anything).Return(model.ErrStoreHoldsPlainData).Once(),
		locker.On("Release").Return(nil).Once(),
	)
	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		WriteAdmission: admission,
		SessionLocker:  sessionLocker,
		SavepointRepo:  &mockSavepointRepo{},
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.CreateSavepoint(newLockTestContext(), model.CreateSavepointRequest{Name: "sp"})

	// Assert
	require.ErrorIs(t, err, model.ErrStoreHoldsPlainData)
	locker.AssertExpectations(t)
	admission.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
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
		ArcaveProject:    demoArcaveProject,
		ProjectLocker:    locker,
		SessionLocker:    sessionLocker,
		WriteAdmission:   fakeWriteAdmission{},
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
				ArcaveProject: demoArcaveProject,
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
				WriteAdmission:   fakeWriteAdmission{},
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
		WriteAdmission:   fakeWriteAdmission{},
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
		WriteAdmission:   fakeWriteAdmission{},
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

// A rollback aborted before its restore only restarts the services, past the interrupt that
// aborted it: the safety snapshot it took stays, and no session lock is asked.
func TestService_abortBeforeRestore(t *testing.T) {
	// Arrange
	runner := &mockComposeRunner{}
	runner.On("Start", mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil }), demoSpec, time.Minute).
		Return(nil).Once()
	svc := New(Deps{ComposeRunner: runner})
	working := workingSet{
		project:     demoSpec,
		waitTimeout: time.Minute,
	}
	ctx, cancel := context.WithCancel(newLockTestContext())
	cancel()

	// Act
	svc.abortBeforeRestore(ctx, working)

	// Assert
	runner.AssertExpectations(t)
}

// A removal checks the session's lock before it uncommits each savepoint: a lost lock removes
// nothing.
func TestService_RemoveSavepoints_LostLockRemovesNothing(t *testing.T) {
	// Arrange
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	list := repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	sessionLocker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSavepointRemove).Return(grantedLock, nil).Once()
	verify := grantedLock.On("Verify", mock.Anything).Return(model.ErrSessionLockLost).Once()
	unlock := grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	mock.InOrder(lock, list, verify, unlock)
	svc := New(Deps{
		WriteAdmission: fakeWriteAdmission{},
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		SessionLocker:  sessionLocker,
		SavepointRepo:  repo,
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.RemoveSavepoints(newLockTestContext(), model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	repo.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// A workflow that writes the store admits its writes under the project lock, before the first; one
// that does not never asks.
func TestSessionService_WriteAdmission(t *testing.T) {
	tests := []struct {
		name   string
		writes bool
		run    func(svc *SessionService) error
	}{
		{name: "session create", writes: true, run: func(svc *SessionService) error {
			_, err := svc.CreateSession(newLockTestContext(), model.CreateSessionRequest{Name: "exp"})
			return err
		}},
		{name: "session remove", writes: true, run: func(svc *SessionService) error {
			_, err := svc.RemoveSession(newLockTestContext(), model.RemoveSessionRequest{Session: "exp"})
			return err
		}},
		{name: "session list", run: func(svc *SessionService) error {
			_, err := svc.ListSessions(newLockTestContext(), model.ListSessionsRequest{})
			return err
		}},
		{name: "session set", run: func(svc *SessionService) error {
			_, err := svc.SetSession(newLockTestContext(), "exp")
			return err
		}},
		{name: "session unset", run: func(svc *SessionService) error {
			_, err := svc.UnsetSession(newLockTestContext())
			return err
		}},
		{name: "session current", run: func(svc *SessionService) error {
			_, err := svc.ResolveSession(newLockTestContext(), "exp")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: a writer's admission refuses, so it writes nothing; a reader's has no expectation.
			locker := &mockLocker{}
			locker.On("Acquire", mock.Anything).Return(nil).Maybe()
			locker.On("Release").Return(nil).Maybe()
			admission := &mockWriteAdmission{}
			repo := &mockSessionRepo{}
			if tt.writes {
				admission.On("AdmitWrite", mock.Anything).Return(model.ErrStoreHoldsPlainData).Once()
			} else {
				repo.On("ListSessions", mock.Anything).Return([]model.Session(nil), errListStore).Maybe()
			}
			svc := NewSessionService(SessionDeps{
				NewSessionLocker: newFakeSessionLocker,
				Current:          &fakePointer{},
				Sessions:         repo,
				Locker:           locker,
				WriteAdmission:   admission,
				Inflight:         &fakeInflight{},
			})

			// Act
			err := tt.run(svc)

			// Assert
			if tt.writes {
				require.ErrorIs(t, err, model.ErrStoreHoldsPlainData)
			}
			admission.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

func TestSavepointStoreService_ListSavepoints_AdmitsNoWrite(t *testing.T) {
	// Arrange: a admission with no expectation fails on any call.
	admission := &mockWriteAdmission{}
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{}, nil).Once()
	svc := NewSavepointStoreService(SavepointStoreDeps{SavepointRepo: repo, WriteAdmission: admission})

	// Act
	_, err := svc.ListSavepoints(newLockTestContext(), model.ListRequest{Sort: model.SortDesc})

	// Assert
	require.NoError(t, err)
	admission.AssertExpectations(t)
	repo.AssertExpectations(t)
}
