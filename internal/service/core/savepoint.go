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

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/rs/zerolog"
	"github.com/xhit/go-str2duration/v2"
	"golang.org/x/sync/errgroup"
)

// maxParallelRemovals caps how many savepoints applyRemoval deletes
// in parallel; each fans out over its own volumes in turn.
const maxParallelRemovals = 5

// errNoVolumesToBackUp refuses a savepoint that would cover nothing. It stays
// unexported: no adapter matches on it, so it is not a seam contract and does
// not belong in model.
var errNoVolumesToBackUp = errors.New("no stateful volumes found; nothing to capture")

// errCaptureDirectoryNotEmpty refuses to capture into a directory that already
// holds data. Unexported for the same reason: nothing matches on it.
var errCaptureDirectoryNotEmpty = errors.New("capture directory is not empty")

// CreateSavepoint stops the services, captures every volume under one id, commits
// the record, and starts the services again. The project lock is held throughout.
//
// On error the record is zero, whatever the store holds: a savepoint committed
// before a failed restart is named by the error and found by `savepoint list`.
func (s *Service) CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error) {
	return locked(ctx, s, func() (model.Savepoint, error) {
		return s.createSavepoint(ctx, req)
	})
}

// createSavepoint is the workflow itself, run with the project lock held.
func (s *Service) createSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error) {
	log := zerolog.Ctx(ctx)

	if err := req.Validate(); err != nil {
		return model.Savepoint{}, err
	}
	if _, err := s.requireNoInflight(ctx, ""); err != nil {
		return model.Savepoint{}, err
	}
	visible, err := s.listSavepoints(ctx, false)
	if err != nil {
		return model.Savepoint{}, err
	}
	if err := requireUniqueName(visible, req.Name); err != nil {
		return model.Savepoint{}, err
	}

	working, err := s.prepare(ctx, req.WaitTimeout)
	if err != nil {
		logInterrupt(ctx, "savepoint interrupted before anything was stopped: nothing changed")
		return model.Savepoint{}, preferInterrupt(ctx, err)
	}
	s.warnCapturePolicyDrift(ctx, visible, working.project)

	killed, err := s.stopServices(ctx, working)
	if err != nil {
		// A stop that failed or was interrupted partway may have stopped some services.
		logInterrupt(ctx, "savepoint interrupted while stopping services: restarting them, nothing captured")
		if err := s.restartServices(ctx, working); err != nil {
			log.Error().Err(err).Msg("services failed to restart after stop failure")
		}
		return model.Savepoint{}, preferInterrupt(ctx, err)
	}

	// Capture while the services are stopped. The capture error is what the caller
	// gets; the restart on top of it is best-effort and only logged.
	savepoint, err := s.performSavepoint(ctx, working, killed, req.Name, req.Description, req.Labels)
	if err != nil {
		logInterrupt(ctx, "savepoint interrupted during capture: restarting services, nothing committed")
		err = preferInterrupt(ctx, err)
		if err := s.restartServices(ctx, working); err != nil {
			log.Error().Err(err).Msg("services failed to restart after capture failure")
		}
		return model.Savepoint{}, fmt.Errorf("capturing volumes: %w", err)
	}

	logInterrupt(ctx, "savepoint committed before the interrupt: restarting services")
	if err := s.restartServices(ctx, working); err != nil {
		return model.Savepoint{}, fmt.Errorf("savepoint %s committed; services failed to start: %w", savepoint.ID, err)
	}
	return savepoint, nil
}

// requireUniqueName refuses a name another of the visible savepoints carries. Safety
// snapshots do not count: a name selector never sees them.
func requireUniqueName(visible []model.Savepoint, name string) error {
	if name == "" {
		return nil
	}
	for _, savepoint := range visible {
		if savepoint.Name == name {
			return fmt.Errorf("%w: %q names savepoint %s", model.ErrSavepointNameTaken, name, savepoint.ID)
		}
	}
	return nil
}

