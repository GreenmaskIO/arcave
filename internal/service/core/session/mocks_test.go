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
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

var demoSession = model.SessionID(uuid.MustParse("0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"))

// mockLocker doubles projectLocker. Release is a closure Acquire returns rather
// than a method of the seam, so it reports itself as one — MethodCalled puts it
// where testify counts it and mock.InOrder can place it.
type mockLocker struct{ mock.Mock }

func (m *mockLocker) Acquire(ctx context.Context) (func() error, error) {
	if err := m.Called(ctx).Error(0); err != nil {
		return nil, err
	}
	return func() error { return m.MethodCalled("Release").Error(0) }, nil
}

// fakeWriteAdmission admits every write, for tests that are not about the admission.
type fakeWriteAdmission struct{}

func (fakeWriteAdmission) AdmitWrite(context.Context) error { return nil }

// mockWriteAdmission doubles writeAdmission.
type mockWriteAdmission struct{ mock.Mock }

func (m *mockWriteAdmission) AdmitWrite(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// fakeSessionLocker always grants the session's lock, and the lock always holds, so tests that
// are not about it carry no expectations.
type fakeSessionLocker struct{}

func (fakeSessionLocker) Lock(
	ctx context.Context, _ model.SessionLockOperation,
) (context.Context, model.GrantedSessionLock, error) {
	return ctx, fakeGrantedSessionLock{}, nil
}

// newFakeSessionLocker opens a fakeSessionLocker for any session.
func newFakeSessionLocker(model.SessionID) (model.SessionLocker, error) {
	return fakeSessionLocker{}, nil
}

// fakeGrantedSessionLock is a session lock that always holds.
type fakeGrantedSessionLock struct{}

func (fakeGrantedSessionLock) Verify(context.Context) error { return nil }

func (fakeGrantedSessionLock) Unlock(context.Context) error { return nil }

// mockSessionLocker doubles model.SessionLocker; Lock hands out the *mockGrantedSessionLock it
// returns, with the caller's context, cancelled with the lock's lostCause when the test sets one.
type mockSessionLocker struct{ mock.Mock }

func (m *mockSessionLocker) Lock(
	ctx context.Context, operation model.SessionLockOperation,
) (context.Context, model.GrantedSessionLock, error) {
	args := m.Called(ctx, operation)
	if err := args.Error(1); err != nil {
		return nil, nil, err
	}
	grantedLock := args.Get(0).(*mockGrantedSessionLock)
	if grantedLock.lostCause == nil {
		return ctx, grantedLock, nil
	}
	lockCtx, cancel := context.WithCancelCause(ctx)
	cancel(grantedLock.lostCause)
	return lockCtx, grantedLock, nil
}

// mockGrantedSessionLock doubles model.GrantedSessionLock; lostCause, when set, is the cause the
// context Lock returned with it is cancelled with.
type mockGrantedSessionLock struct {
	mock.Mock
	lostCause error
}

func (m *mockGrantedSessionLock) Verify(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockGrantedSessionLock) Unlock(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// fakePointer is a stateful stand-in for .arcave/current_session; the zero value holds none.
type fakePointer struct {
	mu       sync.Mutex
	id       *model.SessionID
	err      error // returned by every call when set
	clearErr error // returned by Clear alone when set
}

func (f *fakePointer) Get(_ context.Context) (model.SessionID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return model.SessionID{}, false, f.err
	}
	if f.id == nil {
		return model.SessionID{}, false, nil
	}
	return *f.id, true, nil
}

func (f *fakePointer) Put(_ context.Context, id model.SessionID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.id = &id
	return nil
}

func (f *fakePointer) Clear(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.clearErr != nil {
		return f.clearErr
	}
	f.id = nil
	return nil
}

// pointerAt is a fakePointer holding id.
func pointerAt(id model.SessionID) *fakePointer {
	return &fakePointer{id: &id}
}

// mockSession doubles provider.Session.
type mockSession struct {
	mock.Mock
	// id answers ID unmocked.
	id model.SessionID
}

func (m *mockSession) ID() model.SessionID { return m.id }

func (m *mockSession) Workloads(ctx context.Context) ([]provider.WorkloadName, error) {
	args := m.Called(ctx)
	workloads, _ := args.Get(0).([]provider.WorkloadName)
	return workloads, args.Error(1)
}

func (m *mockSession) Savepoints(workload provider.WorkloadName) (provider.Savepoints, error) {
	args := m.Called(workload)
	savepoints, _ := args.Get(0).(provider.Savepoints)
	return savepoints, args.Error(1)
}

func (m *mockSession) FindGarbage(ctx context.Context, dataKinds []model.GarbageDataKind) []provider.Garbage {
	found, _ := m.Called(ctx, dataKinds).Get(0).([]provider.Garbage)
	return found
}

// mockSavepoints doubles provider.Savepoints.
type mockSavepoints struct{ mock.Mock }

func (m *mockSavepoints) List(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	savepoints, _ := args.Get(0).([]model.Savepoint)
	return savepoints, args.Error(1)
}

func (m *mockSavepoints) Load(ctx context.Context, id model.SavepointID) (model.Savepoint, bool, error) {
	args := m.Called(ctx, id)
	savepoint, _ := args.Get(0).(model.Savepoint)
	return savepoint, args.Bool(1), args.Error(2)
}

func (m *mockSavepoints) Remove(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}

// expectLock declares a free lock and the release that always follows it —
// withUnlock defers the release, so a taken lock is a released one.
func expectLock(locker *mockLocker) *mock.Call {
	locker.On("Release").Return(nil).Once()
	return locker.On("Acquire", mock.Anything).Return(nil).Once()
}

func newLockTestContext() context.Context {
	return zerolog.Nop().WithContext(context.Background())
}
