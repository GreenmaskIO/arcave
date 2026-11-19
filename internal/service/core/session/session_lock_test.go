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

package session

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

var errListStore = errors.New("store unreachable")

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
			svc := New(Deps{
				WriteAdmission:   fakeWriteAdmission{},
				NewSessionLocker: func(model.SessionID) (model.SessionLocker, error) { return sessionLocker, nil },
				Current:          &fakePointer{},
				Sessions:         repo,
				Locker:           locker,
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
	svc := New(Deps{
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
	svc := New(Deps{
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

// A workflow that writes the store admits its writes under the project lock, before the first; one
// that does not never asks.
func TestSessionService_withWriteUnlock(t *testing.T) {
	tests := []struct {
		name   string
		writes bool
		run    func(svc *Service) error
	}{
		{name: "session create", writes: true, run: func(svc *Service) error {
			_, err := svc.CreateSession(newLockTestContext(), model.CreateSessionRequest{Name: "exp"})
			return err
		}},
		{name: "session remove", writes: true, run: func(svc *Service) error {
			_, err := svc.RemoveSession(newLockTestContext(), model.RemoveSessionRequest{Session: "exp"})
			return err
		}},
		{name: "session list", run: func(svc *Service) error {
			_, err := svc.ListSessions(newLockTestContext(), model.ListSessionsRequest{})
			return err
		}},
		{name: "session set", run: func(svc *Service) error {
			_, err := svc.SetSession(newLockTestContext(), "exp")
			return err
		}},
		{name: "session unset", run: func(svc *Service) error {
			_, err := svc.UnsetSession(newLockTestContext())
			return err
		}},
		{name: "session current", run: func(svc *Service) error {
			_, err := svc.ResolveSession(newLockTestContext(), "exp")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: a writer's admission refuses, so it writes nothing; a reader's has no expectation.
			locker := &mockLocker{}
			admission := &mockWriteAdmission{}
			repo := &mockSessionRepo{}
			if tt.writes {
				mock.InOrder(
					locker.On("Acquire", mock.Anything).Return(nil).Once(),
					admission.On("AdmitWrite", mock.Anything).Return(model.ErrStoreHoldsPlainData).Once(),
					locker.On("Release").Return(nil).Once(),
				)
			} else {
				// A reader may take the project lock (set, unset); it never asks for an admission.
				locker.On("Acquire", mock.Anything).Return(nil).Maybe()
				locker.On("Release").Return(nil).Maybe()
				repo.On("ListSessions", mock.Anything).Return([]model.Session(nil), errListStore).Maybe()
			}
			svc := New(Deps{
				NewSessionLocker: newFakeSessionLocker,
				Current:          &fakePointer{},
				Sessions:         repo,
				Locker:           locker,
				WriteAdmission:   admission,
			})

			// Act
			err := tt.run(svc)

			// Assert
			if tt.writes {
				require.ErrorIs(t, err, model.ErrStoreHoldsPlainData)
			}
			admission.AssertExpectations(t)
			locker.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}
