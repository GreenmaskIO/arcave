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
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// SavepointReader reads the committed savepoints of one compose project's store.
type SavepointReader interface {
	LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]model.Savepoint, error)
}

// sessionLookup reads a session's record; the default session always has one.
type sessionLookup interface {
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
}

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// inflightMarkers reads the interrupted-rollback marker.
type inflightMarkers interface {
	Get(ctx context.Context) (marker model.InflightMarker, found bool, err error)
}

// Deps are GarbageCollector's collaborators.
type Deps struct {
	// Store is the arcave project's store, which every compose project's scope is opened in.
	Store storages.Navigator
	// Session is the session the gc looks at; zero means model.DefaultSession.
	Session  model.SessionID
	Sessions sessionLookup
	// NewSavepoints opens the savepoints of a compose project's store.
	NewSavepoints func(store storages.Storager) SavepointReader
	Locker        projectLocker
	// SessionLocker holds Session's lock in the store for the whole run.
	SessionLocker model.SessionLocker
	Inflight      inflightMarkers
	// DaemonCollectors and ComposeProjectCollectors are one per kind, in the order they run.
	DaemonCollectors         []DaemonCollector
	ComposeProjectCollectors []ComposeProjectCollector
	// RestoredLayerGrace is the restored layer grace of a request naming none.
	RestoredLayerGrace time.Duration
	// UnlistedNamespaces are the top-level directories of a compose project store its scope's
	// listing leaves out: a backend's own, which its collector reads through the backend.
	UnlistedNamespaces []string
	// Now is the clock a scope is stamped with; nil is the wall clock.
	Now func() time.Time
}

// GarbageCollector finds one session's garbage. It deletes nothing yet.
type GarbageCollector struct {
	store                    storages.Navigator
	session                  model.SessionID
	sessions                 sessionLookup
	newSavepoints            func(store storages.Storager) SavepointReader
	locker                   projectLocker
	sessionLocker            model.SessionLocker
	inflight                 inflightMarkers
	daemonCollectors         []DaemonCollector
	composeProjectCollectors []ComposeProjectCollector
	unlisted                 []string
	grace                    time.Duration
	now                      func() time.Time
}

// New builds a GarbageCollector over deps.
func New(deps Deps) *GarbageCollector {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().Round(0) }
	}
	return &GarbageCollector{
		store:                    deps.Store,
		session:                  cmp.Or(deps.Session, model.DefaultSession),
		sessions:                 deps.Sessions,
		newSavepoints:            deps.NewSavepoints,
		locker:                   deps.Locker,
		sessionLocker:            deps.SessionLocker,
		inflight:                 deps.Inflight,
		daemonCollectors:         deps.DaemonCollectors,
		composeProjectCollectors: deps.ComposeProjectCollectors,
		unlisted:                 deps.UnlistedNamespaces,
		grace:                    deps.RestoredLayerGrace,
		now:                      now,
	}
}

// GarbageCollect reports what every collector req selects finds in the session; only a dry
// run exists. It holds the project and session locks throughout, so no capture runs meanwhile.
func (s *GarbageCollector) GarbageCollect(
	ctx context.Context, req model.GarbageCollectRequest,
) (model.GarbageCollectResult, error) {
	if !req.DryRun {
		return model.GarbageCollectResult{}, model.ErrGarbageCollectNotImplemented
	}
	if err := req.Validate(); err != nil {
		return model.GarbageCollectResult{}, err
	}
	collectors := s.readCollectorSet(ctx, req.DataKinds)
	return withUnlock(ctx, s.locker, func() (model.GarbageCollectResult, error) {
		return withSessionLock(ctx, s.sessionLocker, func(ctx context.Context) (model.GarbageCollectResult, error) {
			// Resolved outside the lock: a session removed since has no garbage, only a wrong answer.
			if err := requireSession(ctx, s.sessions, s.session); err != nil {
				return model.GarbageCollectResult{}, err
			}
			return s.findGarbage(ctx, req, collectors)
		})
	})
}

// withUnlock runs fn under the project lock, released when fn returns.
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

