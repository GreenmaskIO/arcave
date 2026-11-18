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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

var (
	errWorkflow  = errors.New("workflow failed")
	errListStore = errors.New("store unreachable")
)

func newLockTestContext() context.Context {
	return zerolog.Nop().WithContext(context.Background())
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
			repo := &mockSavepoints{}
			grantedLock := &mockGrantedSessionLock{}
			calls := []*mock.Call{
				locker.On("Acquire", mock.Anything).Return(nil).Once(),
				admission.On("AdmitWrite", mock.Anything).Return(nil).Once(),
				sessionLocker.On("Lock", mock.Anything, tt.operation).Return(grantedLock, nil).Once(),
			}
			calls = append(calls,
				repo.On("List", mock.Anything).Return(nil, errListStore).Once(),
				grantedLock.On("Unlock", mock.Anything).Return(nil).Once(),
				locker.On("Release").Return(nil).Once(),
			)
			mock.InOrder(calls...)
			svc := New(Deps{
				ArcaveProject:  demoArcaveProject,
				ProjectLocker:  locker,
				WriteAdmission: admission,
				SessionLocker:  sessionLocker,
				Savepoints:     repo,
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
		Savepoints:     &mockSavepoints{},
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
	stages := newMockStages(false)
	svc := New(Deps{
		Store:          newDirStore(t),
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		SessionLocker:  sessionLocker,
		WriteAdmission: fakeWriteAdmission{},
		Workload:       &stages.workload,
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.CreateSavepoint(newLockTestContext(), model.CreateSavepointRequest{Name: "sp"})

	// Assert: another host's operation holds the session; nothing is read or stopped.
	require.ErrorIs(t, err, model.ErrSessionLocked)
	locker.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	stages.AssertExpectations(t)
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

// A rollback aborted before its restore releases what its safety snapshot held on a revertContext:
// the release outlives the interrupt. No session lock is asked.
func TestAbortBeforeRestore(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(newLockTestContext())
	cancel()
	capturePlan := &mockCapturePlan{}
	revertCtx := mock.MatchedBy(func(ctx context.Context) bool {
		_, bounded := ctx.Deadline()
		return ctx.Err() == nil && bounded
	})
	capturePlan.On("Release", revertCtx, time.Minute).Return(nil).Once()

	// Act
	abortBeforeRestore(ctx, capturePlan, time.Minute)

	// Assert
	capturePlan.AssertExpectations(t)
}

// A removal checks the session's lock before it uncommits each savepoint: a lost lock removes
// nothing.
func TestService_RemoveSavepoints_LostLockRemovesNothing(t *testing.T) {
	// Arrange
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepoints{}
	list := repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
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
		Savepoints:     repo,
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

func TestSavepointStoreService_ListSavepoints_AdmitsNoWrite(t *testing.T) {
	// Arrange: an admission with no expectation fails on any call.
	admission := &mockWriteAdmission{}
	repo := &mockSavepoints{}
	repo.On("List", mock.Anything).Return([]model.Savepoint{}, nil).Once()
	svc := NewSavepointStoreService(SavepointStoreDeps{Savepoints: repo, WriteAdmission: admission})

	// Act
	_, err := svc.ListSavepoints(newLockTestContext(), model.ListRequest{Sort: model.SortDesc})

	// Assert
	require.NoError(t, err)
	admission.AssertExpectations(t)
	repo.AssertExpectations(t)
}
