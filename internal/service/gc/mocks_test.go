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

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockSessions struct{ mock.Mock }

func (m *mockSessions) LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(model.Session), args.Bool(1), args.Error(2)
}

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

// mockSessionLocker doubles model.SessionLocker; Lock hands out the *mockGrantedSessionLock it
// returns, with the caller's context.
type mockSessionLocker struct{ mock.Mock }

func (m *mockSessionLocker) Lock(
	ctx context.Context, operation model.SessionLockOperation,
) (context.Context, model.GrantedSessionLock, error) {
	args := m.Called(ctx, operation)
	if err := args.Error(1); err != nil {
		return nil, nil, err
	}
	return ctx, args.Get(0).(*mockGrantedSessionLock), nil
}

// mockGrantedSessionLock doubles model.GrantedSessionLock.
type mockGrantedSessionLock struct{ mock.Mock }

func (m *mockGrantedSessionLock) Verify(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockGrantedSessionLock) Unlock(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// fakeSessionLocker always grants the session's lock, so tests that are not about it carry no
// expectations.
type fakeSessionLocker struct{}

func (fakeSessionLocker) Lock(
	ctx context.Context, _ model.SessionLockOperation,
) (context.Context, model.GrantedSessionLock, error) {
	return ctx, fakeGrantedSessionLock{}, nil
}

// fakeGrantedSessionLock is a session lock that always holds.
type fakeGrantedSessionLock struct{}

func (fakeGrantedSessionLock) Verify(context.Context) error { return nil }
func (fakeGrantedSessionLock) Unlock(context.Context) error { return nil }

type mockInflight struct{ mock.Mock }

func (m *mockInflight) Get(ctx context.Context) (model.InflightMarker, bool, error) {
	args := m.Called(ctx)
	marker, _ := args.Get(0).(model.InflightMarker)
	return marker, args.Bool(1), args.Error(2)
}

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

type mockGarbage struct{ mock.Mock }

func (m *mockGarbage) Report() model.GarbageReport {
	return m.Called().Get(0).(model.GarbageReport)
}

func (m *mockGarbage) Delete(ctx context.Context) model.GarbageDeletion {
	return m.Called(ctx).Get(0).(model.GarbageDeletion)
}

type mockSavepoints struct{ mock.Mock }

func (m *mockSavepoints) List(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	savepoints, _ := args.Get(0).([]model.Savepoint)
	return savepoints, args.Error(1)
}

func (m *mockSavepoints) Remove(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}
