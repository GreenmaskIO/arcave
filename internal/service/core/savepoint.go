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

// maxConcurrentSavepointRemovals caps how many savepoints applyRemoval deletes
// in parallel; each fans out over its own volumes in turn.
const maxConcurrentSavepointRemovals = 5

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
	return withUnlock(ctx, s, func() (model.Savepoint, error) {
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
	if err := s.requireUniqueName(ctx, req.Name); err != nil {
		return model.Savepoint{}, err
	}

	ws, err := s.prepare(ctx, req.WaitTimeout)
	if err != nil {
		return model.Savepoint{}, err
	}

	killed, err := s.stopServices(ctx, ws)
	if err != nil {
		return model.Savepoint{}, err
	}

	// Capture while the services are stopped. The capture error is what the caller
	// gets; the restart on top of it is best-effort and only logged.
	record, err := s.performSavepoint(ctx, ws, killed, req.Name, req.Description, req.Labels)
	if err != nil {
		if err := s.startServices(ctx, ws); err != nil {
			log.Error().Err(err).Msg("services failed to restart after capture failure")
		}
		return model.Savepoint{}, fmt.Errorf("capturing volumes: %w", err)
	}

	if err := s.startServices(ctx, ws); err != nil {
		return model.Savepoint{}, fmt.Errorf("savepoint %s committed; services failed to start: %w", record.ID, err)
	}
	return record, nil
}

// requireUniqueName refuses a name another savepoint of the compose project carries.
// Safety snapshots do not count: a name selector never sees them.
func (s *Service) requireUniqueName(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	records, err := s.listSavepoints(ctx, false)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Name == name {
			return fmt.Errorf("%w: %q names savepoint %s", model.ErrSavepointNameTaken, name, r.ID)
		}
	}
	return nil
}

// ListSavepoints returns the committed savepoints req asks for.
func (s *Service) ListSavepoints(ctx context.Context, req model.ListRequest) ([]model.Savepoint, error) {
	return s.listSavepoints(ctx, req.IncludeSafetySnapshots)
}

// listSavepoints is core's only read of the repository, so the rule that hides a
// safety snapshot has one place to be forgotten. Which selector may see one is
// findSavepoint's and selectSavepointsToRemove's call.
func (s *Service) listSavepoints(ctx context.Context, includeSafetySnapshots bool) ([]model.Savepoint, error) {
	records, err := s.savepointRepo.ListSavepoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing savepoints: %w", err)
	}
	if includeSafetySnapshots {
		return records, nil
	}
	return visibleSavepoints(records), nil
}

