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

package model

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OperationID names one run of an operation holding a session lock: a canonical UUIDv7.
type OperationID string

// NewOperationID returns a fresh UUIDv7.
func NewOperationID() OperationID {
	return OperationID(uuid.Must(uuid.NewV7()).String())
}

// String renders the id.
func (m OperationID) String() string { return string(m) }

// SessionLockOperation names the operation holding a session lock.
type SessionLockOperation string

const (
	SessionLockOperationSavepointCreate SessionLockOperation = "savepoint-create"
	SessionLockOperationSavepointRemove SessionLockOperation = "savepoint-remove"
	SessionLockOperationRollback        SessionLockOperation = "rollback"
	SessionLockOperationSessionFork     SessionLockOperation = "session-fork"
	SessionLockOperationSessionRemove   SessionLockOperation = "session-remove"
	SessionLockOperationGarbageCollect  SessionLockOperation = "gc"
)

// SessionLockRecord is what a session's lock file says: the operation holding the lock and the
// host it runs on. Revision grows with every renewal; a lock whose revision stands still is dead.
type SessionLockRecord struct {
	OperationID   OperationID          `json:"op_id"`
	Operation     SessionLockOperation `json:"operation"`
	Host          string               `json:"host"`
	PID           int                  `json:"pid"`
	ArcaveVersion string               `json:"arcave_version"`
	AcquiredAt    time.Time            `json:"acquired_at"`
	Revision      int64                `json:"revision"`
}

// SessionLocker takes a session's lock in the store, which every host sharing the store sees. A
// model type because sessionlock implements it and core and gc hold it.
type SessionLocker interface {
	// Lock takes the session's lock and returns ctx cancelled with ErrSessionLockLost once it is
	// lost, ended by Unlock: run the operation under it, so the writes no Verify gates stop
	// before another host can take the lock over.
	Lock(ctx context.Context, operation SessionLockOperation) (context.Context, GrantedSessionLock, error)
}

// GrantedSessionLock is a session's lock SessionLocker granted one operation: its lifecycle until
// the operation unlocks it.
type GrantedSessionLock interface {
	// Verify refuses once the store no longer names this operation; called before a commit.
	Verify(ctx context.Context) error
	Unlock(ctx context.Context) error
}

// SessionLockedError reports a session another operation holds the lock of.
type SessionLockedError struct {
	Session SessionID
	Holder  SessionLockRecord
}

func (m *SessionLockedError) Error() string {
	return fmt.Sprintf("session %s is locked by %s %s on %s (pid %d) since %s",
		m.Session, m.Holder.Operation, m.Holder.OperationID, m.Holder.Host, m.Holder.PID,
		m.Holder.AcquiredAt.Format(time.RFC3339))
}

// Is lets errors.Is match ErrSessionLocked.
func (m *SessionLockedError) Is(target error) bool { return target == ErrSessionLocked }
