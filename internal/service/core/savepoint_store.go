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
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/xhit/go-str2duration/v2"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// maxParallelRemovals caps how many savepoints applyRemoval deletes in parallel.
const maxParallelRemovals = 5

// SavepointStoreDeps is what the workflows that read and delete in the store alone run on.
type SavepointStoreDeps struct {
	SavepointRepo savepointRepository
	ProjectLocker projectLocker
	// SessionLocker holds Session's lock in the store for the whole of a workflow.
	SessionLocker model.SessionLocker
	Inflight      inflightMarkers
	// Session is the session SavepointRepo belongs to; zero means model.DefaultSession.
	Session model.SessionID
	// ComposeProject is the compose project SavepointRepo belongs to.
	ComposeProject model.DockerComposeProjectName
	// Sessions checks, under the lock, that Session still exists.
	Sessions sessionLookup
}

// SavepointStoreService lists and removes savepoints over one compose project's store. It reaches
// no daemon, so it runs where none answers.
type SavepointStoreService struct {
	savepointRepo  savepointRepository
	projectLocker  projectLocker
	sessionLocker  model.SessionLocker
	inflightMarker inflightMarkers
	session        model.SessionID
	composeProject model.DockerComposeProjectName
	sessions       sessionLookup
}

func NewSavepointStoreService(deps SavepointStoreDeps) *SavepointStoreService {
	if deps.Session.IsNil() {
		deps.Session = model.DefaultSession
	}
	return &SavepointStoreService{
		savepointRepo:  deps.SavepointRepo,
		projectLocker:  deps.ProjectLocker,
		sessionLocker:  deps.SessionLocker,
		inflightMarker: deps.Inflight,
		session:        deps.Session,
		composeProject: deps.ComposeProject,
		sessions:       deps.Sessions,
	}
}

// ListSavepoints returns the committed savepoints req asks for, in req.Sort's order.
func (s *SavepointStoreService) ListSavepoints(ctx context.Context, req model.ListRequest) ([]model.Savepoint, error) {
	savepoints, err := s.listSavepoints(ctx, req.IncludeSafetySnapshots)
	if err != nil {
		return nil, err
	}
	if req.Sort != model.SortAsc {
		slices.Reverse(savepoints)
	}
	return savepoints, nil
}

// RemoveSavepoints deletes savepoints selected by req. Combined retention rules
// keep a savepoint if any one of them retains it.
//
// A dry run takes no lock — it only reads. A real removal locks before it lists,
// so the set it selects is the set it deletes.
func (s *SavepointStoreService) RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (_ model.RemoveResult, err error) {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindSavepointRemove})
	defer func() { step.End(err) }()
	if err := validateRemoveRequest(req); err != nil {
		return model.RemoveResult{}, err
	}

	if req.DryRun {
		removed, err := s.selectRemoval(ctx, req)
		if err != nil {
			return model.RemoveResult{}, err
		}
		return model.RemoveResult{
			DryRun:  true,
			Removed: removed,
		}, nil
	}

	return locked(ctx, s, model.SessionLockOperationSavepointRemove,
		func(ctx context.Context, grantedLock model.GrantedSessionLock) (model.RemoveResult, error) {
			// The gate guards the safety snapshot too: while an interrupted rollback is unresolved,
			// that snapshot is the way back, and no selector may delete it.
			if _, err := s.requireNoInflight(ctx, ""); err != nil {
				return model.RemoveResult{}, err
			}
			return s.removeSavepoints(ctx, grantedLock, req)
		})
}

// locked runs a workflow with the project lock and the session's lock held, once its session is
// known to still exist: one deleted since the command resolved it must not be written into again.
// fn gets the session lock and its context.
func locked[T any](
	ctx context.Context, s *SavepointStoreService, operation model.SessionLockOperation,
	fn func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error),
) (T, error) {
	return withUnlock(ctx, s.projectLocker, func() (T, error) {
		return withSessionLock(ctx, s.sessionLocker, operation, func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error) {
			if err := requireSession(ctx, s.sessions, s.session); err != nil {
				var zero T
				return zero, err
			}
			return fn(ctx, grantedLock)
		})
	})
}

// listSavepoints, oldest first, is core's only read of the repository, so the rule that hides a
// safety snapshot has one place to be forgotten. Which selector may see one is
// findSavepoint's and selectSavepointsToRemove's call.
func (s *SavepointStoreService) listSavepoints(ctx context.Context, includeSafetySnapshots bool) ([]model.Savepoint, error) {
	savepoints, err := s.savepointRepo.ListSavepoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing savepoints: %w", err)
	}
	if includeSafetySnapshots {
		return savepoints, nil
	}
	return visibleSavepoints(savepoints), nil
}

// visibleSavepoints drops the safety snapshots. Non-nil, so an empty listing
// marshals as [] rather than null.
func visibleSavepoints(savepoints []model.Savepoint) []model.Savepoint {
	visible := make([]model.Savepoint, 0, len(savepoints))
	for _, savepoint := range savepoints {
		if !savepoint.IsSafetySnapshot() {
			visible = append(visible, savepoint)
		}
	}
	return visible
}

