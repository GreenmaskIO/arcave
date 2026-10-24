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
	"sync"
	"sync/atomic"

	"github.com/greenmaskio/arcave/internal/model"
)

// GrantedLock is a session's lock the Locker granted one operation, renewed until it is unlocked.
type GrantedLock struct {
	locker   *Locker
	opID     model.OperationID
	cancel   context.CancelCauseFunc
	quit     chan struct{}
	done     chan struct{}
	unlocked atomic.Bool

	mu        sync.Mutex
	lock      model.SessionLockRecord
	renewedAt renewal
}

// Verify refuses with model.ErrSessionLockLost when the store no longer holds this lock, or its
// last renewal is too old to vouch for it: what an operation calls before it commits.
func (g *GrantedLock) Verify(ctx context.Context) error { return g.locker.verify(ctx, g) }

// Unlock removes the lock and stops renewing it; a lock already unlocked, too old to vouch for, or
// one the store no longer names this lock's operation in is model.ErrSessionLockNotHeld.
func (g *GrantedLock) Unlock(ctx context.Context) error { return g.locker.unlock(ctx, g) }