// ListSavepoints returns the committed savepoints req asks for, in req.Sort's order.
func (s *Service) ListSavepoints(ctx context.Context, req model.ListRequest) ([]model.Savepoint, error) {
	savepoints, err := s.listSavepoints(ctx, req.IncludeSafetySnapshots)
	if err != nil {
		return nil, err
	}
	if req.Sort != model.SortAsc {
		slices.Reverse(savepoints)
	}
	return savepoints, nil
}

// listSavepoints, oldest first, is core's only read of the repository, so the rule that hides a
// safety snapshot has one place to be forgotten. Which selector may see one is
// findSavepoint's and selectSavepointsToRemove's call.
func (s *Service) listSavepoints(ctx context.Context, includeSafetySnapshots bool) ([]model.Savepoint, error) {
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

// RemoveSavepoints deletes savepoints selected by req. Combined retention rules
// keep a savepoint if any one of them retains it.
//
// A dry run takes no lock — it only reads. A real removal locks before it lists,
// so the set it selects is the set it deletes.
func (s *Service) RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error) {
	if err := validateRemoveRequest(req); err != nil {
		return model.RemoveResult{}, err
	}

	if req.DryRun {
		return s.removeSavepoints(ctx, req)
	}

	return locked(ctx, s, func() (model.RemoveResult, error) {
		// The gate guards the safety snapshot too: while an interrupted rollback is unresolved,
		// that snapshot is the way back, and no selector may delete it.
		if _, err := s.requireNoInflight(ctx, ""); err != nil {
			return model.RemoveResult{}, err
		}
		return s.removeSavepoints(ctx, req)
	})
}

// removeSavepoints selects what req names and deletes it; a dry run stops at the
// selection.
func (s *Service) removeSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error) {
	// Everything, safety snapshots included: which of them this request may see is
	// selectSavepointsToRemove's call, and it differs by selector.
	savepoints, err := s.listSavepoints(ctx, true)
	if err != nil {
		return model.RemoveResult{}, err
	}

	_, removed, err := s.selectSavepointsToRemove(savepoints, req)
	if err != nil {
		return model.RemoveResult{}, err
	}

	if !req.DryRun && len(removed) > 0 {
		if err := s.applyRemoval(ctx, removed); err != nil {
			logInterrupt(ctx, "removal interrupted: the savepoints not yet removed are kept, "+
				"a partly removed one is left as an orphan directory")
			return model.RemoveResult{}, err
		}
	}

	return model.RemoveResult{DryRun: req.DryRun, Removed: removed}, nil
}

// validateRemoveRequest rejects contradictory or incomplete requests — ids and
// retention rules are mutually exclusive, one selector is required — with the
// model sentinels the adapters match on.
func validateRemoveRequest(req model.RemoveRequest) error {
	// Before hasRetention, which counts only a positive RetainRecent: a negative
	// would fall through and be reported as "no selector provided".
	if req.RetainRecent < 0 {
		return model.ErrRetainRecentNegative
	}

	hasRetention := req.RetainRecent > 0 || req.RetainFor != "" || req.BeforeDate != ""

	if len(req.IDs) > 0 && hasRetention {
		return model.ErrRemoveSelectorConflict
	}
	if len(req.IDs) == 0 && !hasRetention {
		return model.ErrRemoveNoSelector
	}
	return nil
}

