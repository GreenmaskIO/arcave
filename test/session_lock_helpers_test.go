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

package test

import (
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/service/sessionlock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// testSessionLockTiming is the production timing with a short poll; testSessionLockSettle a
// short settle, so a test pays little for every workflow's lock.
var testSessionLockTiming = sessionlock.Timing{
	TTL:   sessionlock.DefaultTiming.TTL,
	Renew: sessionlock.DefaultTiming.Renew,
	Wait:  sessionlock.DefaultTiming.Wait,
	Poll:  100 * time.Millisecond,
}

const testSessionLockSettle = 50 * time.Millisecond

// newSessionLocker is session's lock in the arcave project store, where the DI root keeps it.
func newSessionLocker(t *testing.T, arcaveProjectStore storages.Navigator, session model.SessionID) *sessionlock.Locker {
	t.Helper()
	locker, err := openSessionLocker(arcaveProjectStore, session)
	require.NoError(t, err)
	return locker
}

// newStoreLocker is a session lock kept at the root of store, for a test that holds no arcave
// project store: only the workflows of the test itself contend for it.
func newStoreLocker(store storages.Storager, session model.SessionID) *sessionlock.Locker {
	return newTestLocker(store, session.String()+".lock.json", session)
}

func openSessionLocker(arcaveProjectStore storages.Navigator, session model.SessionID) (*sessionlock.Locker, error) {
	store, key, err := storefactory.SessionLock(arcaveProjectStore, session)
	if err != nil {
		return nil, err
	}
	return newTestLocker(store, key, session), nil
}

// newTestLocker is session's lock at key of store, through the lock store a command builds.
func newTestLocker(store storages.Storager, key string, session model.SessionID) *sessionlock.Locker {
	lockStore := sessionlock.NewFischerSessionLockStore(store, key).SetSettle(testSessionLockSettle)
	return sessionlock.New(lockStore, session, heartbeat.RunnerInfo{Host: "test"}).SetTiming(testSessionLockTiming)
}

// newSessionLockerFactory opens the lock of any session of the arcave project store.
func newSessionLockerFactory(arcaveProjectStore storages.Navigator) func(id model.SessionID) (model.SessionLocker, error) {
	return func(id model.SessionID) (model.SessionLocker, error) {
		return openSessionLocker(arcaveProjectStore, id)
	}
}
