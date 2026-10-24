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

package sessionlock

import (
	"context"
	"errors"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	// ErrClaimLost reports a claim, rewrite or removal the store no longer allows: another
	// operation holds the lock, or won the claim.
	ErrClaimLost = errors.New("session lock claim lost")
	// ErrClaimSlow reports a claim that could not be made safely in time and is worth retrying.
	ErrClaimSlow = errors.New("session lock claim too slow")
)

// SessionLockStore keeps one session's lock record and gives a contended write one winner: the
// mechanism of exclusion, apart from waiting for, renewing and unlocking a lock, which is Locker's.
type SessionLockStore interface {
	// Read returns the stored lock; one that does not decode is found with a zero record.
	Read(ctx context.Context) (lock model.SessionLockRecord, found bool, err error)
	// Claim writes lock only while the store still holds expected's holder at its revision (the
	// zero record: none, or one that does not decode) and no concurrent claim wins; a claim that
	// fails may have landed all the same, and is the caller's to Remove.
	Claim(ctx context.Context, lock model.SessionLockRecord, expected model.SessionLockRecord) error
	// Rewrite replaces the lock only while the store still names lock.OperationID.
	Rewrite(ctx context.Context, lock model.SessionLockRecord) error
	// Remove deletes the lock only while the store still names opID.
	Remove(ctx context.Context, opID model.OperationID) error
}