// validateRemoveRequest rejects contradictory or incomplete requests — refs and
// retention rules are mutually exclusive, one selector is required — with the
// model sentinels the adapters match on.
func validateRemoveRequest(req model.RemoveRequest) error {
	// Before hasRetention, which counts only a positive RetainRecent: a negative
	// would fall through and be reported as "no selector provided".
	if req.RetainRecent < 0 {
		return model.ErrRetainRecentNegative
	}

	hasRetention := req.RetainRecent > 0 || req.RetainFor != "" || req.BeforeDate != ""

	if len(req.Refs) > 0 && hasRetention {
		return model.ErrRemoveSelectorConflict
	}
	if len(req.Refs) == 0 && !hasRetention {
		return model.ErrRemoveNoSelector
	}
	return nil
}

// removeSavepoints selects what req names and deletes each one while grantedLock still holds.
func (s *SavepointStoreService) removeSavepoints(
	ctx context.Context, grantedLock model.GrantedSessionLock, req model.RemoveRequest,
) (model.RemoveResult, error) {
	removed, err := s.selectRemoval(ctx, req)
	if err != nil {
		return model.RemoveResult{}, err
	}
	if len(removed) > 0 {
		if err := s.applyRemoval(ctx, grantedLock, removed); err != nil {
			logInterrupt(ctx, "removal interrupted: the savepoints not yet removed are kept, "+
				"a partly removed one is left as an orphan directory")
			return model.RemoveResult{}, err
		}
	}
	return model.RemoveResult{Removed: removed}, nil
}

// selectRemoval lists the savepoints req names.
func (s *SavepointStoreService) selectRemoval(ctx context.Context, req model.RemoveRequest) ([]model.Savepoint, error) {
	// Everything, safety snapshots included: which of them this request may see is
	// selectSavepointsToRemove's call, and it differs by selector.
	savepoints, err := s.listSavepoints(ctx, true)
	if err != nil {
		return nil, err
	}
	_, removed, err := s.selectSavepointsToRemove(savepoints, req)
	if err != nil {
		return nil, err
	}
	return removed, nil
}

// selectSavepointsToRemove splits records by req: an explicit ref list when
// given, otherwise the retention rules.
//
// The two see different sets. An id sees every record, safety snapshots
// included — that is how one is removed by hand — a name only the visible ones. The retention
// rules scan, and a scan sees only what `savepoint list` shows: an undo must
// neither count as one of the N newest, nor be swept by an age rule.
func (s *SavepointStoreService) selectSavepointsToRemove(
	savepoints []model.Savepoint,
	req model.RemoveRequest,
) (kept, removed []model.Savepoint, err error) {
	if len(req.Refs) > 0 {
		return partitionByRefs(savepoints, req.Refs)
	}
	cutoffs, err := s.parseRetentionCutoffs(req)
	if err != nil {
		return nil, nil, err
	}
	kept, removed = partitionSavepoints(visibleSavepoints(savepoints), req.RetainRecent, cutoffs)
	return kept, removed, nil
}

// parseRetentionCutoffs parses the time-based retention flags into cutoff instants;
// each marks savepoints created before it as deletable.
func (s *SavepointStoreService) parseRetentionCutoffs(req model.RemoveRequest) ([]time.Time, error) {
	var cutoffs []time.Time
	if req.RetainFor != "" {
		retainFor, err := str2duration.ParseDuration(req.RetainFor)
		if err != nil {
			return nil, fmt.Errorf("invalid --retain-for %q: %w", req.RetainFor, err)
		}
		cutoffs = append(cutoffs, time.Now().UTC().Add(-retainFor))
	}
	if req.BeforeDate != "" {
		before, err := time.Parse(time.RFC3339Nano, req.BeforeDate)
		if err != nil {
			return nil, fmt.Errorf("invalid --before-date %q: %w", req.BeforeDate, err)
		}
		cutoffs = append(cutoffs, before)
	}
	return cutoffs, nil
}

