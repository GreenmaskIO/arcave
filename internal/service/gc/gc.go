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
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
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
	// DaemonErasers and ComposeProjectErasers are one per action a gc carries out at their level;
	// an action none carries out is only reported.
	DaemonErasers         []DaemonEraser
	ComposeProjectErasers []ComposeProjectEraser
	// RestoredLayerGrace is the restored layer grace of a request naming none.
	RestoredLayerGrace time.Duration
	// UnlistedNamespaces are the top-level directories of a compose project store its scope's
	// listing leaves out: a backend's own, which its collector reads through the backend.
	UnlistedNamespaces []string
	// Now is the clock a scope is stamped with; nil is the wall clock.
	Now func() time.Time
}

// GarbageCollector finds one session's garbage, and deletes what its erasers can.
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
	erasers                  eraserSet
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
		erasers:                  newEraserSet(deps.DaemonErasers, deps.ComposeProjectErasers),
		unlisted:                 deps.UnlistedNamespaces,
		grace:                    deps.RestoredLayerGrace,
		now:                      now,
	}
}

// GarbageCollect finds what every collector req selects finds in the session and, past a dry run,
// deletes it. It holds the project and session locks throughout, so no capture runs meanwhile.
func (s *GarbageCollector) GarbageCollect(
	ctx context.Context, req model.GarbageCollectRequest,
) (_ model.GarbageCollectResult, err error) {
	// Outside the locks: a lost lock cancels the work under it, and the root says why.
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindGC})
	defer func() { step.End(err) }()
	if err := req.Validate(); err != nil {
		return model.GarbageCollectResult{}, err
	}
	collectors := s.readCollectorSet(ctx, req.DataKinds)
	return withUnlock(ctx, s.locker, func() (model.GarbageCollectResult, error) {
		return withSessionLock(ctx, s.sessionLocker, func(ctx context.Context, grantedLock model.GrantedSessionLock) (
			model.GarbageCollectResult, error,
		) {
			// Resolved outside the lock: a session removed since has no garbage, only a wrong answer.
			if err := requireSession(ctx, s.sessions, s.session); err != nil {
				return model.GarbageCollectResult{}, err
			}
			return s.collectGarbage(ctx, req, collectors, grantedLock)
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

// withSessionLock runs fn on the session lock's context, cancelled once the lock is lost, and
// unlocks however fn ends.
func withSessionLock[T any](
	ctx context.Context, locker model.SessionLocker,
	fn func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error),
) (T, error) {
	waitCtx, wait := progress.Begin(ctx, progress.Step{Kind: kindSessionLock})
	lockCtx, grantedLock, err := locker.Lock(waitCtx, model.SessionLockOperationGarbageCollect)
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

// collectGarbage reports what the collectors find or, past a dry run, deletes it.
func (s *GarbageCollector) collectGarbage(
	ctx context.Context, req model.GarbageCollectRequest, collectors collectorSet,
	grantedLock model.GrantedSessionLock,
) (model.GarbageCollectResult, error) {
	if req.DryRun {
		return s.reportGarbage(ctx, req, collectors)
	}
	return s.deleteGarbage(ctx, req, collectors, grantedLock)
}

// reportGarbage finds the garbage and deletes nothing. Past a lost lock or an interrupt, what it
// judged is no verdict: it returns the cause alone.
func (s *GarbageCollector) reportGarbage(
	ctx context.Context, req model.GarbageCollectRequest, collectors collectorSet,
) (model.GarbageCollectResult, error) {
	inflightWarnings := s.readInflightWarnings(ctx)
	found := s.findGarbage(ctx, req, collectors)
	if cause := context.Cause(ctx); cause != nil {
		return model.GarbageCollectResult{}, cause
	}
	result := s.newGarbageCollectResult(req, found, inflightWarnings)
	if result.IsPartial() {
		return result, model.ErrGarbageCollectPartial
	}
	return result, nil
}

// deleteGarbage finds the garbage, then deletes it. A run stopped while finding returns the cause
// alone, as a dry run does; what it deleted cannot come back, so one stopped partway through
// deleting returns its result beside a *model.GarbageCollectInterruptedError.
func (s *GarbageCollector) deleteGarbage(
	ctx context.Context, req model.GarbageCollectRequest, collectors collectorSet,
	grantedLock model.GrantedSessionLock,
) (model.GarbageCollectResult, error) {
	if err := s.requireNoInflight(ctx); err != nil {
		return model.GarbageCollectResult{}, err
	}
	found := s.findGarbage(ctx, req, collectors)
	if cause := context.Cause(ctx); cause != nil {
		return model.GarbageCollectResult{}, cause
	}
	eraseCtx, erase := progress.Begin(ctx, progress.Step{Kind: kindGCErase})
	deleted, err := s.erasers.deleteGarbage(eraseCtx, grantedLock, found)
	erase.End(err)
	result := s.newGarbageCollectResult(req, deleted, newUndeletedWarnings(deleted.reports()))
	if err != nil {
		return result, &model.GarbageCollectInterruptedError{Cause: err}
	}
	if result.IsPartial() {
		return result, model.ErrGarbageCollectPartial
	}
	return result, nil
}

// requireNoInflight refuses a run that deletes while an interrupted rollback stands: what it was
// writing is not settled.
func (s *GarbageCollector) requireNoInflight(ctx context.Context) error {
	marker, found, err := s.inflight.Get(ctx)
	if err != nil {
		return fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("session", marker.Session.String()).
		Str("compose_project", marker.ComposeProject.String()).
		Msg("finish or recover the interrupted rollback first; arcave gc --dry-run still reports")
	return err
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

// findGarbage runs the daemon collectors, then each compose project's.
func (s *GarbageCollector) findGarbage(
	ctx context.Context, req model.GarbageCollectRequest, collectors collectorSet,
) garbageFound {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindGCFind})
	defer func() { step.End(context.Cause(ctx)) }()
	grace := s.grace
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
	daemonReports := collectors.runDaemonCollectors(ctx, daemonScope, listErr)
	logGarbageFound(ctx, daemonReports)
	return garbageFound{
		daemon:          daemonReports,
		composeProjects: s.findComposeProjectGarbage(ctx, collectors, sessionStore, composeProjects, listErr),
	}
}

// findComposeProjectGarbage runs the compose-project collectors over every compose project.
func (s *GarbageCollector) findComposeProjectGarbage(
	ctx context.Context, collectors collectorSet, sessionStore storages.Navigator,
	names []model.DockerComposeProjectName, listErr error,
) []composeProjectGarbage {
	if len(collectors.composeProject) == 0 {
		// No compose-project collector runs: reading the scopes would only hold the lock longer.
		return nil
	}
	if listErr != nil {
		return []composeProjectGarbage{{
			reports: collectors.runComposeProjectCollectors(ctx, "", ComposeProjectScope{}, listErr),
		}}
	}
	garbage := make([]composeProjectGarbage, 0, len(names))
	for _, name := range names {
		projectCtx, step := progress.Begin(ctx, progress.Step{
			Kind: kindComposeProject,
			Name: name.String(),
		})
		scope, store, err := s.readComposeProjectScope(projectCtx, sessionStore, name)
		reports := collectors.runComposeProjectCollectors(projectCtx, name, scope, err)
		step.End(context.Cause(projectCtx))
		logGarbageFound(ctx, reports)
		garbage = append(garbage, composeProjectGarbage{
			scope:   scope,
			store:   store,
			reports: reports,
		})
	}
	return garbage
}

// newGarbageCollectResult is the result over found.
func (s *GarbageCollector) newGarbageCollectResult(
	req model.GarbageCollectRequest, found garbageFound, warnings []string,
) model.GarbageCollectResult {
	reports := found.reports()
	return model.GarbageCollectResult{
		DryRun:     req.DryRun,
		Session:    s.session,
		Reports:    reports,
		Savepoints: newDeadSavepoints(reports),
		Total:      newGarbageTotal(reports),
		Warnings:   warnings,
	}
}
