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
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// sessionLookup reads a session's record; the default session always has one.
type sessionLookup interface {
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
}

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// writeAdmission admits the run's writes to the arcave project's store, under the project lock and
// before the first: the session's lock, a dry run's included.
type writeAdmission interface {
	AdmitWrite(ctx context.Context) error
}

// inflightMarkers reads the interrupted-rollback marker.
type inflightMarkers interface {
	Get(ctx context.Context) (marker model.InflightMarker, found bool, err error)
}

// Deps are GarbageCollector's collaborators.
type Deps struct {
	Sessions sessionLookup
	// ProviderSession is the session the gc looks at, as the provider holds it: its compose
	// projects, savepoints and garbage.
	ProviderSession provider.Session
	Locker          projectLocker
	// WriteAdmission admits the run's writes to the arcave project's store, before the first.
	WriteAdmission writeAdmission
	// SessionLocker holds the session's lock in the store for the whole run.
	SessionLocker model.SessionLocker
	Inflight      inflightMarkers
	// RetainRecent is how many safety snapshots of each compose project a run keeps.
	RetainRecent int
}

// GarbageCollector finds one session's garbage and the safety snapshots past the retention, and
// deletes them.
type GarbageCollector struct {
	sessionID       model.SessionID
	sessions        sessionLookup
	providerSession provider.Session
	locker          projectLocker
	writeAdmission  writeAdmission
	sessionLocker   model.SessionLocker
	inflight        inflightMarkers
	retainRecent    int
}

// New builds a GarbageCollector over deps.
func New(deps Deps) *GarbageCollector {
	return &GarbageCollector{
		sessionID:       deps.ProviderSession.ID(),
		sessions:        deps.Sessions,
		providerSession: deps.ProviderSession,
		locker:          deps.Locker,
		writeAdmission:  deps.WriteAdmission,
		sessionLocker:   deps.SessionLocker,
		inflight:        deps.Inflight,
		retainRecent:    deps.RetainRecent,
	}
}

// GarbageCollect finds the garbage among req's data kinds and the safety snapshots past the
// retention and, past a dry run, deletes them, under the project and session locks.
func (s *GarbageCollector) GarbageCollect(
	ctx context.Context, req model.GarbageCollectRequest,
) (_ model.GarbageCollectResult, err error) {
	// Opened outside the locks, so a lost lock's cause lands on the root.
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindGC})
	defer func() { step.End(err) }()
	if err := req.Validate(); err != nil {
		return model.GarbageCollectResult{}, err
	}
	return withUnlock(ctx, s.locker, func() (model.GarbageCollectResult, error) {
		if err := s.writeAdmission.AdmitWrite(ctx); err != nil {
			return model.GarbageCollectResult{}, fmt.Errorf("admit the write: %w", err)
		}
		return withSessionLock(ctx, s.sessionLocker, func(ctx context.Context, grantedLock model.GrantedSessionLock) (
			model.GarbageCollectResult, error,
		) {
			// The session was resolved outside the lock and may be gone since.
			if err := requireSession(ctx, s.sessions, s.sessionID); err != nil {
				return model.GarbageCollectResult{}, err
			}
			return s.collectGarbage(ctx, req, grantedLock)
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

// collectGarbage reports what is found or, past a dry run, deletes it.
func (s *GarbageCollector) collectGarbage(
	ctx context.Context, req model.GarbageCollectRequest, grantedLock model.GrantedSessionLock,
) (model.GarbageCollectResult, error) {
	if req.DryRun {
		return s.reportGarbage(ctx, req)
	}
	return s.deleteGarbage(ctx, req, grantedLock)
}

// reportGarbage finds the garbage and deletes nothing. Past a lost lock or an interrupt, what it
// judged is no verdict: it returns the cause alone.
func (s *GarbageCollector) reportGarbage(
	ctx context.Context, req model.GarbageCollectRequest,
) (model.GarbageCollectResult, error) {
	inflightWarnings := s.readInflightWarnings(ctx)
	found := s.findGarbage(ctx, req.DataKinds)
	if cause := context.Cause(ctx); cause != nil {
		return model.GarbageCollectResult{}, cause
	}
	result := s.newGarbageCollectResult(req, newFoundReports(found), inflightWarnings)
	if result.IsPartial() {
		return result, model.ErrGarbageCollectPartial
	}
	return result, nil
}

// deleteGarbage finds the garbage, then deletes it; a run stopped partway through deleting returns
// its result beside a *model.GarbageCollectInterruptedError, one stopped while finding the cause alone.
func (s *GarbageCollector) deleteGarbage(
	ctx context.Context, req model.GarbageCollectRequest, grantedLock model.GrantedSessionLock,
) (model.GarbageCollectResult, error) {
	if err := s.requireNoInflight(ctx); err != nil {
		return model.GarbageCollectResult{}, err
	}
	found := s.findGarbage(ctx, req.DataKinds)
	if cause := context.Cause(ctx); cause != nil {
		return model.GarbageCollectResult{}, cause
	}
	eraseCtx, erase := progress.Begin(ctx, progress.Step{Kind: kindGCErase})
	deleted, err := eraseGarbage(eraseCtx, grantedLock, found)
	erase.End(err)
	result := s.newGarbageCollectResult(req, deleted, nil)
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

// findGarbage finds what the provider deems garbage, then the safety snapshots past the retention:
// a safety snapshot retired by this run is still live to the provider's finding.
func (s *GarbageCollector) findGarbage(ctx context.Context, dataKinds []model.GarbageDataKind) []provider.Garbage {
	findCtx, step := progress.Begin(ctx, progress.Step{Kind: kindGCFind})
	found := s.providerSession.FindGarbage(findCtx, dataKinds)
	step.End(context.Cause(findCtx))
	if ctx.Err() != nil {
		// Its caller returns the cause: nothing judged past it is a verdict.
		return found
	}
	found = append(found, s.readRetired(ctx, dataKinds)...)
	logGarbageFound(ctx, newFoundReports(found))
	return found
}

// newGarbageCollectResult is the result over reports.
func (s *GarbageCollector) newGarbageCollectResult(
	req model.GarbageCollectRequest, reports []model.GarbageReport, warnings []string,
) model.GarbageCollectResult {
	return model.GarbageCollectResult{
		DryRun:     req.DryRun,
		Session:    s.sessionID,
		Reports:    reports,
		Savepoints: newDeadSavepoints(reports),
		Total:      newGarbageTotal(reports),
		Warnings:   warnings,
	}
}

// newFoundReports are the reports of found, with no deletion.
func newFoundReports(found []provider.Garbage) []model.GarbageReport {
	reports := make([]model.GarbageReport, 0, len(found))
	for _, garbage := range found {
		reports = append(reports, garbage.Report())
	}
	return reports
}
