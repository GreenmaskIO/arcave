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

package workflow

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

var demoSession = model.SessionID(uuid.MustParse("0f9c2f4a-6d3e-4b6f-9a2c-1d5e8b7a4c31"))

// mockSessionLookup doubles sessionLookup.
type mockSessionLookup struct{ mock.Mock }

func (m *mockSessionLookup) LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(model.Session), args.Bool(1), args.Error(2)
}

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
