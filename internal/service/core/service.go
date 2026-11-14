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
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/rs/zerolog"
)

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// writeAdmission admits a workflow's writes to the arcave project's store, under the project lock
// and before the first: an encrypted one is marked for its key then.
type writeAdmission interface {
	AdmitWrite(ctx context.Context) error
}

// sessionLookup finds a session's record; core re-checks it under the lock.
type sessionLookup interface {
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
}

// inflightMarkers is the intent marker's seam: written before a rollback's first
// destructive write, cleared when the environment is consistent again.
type inflightMarkers interface {
	Get(ctx context.Context) (marker model.InflightMarker, found bool, err error)
	Put(ctx context.Context, marker model.InflightMarker) error
	Clear(ctx context.Context) error
}

// Deps are Service's collaborators. A struct rather than a parameter list: the
// seams have no natural order, so a new one could silently swap with an existing one.
type Deps struct {
	// Savepoints are the compose project's savepoints, as the provider keeps them.
	Savepoints    provider.Savepoints
	ProjectLocker projectLocker
	// WriteAdmission admits a workflow's writes to the arcave project's store, before the first.
	WriteAdmission writeAdmission
	// SessionLocker holds Session's lock in the store for the whole of a workflow.
	SessionLocker model.SessionLocker
	// Workload is the compose project the workflows act on, through the provider's stages.
	Workload provider.Workload
	Inflight inflightMarkers
	// ArcaveProject is the arcave project the workload belongs to, which a rollback's deployment
	// labels name.
	ArcaveProject model.ArcaveProjectName
	// Session is the session the workload belongs to; zero means model.DefaultSession.
	Session model.SessionID
	// ComposeProject is the compose project the workload is.
	ComposeProject model.DockerComposeProjectName
	// Sessions checks, under the lock, that Session still exists.
	Sessions sessionLookup
}

// Service orchestrates arcave's savepoint workflows over one workload; a capture and a rollback
// read savepoints through the embedded SavepointStoreService.
type Service struct {
	*SavepointStoreService
	workload      provider.Workload
	arcaveProject model.ArcaveProjectName
}

func New(deps Deps) *Service {
	return &Service{
		SavepointStoreService: NewSavepointStoreService(SavepointStoreDeps{
			Savepoints:     deps.Savepoints,
			ProjectLocker:  deps.ProjectLocker,
			WriteAdmission: deps.WriteAdmission,
			SessionLocker:  deps.SessionLocker,
			Inflight:       deps.Inflight,
			Session:        deps.Session,
			ComposeProject: deps.ComposeProject,
			Sessions:       deps.Sessions,
		}),
		workload:      deps.Workload,
		arcaveProject: deps.ArcaveProject,
	}
}

// withSessionLock runs fn holding the session's lock in the store, and unlocks it however fn
// ends; a failed unlock is logged. fn runs on the lock's context: a lost lock cancels it with
// model.ErrSessionLockLost as its cause, which fn reports as any interrupt.
func withSessionLock[T any](
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

// withUnlock runs fn with the project lock held, and releases it however fn ends.
// Releasing is best-effort: a failed release is logged, never returned.
//
// A function rather than a method, because Go has no generic methods: every
// workflow returns its own result type, and the lock must not decide it.
func withUnlock[T any](ctx context.Context, locker projectLocker, fn func() (T, error)) (T, error) {
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

// withWriteUnlock is withUnlock for a workflow that writes the store: its writes are admitted
// under the lock, before fn's first.
func withWriteUnlock[T any](
	ctx context.Context, locker projectLocker, admission writeAdmission, fn func() (T, error),
) (T, error) {
	return withUnlock(ctx, locker, func() (T, error) {
		if err := admission.AdmitWrite(ctx); err != nil {
			var zero T
			return zero, fmt.Errorf("admit the write: %w", err)
		}
		return fn()
	})
}

// readSessionRef names session id as its record does, refusing one other than the default that
// has no record: one resolved outside the lock may have been removed since.
func readSessionRef(ctx context.Context, sessions sessionLookup, id model.SessionID) (model.SessionRef, error) {
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