// partitionByRefs splits records into the ones a ref names (removed) and the rest
// (kept). A canonical UUID is an id and reaches every record, safety snapshots
// included; anything else is a name and sees only what `savepoint list` shows. A ref
// naming nothing, or a name several savepoints carry, fails the whole request.
func partitionByRefs(
	savepoints []model.Savepoint,
	refs []string,
) (kept, removed []model.Savepoint, err error) {
	var (
		visible = visibleSavepoints(savepoints)
		want    = make(map[model.SavepointID]bool, len(refs))
		missing []string
	)
	for _, ref := range refs {
		var matched []model.Savepoint
		if id := model.SavepointID(ref); id.Validate() == nil {
			matched = findSavepointsByID(savepoints, id)
		} else {
			matched = findSavepointsByName(visible, ref)
		}
		switch len(matched) {
		case 0:
			missing = append(missing, ref)
		case 1:
			want[matched[0].ID] = true
		default:
			return nil, nil, newAmbiguousSavepointNameError(ref, matched)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("savepoint(s) not found: %s", strings.Join(missing, ", "))
	}
	for _, savepoint := range savepoints {
		if want[savepoint.ID] {
			removed = append(removed, savepoint)
		} else {
			kept = append(kept, savepoint)
		}
	}
	return kept, removed, nil
}

// partitionSavepoints removes a record only when every active rule marks it
// deletable: outside the retainRecent newest, and older than every cutoff.
func partitionSavepoints(
	savepoints []model.Savepoint,
	retainRecent int,
	cutoffs []time.Time,
) (kept, removed []model.Savepoint) {
	retainedByRecency := map[model.SavepointID]bool{}
	hasRecent := retainRecent > 0
	if hasRecent {
		byNewest := append([]model.Savepoint(nil), savepoints...)
		sort.Slice(byNewest, func(i, j int) bool {
			return byNewest[i].CreatedAt.After(byNewest[j].CreatedAt)
		})
		for i := 0; i < retainRecent && i < len(byNewest); i++ {
			retainedByRecency[byNewest[i].ID] = true
		}
	}

	for _, savepoint := range savepoints {
		deletable := !hasRecent || !retainedByRecency[savepoint.ID]
		for _, cutoff := range cutoffs {
			if !savepoint.CreatedAt.Before(cutoff) {
				deletable = false
			}
		}
		if deletable {
			removed = append(removed, savepoint)
		} else {
			kept = append(kept, savepoint)
		}
	}
	return kept, removed
}

// applyRemoval deletes every savepoint removed names. A delete that fails past the record leaves
// an orphaned directory, reported here and reclaimed by `arcave gc`.
func (s *SavepointStoreService) applyRemoval(
	ctx context.Context, grantedLock model.GrantedSessionLock, removed []model.Savepoint,
) error {
	// A plain errgroup.Group, not WithContext: a cancelled sibling would abort
	// mid-delete. Wait reports only the first failure, so each goroutine records
	// its own — that is what names the savepoints whose data survived.
	errs := make([]error, len(removed))
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindSavepointDrop}, len(removed))
	eg := new(errgroup.Group)
	eg.SetLimit(maxParallelRemovals)
	for i, savepoint := range removed {
		eg.Go(func() error {
			removeCtx, step := progress.Run(ctx, progress.Step{
				Kind: kindSavepoint,
				ID:   savepoint.ID.String(),
				Name: savepoint.Name,
			})
			errs[i] = s.removeSavepoint(removeCtx, grantedLock, savepoint)
			step.End(errs[i])
			return errs[i]
		})
	}
	if err := eg.Wait(); err != nil {
		err = errors.Join(errs...)
		batch.End(err)
		return err
	}
	batch.End(nil)
	return nil
}

// removeSavepoint drops one savepoint from the savepoint store.
func (s *SavepointStoreService) removeSavepoint(
	ctx context.Context, grantedLock model.GrantedSessionLock, savepoint model.Savepoint,
) error {
	// Dropping it uncommits a record: only while the session's lock is still this run's.
	if err := grantedLock.Verify(ctx); err != nil {
		return fmt.Errorf("remove savepoint %s: %w", savepoint.ID, err)
	}
	if err := s.savepointRepo.DeleteSavepoint(ctx, savepoint.ID); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("savepoint", savepoint.ID.String()).
			Msg("failed to remove the savepoint; if its record went, the directory is orphaned for a gc")
		return err
	}
	return nil
}

// rollbackRecovery is what a rollback takes over from the interrupted one it recovers from; zero
// when it recovers from none.
type rollbackRecovery struct {
	recovering bool
	// safety is the safety snapshot the standing marker names: the recovery's marker keeps naming
	// it, since a failed recovery leaves that snapshot the last copy of the state before them both.
	safety model.SavepointID
}

// requireNoInflight refuses to run while .arcave/inflight.json stands, except for the rollback
// that recovers from it. restoring is the savepoint the caller rolls back to, "" for any other
// workflow. It reports what the caller, if it is that recovery, takes over.
func (s *SavepointStoreService) requireNoInflight(ctx context.Context, restoring model.SavepointID) (rollbackRecovery, error) {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return rollbackRecovery{}, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return rollbackRecovery{}, nil
	}
	if s.isRecoveryRollback(marker, restoring) {
		zerolog.Ctx(ctx).Info().Str("interrupted_target", marker.Target.String()).
			Msg("recovering from an interrupted rollback; the marker clears on success")
		return rollbackRecovery{
			recovering: true,
			safety:     marker.Safety,
		}, nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("interrupted_target", marker.Target.String()).
		Str("session", marker.Session.String()).
		Str("compose_project", marker.ComposeProject.String()).
		Str("safety_snapshot_id", marker.Safety.String()).
		Str("phase", string(marker.Phase)).
		Msg(recoveryHint(marker))
	return rollbackRecovery{}, err
}

// isRecoveryRollback reports whether a rollback to id, in this session and compose project,
// recovers from the interrupted rollback the marker records: one to its safety snapshot or, when it
// took none and no earlier state is left to protect, one to any savepoint.
func (s *SavepointStoreService) isRecoveryRollback(marker model.InflightMarker, id model.SavepointID) bool {
	if id == "" || marker.Session != s.session || marker.ComposeProject != s.composeProject {
		return false
	}
	return marker.Safety == "" || id == marker.Safety
}
