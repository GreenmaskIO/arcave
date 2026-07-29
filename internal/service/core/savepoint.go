package core

import (
	"context"
	"errors"
	"fmt"
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

// CreateSavepoint stops the project services, captures every volume marked for
// backup under one id, commits the record, and starts the services again.
//
// The project lock is held for the whole workflow, and taken first: a caller who
// cannot have the project must not learn anything about it, and must certainly
// not stop its services.
//
// The volumes are resolved exactly as a rollback resolves them: a savepoint
// covering a volume its own rollback cannot reach would never roll back.
func (s *Service) CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error) {
	log := zerolog.Ctx(ctx)

	unlock, err := s.lockProject(ctx)
	if err != nil {
		return model.Savepoint{}, err
	}
	defer unlock()

	volumes, err := s.resolveBackupVolumes(ctx, req.ComposeFiles)
	if err != nil {
		return model.Savepoint{}, err
	}
	if len(volumes) == 0 {
		return model.Savepoint{}, errNoVolumesToBackUp
	}

	// Ahead of the stop: a backend that cannot be built leaves the project running.
	backends, err := s.buildBackends(volumes, model.BackendTierSavepoint)
	if err != nil {
		return model.Savepoint{}, err
	}

	log.Info().Msg("stopping services")
	if err := s.composeRunner.Stop(ctx, req.ComposeFiles...); err != nil {
		return model.Savepoint{}, fmt.Errorf("stopping services: %w", err)
	}

	// Capture while the services are stopped. On failure they still must come back
	// up, so the restart is best-effort — a restart failure on top changes nothing
	// and is only logged — and the capture error is what the caller gets.
	record, err := s.performSavepoint(ctx, s.savepointRepo, s.savepointStore, backends, req.Name, req.Description)
	if err != nil {
		// Nothing was committed, so there is no record to uncommit — only the data
		// the fan-out got as far as writing, which no savepoint will ever name. A
		// refused directory is the exception: its objects predate this run, and
		// keeping them is the point of the refusal.
		if !errors.Is(err, errCaptureDirectoryNotEmpty) {
			if dropErr := s.dropCaptures(ctx, s.savepointStore, record, backends); dropErr != nil {
				log.Warn().Err(dropErr).Str("savepoint", record.ID.String()).Msg("failed to remove the partial capture")
			}
		}
		log.Info().Msg("starting services")
		if err := s.composeRunner.Up(ctx, req.WaitTimeout, req.ComposeFiles...); err != nil {
			log.Error().Err(err).Msg("services failed to restart after capture failure")
		}
		return model.Savepoint{}, fmt.Errorf("capturing volumes: %w", err)
	}

	log.Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, req.WaitTimeout, req.ComposeFiles...); err != nil {
		return model.Savepoint{}, fmt.Errorf("starting services: %w", err)
	}
	return record, nil
}

// ListSavepoints returns every committed savepoint.
func (s *Service) ListSavepoints(ctx context.Context) ([]model.Savepoint, error) {
	return s.savepointRepo.List(ctx)
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

	if !req.DryRun {
		unlock, err := s.lockProject(ctx)
		if err != nil {
			return model.RemoveResult{}, err
		}
		defer unlock()
	}

	records, err := s.savepointRepo.List(ctx)
	if err != nil {
		return model.RemoveResult{}, fmt.Errorf("listing savepoints: %w", err)
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
	kept, removed = partitionSavepoints(records, req.RetainRecent, cutoffs)
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

// removeSavepoint drops one savepoint from the savepoint tier. The backends are
// built first: one that cannot be built leaves the savepoint whole.
func (s *Service) removeSavepoint(ctx context.Context, record model.Savepoint) error {
	backends, err := s.buildBackends(volumesOf(record), model.BackendTierSavepoint)
	if err != nil {
		return err
	}
	if err := s.dropSavepoint(ctx, s.savepointRepo, s.savepointStore, record, backends); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("savepoint", record.ID.String()).
			Msg("savepoint was uncommitted but its data could not be removed; the directory is orphaned")
		return err
	}
	return nil
}

// refLatest is the reserved ref that selects the newest savepoint by CreatedAt.
const refLatest = "latest"

// findSavepoint resolves a ref — refLatest, an exact id, or a name. Ids win,
// being unique; a name matching several is an error, never a silent guess.
func findSavepoint(records []model.Savepoint, ref string) (model.Savepoint, error) {
	if len(records) == 0 {
		return model.Savepoint{}, errors.New("no savepoints exist")
	}
	if ref == refLatest {
		latest := records[0]
		for _, r := range records[1:] {
			if r.CreatedAt.After(latest.CreatedAt) {
				latest = r
			}
		}
		return latest, nil
	}
	for _, r := range records {
		if r.ID.String() == ref {
			return r, nil
		}
	}
	var byName []model.Savepoint
	for _, r := range records {
		if r.Name == ref {
			byName = append(byName, r)
		}
	}
	switch len(byName) {
	case 0:
		return model.Savepoint{}, fmt.Errorf("savepoint not found: %s", ref)
	case 1:
		return byName[0], nil
	default:
		ids := make([]string, len(byName))
		for i, r := range byName {
			ids[i] = r.ID.String()
		}
		return model.Savepoint{}, fmt.Errorf("%d savepoints are named %q; roll back by id instead: %s", len(byName), ref, strings.Join(ids, ", "))
	}
}