// selectSavepointsToRemove splits records by req: an explicit id list when
// given, otherwise the retention rules.
//
// The two see different sets. An id list sees every record, safety snapshots
// included — that is how a killed rollback's shard is cleared. The retention
// rules scan, and a scan sees only what `savepoint list` shows: an undo must
// neither count as one of the N newest, nor be swept by an age rule.
func (s *Service) selectSavepointsToRemove(
	savepoints []model.Savepoint,
	req model.RemoveRequest,
) (kept, removed []model.Savepoint, err error) {
	if len(req.IDs) > 0 {
		return partitionByIDs(savepoints, req.IDs)
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
func (s *Service) parseRetentionCutoffs(req model.RemoveRequest) ([]time.Time, error) {
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

// partitionByIDs splits records into the ones whose ID was requested (removed)
// and the rest (kept). It errors if any requested ID is not present, so a typo
// never silently deletes nothing.
func partitionByIDs(
	savepoints []model.Savepoint,
	ids []model.SavepointID,
) (kept, removed []model.Savepoint, err error) {
	want := make(map[model.SavepointID]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	seen := make(map[model.SavepointID]bool, len(ids))
	for _, savepoint := range savepoints {
		if want[savepoint.ID] {
			removed = append(removed, savepoint)
			seen[savepoint.ID] = true
		} else {
			kept = append(kept, savepoint)
		}
	}
	var missing []string
	for _, id := range ids {
		if !seen[id] {
			missing = append(missing, id.String())
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("savepoint(s) not found: %s", strings.Join(missing, ", "))
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

// applyRemoval deletes uncommit-first, so the only observable states are "fully
// present" and "gone". The cost is an orphaned directory when the data delete
// fails, reported here and reclaimed by a future `arcave savepoint gc`.
func (s *Service) applyRemoval(ctx context.Context, removed []model.Savepoint) error {
	// A plain errgroup.Group, not WithContext: a cancelled sibling would abort
	// mid-delete. Wait reports only the first failure, so each goroutine records
	// its own — that is what names the savepoints whose data survived.
	errs := make([]error, len(removed))
	eg := new(errgroup.Group)
	eg.SetLimit(maxParallelRemovals)
	for i, savepoint := range removed {
		eg.Go(func() error {
			errs[i] = s.removeSavepoint(ctx, savepoint)
			return errs[i]
		})
	}
	if err := eg.Wait(); err != nil {
		return errors.Join(errs...)
	}
	return nil
}

// removeSavepoint drops one savepoint from the savepoint store. The backends are
// built first: one that cannot be built leaves the savepoint whole.
func (s *Service) removeSavepoint(ctx context.Context, savepoint model.Savepoint) error {
	backends, err := s.newBackends(volumesOf(savepoint))
	if err != nil {
		return err
	}
	if err := s.dropSavepoint(ctx, savepoint, backends); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("savepoint", savepoint.ID.String()).
			Msg("savepoint was uncommitted but its data could not be removed; the directory is orphaned")
		return err
	}
	return nil
}

// findSavepoint resolves a request's selector to one record. Ids win, being
// unique; a name matching several is an error, never a silent guess.
//
// It takes every record so it can apply the visibility rule: an id resolves
// against all of them — rolling back to a killed rollback's undo is the manual
// recovery path, spelled by id — while Name and Latest see only what
// `savepoint list` shows.
//
// The caller has run req.Validate, so the branches read nil-ness alone.
func findSavepoint(savepoints []model.Savepoint, req model.RollbackRequest) (model.Savepoint, error) {
	if req.ID != nil {
		for _, savepoint := range savepoints {
			if savepoint.ID == *req.ID {
				return savepoint, nil
			}
		}
		return model.Savepoint{}, fmt.Errorf("savepoint not found: %s", *req.ID)
	}

	savepoints = visibleSavepoints(savepoints)
	if len(savepoints) == 0 {
		return model.Savepoint{}, errors.New("no savepoints exist")
	}

	if req.Name == nil {
		if req.Latest == nil || !*req.Latest {
			return model.Savepoint{}, model.ErrRollbackNoSelector
		}
		latest := savepoints[0]
		for _, savepoint := range savepoints[1:] {
			if savepoint.CreatedAt.After(latest.CreatedAt) {
				latest = savepoint
			}
		}
		return latest, nil
	}

	var byName []model.Savepoint
	for _, savepoint := range savepoints {
		if savepoint.Name == *req.Name {
			byName = append(byName, savepoint)
		}
	}
	switch len(byName) {
	case 0:
		return model.Savepoint{}, fmt.Errorf("savepoint not found: %s", *req.Name)
	case 1:
		return byName[0], nil
	default:
		ids := make([]model.SavepointID, len(byName))
		for i, savepoint := range byName {
			ids[i] = savepoint.ID
		}
		slices.Sort(ids)
		return model.Savepoint{}, &model.AmbiguousSavepointNameError{Name: *req.Name, IDs: ids}
	}
}
