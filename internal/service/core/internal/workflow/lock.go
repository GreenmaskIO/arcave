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

// Package workflow is what every workflow of core's tree runs under: the project lock, the write's
// admission, the session's lock, and what an interrupt tells the user.
package workflow

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// kindSessionLock is the step of the wait for the session's lock.
const kindSessionLock progress.Kind = "session.lock"

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// writeAdmission admits a workflow's writes to the arcave project's store, under the project lock
// and before the first: an encrypted one is marked for its key then.
type writeAdmission interface {
	AdmitWrite(ctx context.Context) error
}

// sessionLookup finds a session's record; the workflow re-checks it under the lock.
type sessionLookup interface {
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
}

// WithSessionLock runs fn holding the session's lock in the store, and unlocks it however fn
// ends; a failed unlock is logged. fn runs on the lock's context: a lost lock cancels it with
// model.ErrSessionLockLost as its cause, which fn reports as any interrupt.
func WithSessionLock[T any](
	ctx context.Context,
	locker model.SessionLocker,
	operation model.SessionLockOperation,
	fn func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error),
) (T, error) {
	// The wait is a step of its own, shown only once another holds the lock; the workflow goes on
	// under the step ctx carries.
	waitCtx, wait := progress.Begin(ctx, progress.Step{Kind: kindSessionLock})
	lockCtx, grantedLock, err := locker.Lock(waitCtx, operation)
	wait.End(err)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("lock session: %w", err)
	}
	defer func() {
		if err := grantedLock.Unlock(ctx); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("unlock session")
		}
	}()
	return fn(progress.WithStep(lockCtx, progress.Current(ctx)), grantedLock)
}

// WithUnlock runs fn with the project lock held, and releases it however fn ends.
// Releasing is best-effort: a failed release is logged, never returned.
//
// A function rather than a method, because Go has no generic methods: every
// workflow returns its own result type, and the lock must not decide it.
func WithUnlock[T any](ctx context.Context, locker projectLocker, fn func() (T, error)) (T, error) {
	release, err := locker.Acquire(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() {
		if err := release(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("release project lock")
		}
	}()
	return fn()
}

// WithWriteUnlock is WithUnlock for a workflow that writes the store: its writes are admitted
// under the lock, before fn's first.
func WithWriteUnlock[T any](
	ctx context.Context, locker projectLocker, admission writeAdmission, fn func() (T, error),
) (T, error) {
	return WithUnlock(ctx, locker, func() (T, error) {
		if err := admission.AdmitWrite(ctx); err != nil {
			var zero T
			return zero, fmt.Errorf("admit the write: %w", err)
		}
		return fn()
	})
}

// ReadSessionRef names session id as its record does, refusing one other than the default that
// has no record: one resolved outside the lock may have been removed since.
func ReadSessionRef(ctx context.Context, sessions sessionLookup, id model.SessionID) (model.SessionRef, error) {
	if id == model.DefaultSession {
		return model.SessionRef{
			ID:   model.DefaultSession,
			Name: model.DefaultSessionName,
		}, nil
	}
	session, found, err := sessions.LoadSession(ctx, id)
	if err != nil {
		return model.SessionRef{}, fmt.Errorf("checking session %s: %w", id, err)
	}
	if !found {
		return model.SessionRef{}, fmt.Errorf("%w: %s", model.ErrSessionNotFound, id)
	}
	return model.SessionRef{
		ID:   session.ID,
		Name: session.Name,
	}, nil
}