// visibleSavepoints drops the safety snapshots. Non-nil, so an empty listing
// marshals as [] rather than null.
func visibleSavepoints(records []model.Savepoint) []model.Savepoint {
	visible := make([]model.Savepoint, 0, len(records))
	for _, r := range records {
		if !r.IsSafetySnapshot() {
			visible = append(visible, r)
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

	return withUnlock(ctx, s, func() (model.RemoveResult, error) {
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
	records, err := s.listSavepoints(ctx, true)
	if err != nil {
		return model.RemoveResult{}, err
	}

	_, removed, err := s.selectSavepointsToRemove(records, req)
	if err != nil {
		return model.RemoveResult{}, err
	}

	if !req.DryRun && len(removed) > 0 {
		if err := s.applyRemoval(ctx, removed); err != nil {
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
	records []model.Savepoint,
	req model.RemoveRequest,
) (kept, removed []model.Savepoint, err error) {
	if len(req.IDs) > 0 {
		return partitionByIDs(records, req.IDs)
	}
	cutoffs, err := s.retentionCutoffs(req)
	if err != nil {
		return nil, nil, err
	}
	kept, removed = partitionSavepoints(visibleSavepoints(records), req.RetainRecent, cutoffs)
	return kept, removed, nil
}

// retentionCutoffs parses the time-based retention flags into cutoff instants;
// each marks savepoints created before it as deletable.
func (s *Service) retentionCutoffs(req model.RemoveRequest) ([]time.Time, error) {
	var cutoffs []time.Time
	if req.RetainFor != "" {
		d, err := str2duration.ParseDuration(req.RetainFor)
		if err != nil {
			return nil, fmt.Errorf("invalid --retain-for %q: %w", req.RetainFor, err)
		}
		cutoffs = append(cutoffs, time.Now().UTC().Add(-d))
	}
	if req.BeforeDate != "" {
		t, err := time.Parse(time.RFC3339Nano, req.BeforeDate)
		if err != nil {
			return nil, fmt.Errorf("invalid --before-date %q: %w", req.BeforeDate, err)
		}
		cutoffs = append(cutoffs, t)
	}
	return cutoffs, nil
}

// partitionByIDs splits records into the ones whose ID was requested (removed)
// and the rest (kept). It errors if any requested ID is not present, so a typo
// never silently deletes nothing.
func partitionByIDs(
	records []model.Savepoint,
	ids []model.SavepointID,
) (kept, removed []model.Savepoint, err error) {
	want := make(map[model.SavepointID]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	seen := make(map[model.SavepointID]bool, len(ids))
	for _, r := range records {
		if want[r.ID] {
			removed = append(removed, r)
			seen[r.ID] = true
		} else {
			kept = append(kept, r)
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
	records []model.Savepoint,
	retainRecent int,
	cutoffs []time.Time,
) (kept, removed []model.Savepoint) {
	retainedByRecency := map[model.SavepointID]bool{}
	hasRecent := retainRecent > 0
	if hasRecent {
		byNewest := append([]model.Savepoint(nil), records...)
		sort.Slice(byNewest, func(i, j int) bool {
			return byNewest[i].CreatedAt.After(byNewest[j].CreatedAt)
		})
		for i := 0; i < retainRecent && i < len(byNewest); i++ {
			retainedByRecency[byNewest[i].ID] = true
		}
	}

	for _, r := range records {
		del := !hasRecent || !retainedByRecency[r.ID]
		for _, c := range cutoffs {
			if !r.CreatedAt.Before(c) {
				del = false
			}
		}
		if del {
			removed = append(removed, r)
		} else {
			kept = append(kept, r)
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
	g := new(errgroup.Group)
	g.SetLimit(maxConcurrentSavepointRemovals)
	for i, r := range removed {
		g.Go(func() error {
			errs[i] = s.removeSavepoint(ctx, r)
			return errs[i]
		})
	}
	if err := g.Wait(); err != nil {
		return errors.Join(errs...)
	}
	return nil
}

// removeSavepoint drops one savepoint from the savepoint store. The backends are
// built first: one that cannot be built leaves the savepoint whole.
func (s *Service) removeSavepoint(ctx context.Context, record model.Savepoint) error {
	backends, err := s.buildBackends(volumesOf(record))
	if err != nil {
		return err
	}
	if err := s.dropSavepoint(ctx, record, backends); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("savepoint", record.ID.String()).
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
func findSavepoint(records []model.Savepoint, req model.RollbackRequest) (model.Savepoint, error) {
	if req.ID != nil {
		for _, r := range records {
			if r.ID == *req.ID {
				return r, nil
			}
		}
		return model.Savepoint{}, fmt.Errorf("savepoint not found: %s", *req.ID)
	}

	records = visibleSavepoints(records)
	if len(records) == 0 {
		return model.Savepoint{}, errors.New("no savepoints exist")
	}

	if req.Name == nil {
		if req.Latest == nil || !*req.Latest {
			return model.Savepoint{}, model.ErrRollbackNoSelector
		}
		latest := records[0]
		for _, r := range records[1:] {
			if r.CreatedAt.After(latest.CreatedAt) {
				latest = r
			}
		}
		return latest, nil
	}

	var byName []model.Savepoint
	for _, r := range records {
		if r.Name == *req.Name {
			byName = append(byName, r)
		}
	}
	switch len(byName) {
	case 0:
		return model.Savepoint{}, fmt.Errorf("savepoint not found: %s", *req.Name)
	case 1:
		return byName[0], nil
	default:
		ids := make([]model.SavepointID, len(byName))
		for i, r := range byName {
			ids[i] = r.ID
		}
		slices.Sort(ids)
		return model.Savepoint{}, &model.AmbiguousSavepointNameError{Name: *req.Name, IDs: ids}
	}
}