// withSessionLock runs fn holding the session's lock in the store, and unlocks it however fn
// ends; a failed unlock is logged. fn runs on the lock's context: a lost lock cancels it with
// model.ErrSessionLockLost as its cause, and a run so cancelled returns that cause, not its result.
func withSessionLock[T any](
	ctx context.Context, locker model.SessionLocker, fn func(ctx context.Context) (T, error),
) (T, error) {
	lockCtx, grantedLock, err := locker.Lock(ctx, model.SessionLockOperationGarbageCollect)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("lock session: %w", err)
	}
	defer func() {
		if err := grantedLock.Unlock(ctx); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("unlock session")
		}
	}()
	result, err := fn(lockCtx)
	// What a run judged past a lost lock or an interrupt is no verdict.
	if cause := context.Cause(lockCtx); cause != nil {
		var zero T
		return zero, cause
	}
	return result, err
}

// requireSession refuses a session other than the default that has no record: one removed
// since it was resolved.
func requireSession(ctx context.Context, sessions sessionLookup, id model.SessionID) error {
	if id == model.DefaultSession {
		return nil
	}
	_, found, err := sessions.LoadSession(ctx, id)
	if err != nil {
		return fmt.Errorf("checking session %s: %w", id, err)
	}
	if !found {
		return fmt.Errorf("%w: %s", model.ErrSessionNotFound, id)
	}
	return nil
}

// findGarbage runs the daemon collectors of the set, then its compose-project collectors over
// every compose project of the session.
func (s *GarbageCollector) findGarbage(
	ctx context.Context, req model.GarbageCollectRequest, collectors collectorSet,
) (model.GarbageCollectResult, error) {
	var (
		warnings = append(slices.Clone(collectors.warnings), s.readInflightWarnings(ctx)...)
		grace    = s.grace
		reports  = []model.GarbageReport{}
	)
	if req.RestoredLayerGrace != nil {
		grace = *req.RestoredLayerGrace
	}
	sessionStore, composeProjects, listErr := s.readComposeProjects(ctx)
	daemonScope := DaemonScope{
		ComposeProjects:    composeProjects,
		Now:                s.now(),
		RestoredLayerGrace: grace,
	}
	// The daemon's objects are judged against the compose projects: none read is no verdict.
	reports = append(reports, collectors.runDaemonCollectors(ctx, daemonScope, listErr)...)
	reports = append(reports, s.runComposeProjects(ctx, collectors, sessionStore, composeProjects, listErr)...)

	result := model.GarbageCollectResult{
		DryRun:     true,
		Session:    s.session,
		Reports:    reports,
		Savepoints: newDeadSavepoints(reports),
		Total:      newGarbageTotal(reports),
		Warnings:   warnings,
	}
	if result.Total.Failed > 0 || result.Total.Aborted > 0 || result.Total.Unjudged > 0 {
		return result, model.ErrGarbageCollectPartial
	}
	return result, nil
}

// readInflightWarnings says when an interrupted rollback stands: its savepoints are committed,
// so they stay live, but what it was writing is not settled.
func (s *GarbageCollector) readInflightWarnings(ctx context.Context) []string {
	marker, found, err := s.inflight.Get(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("reading the inflight marker")
		return []string{fmt.Sprintf("the inflight marker cannot be read: %v", err)}
	}
	if !found {
		return nil
	}
	return []string{fmt.Sprintf("an interrupted rollback of compose project %s in session %s to savepoint %s stands",
		marker.ComposeProject, marker.Session, marker.Target)}
}

// runComposeProjects runs the compose-project collectors over every compose project of the
// session; a listing that failed with listErr aborts them.
func (s *GarbageCollector) runComposeProjects(
	ctx context.Context, collectors collectorSet, sessionStore storages.Navigator, names []model.DockerComposeProjectName,
	listErr error,
) []model.GarbageReport {
	if len(collectors.composeProject) == 0 {
		// No compose-project collector runs: reading the scopes would only hold the lock longer.
		return nil
	}
	if listErr != nil {
		return collectors.runComposeProjectCollectors(ctx, "", ComposeProjectScope{}, listErr)
	}
	var reports []model.GarbageReport
	for _, name := range names {
		scope, err := s.readComposeProjectScope(ctx, sessionStore, name)
		reports = append(reports, collectors.runComposeProjectCollectors(ctx, name, scope, err)...)
	}
	return reports
}
