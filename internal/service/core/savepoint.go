package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	str2duration "github.com/xhit/go-str2duration/v2"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// maxConcurrentSavepointRemovals caps how many savepoints applyRemoval deletes in
// parallel. The per-mount concurrency of a single snapshot/restore is the
// backend's concern; this bounds the savepoint-level fan-out only.
const maxConcurrentSavepointRemovals = 5

// CreateSavepoint stops the project services, snapshots every persistent mount
// into a new savepoint, commits the savepoint's record, and starts the services
// again. Progress is logged via the logger carried by ctx; the created
// savepoint metadata is returned.
//
// It holds the project lock for the whole workflow, so a concurrent rollback
// cannot restart the services mid-snapshot. The lock is taken first: a caller who
// cannot have the project must not learn anything about it, and must certainly
// not stop its services.
func (s *Service) CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error) {
	log := zerolog.Ctx(ctx)

	unlock, err := s.lockProject(ctx)
	if err != nil {
		return model.Savepoint{}, err
	}
	defer unlock()

	insp, err := s.inspectStatefulMounts(ctx, req.ComposeFiles)
	if err != nil {
		return model.Savepoint{}, err
	}

	record, err := s.newSavepointRecord(req)
	if err != nil {
		return model.Savepoint{}, err
	}

	log.Info().Msg("stopping services")
	if err := s.composeRunner.Stop(ctx, req.ComposeFiles...); err != nil {
		return model.Savepoint{}, fmt.Errorf("stopping services: %w", err)
	}

	// Snapshot while the services are stopped. On failure the services still
	// must come back up, so restart them best-effort — a restart failure on top
	// changes nothing and is only logged — then report the snapshot error.
	if err := s.snapshotWhileStopped(ctx, insp, record); err != nil {
		log.Info().Msg("starting services")
		if err := s.composeRunner.Up(ctx, req.WaitTimeout, req.ComposeFiles...); err != nil {
			log.Error().Err(err).Msg("services failed to restart after snapshot failure")
		}
		return model.Savepoint{}, fmt.Errorf("snapshotting mounts: %w", err)
	}

	log.Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, req.WaitTimeout, req.ComposeFiles...); err != nil {
		return model.Savepoint{}, fmt.Errorf("starting services: %w", err)
	}

	return record.Savepoint, nil
}

// inspectStatefulMounts inspects the compose project and returns its persistent
// mounts. Mounts that cannot be snapshotted are logged and skipped; an empty
// result is an error, since there is nothing to snapshot.
func (s *Service) inspectStatefulMounts(ctx context.Context, composeFiles []string) (model.Inspection, error) {
	insp, err := s.composeInspector.Inspect(ctx, composeFiles...)
	if err != nil {
		return model.Inspection{}, err
	}

	for _, sk := range insp.Skipped {
		zerolog.Ctx(ctx).Warn().
			Str("service", sk.Service).
			Str("reason", sk.Reason).
			Msg("mount will not be backed up")
	}
	if len(insp.Mounts) == 0 {
		return model.Inspection{}, errors.New("no stateful mounts found; nothing to snapshot")
	}
	return insp, nil
}

// newSavepointRecord builds the record for a new savepoint. It reads no store:
// with one record per savepoint there is nothing to append the new one to.
func (s *Service) newSavepointRecord(req model.CreateSavepointRequest) (model.SavepointRecord, error) {
	now := s.now()
	sp := model.Savepoint{
		ID:          uniqueSavepointID(now),
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   now.UTC(),
	}
	return model.SavepointRecord{Savepoint: sp}, nil
}

// uniqueSavepointID returns a Unix-second savepoint ID. IDs double as the
// snapshot namespace, and the project lock serialises creates, so uniqueness
// rests on two creates never landing in the same second.
func uniqueSavepointID(now time.Time) string {
	return strconv.FormatInt(now.Unix(), 10)
}

// snapshotWhileStopped snapshots every mount into the savepoint and commits its
// record, while the services are stopped. The caller must restart the services
// regardless of the outcome.
//
// Three steps, in an order that is the whole crash-safety story:
//
//  1. Drop the namespace. A create killed by a crash on an earlier run can leave
//     an orphan data tree under this id — invisible to List, and therefore not
//     avoided by uniqueSavepointID — whose stale archives would otherwise be
//     mixed into the new savepoint.
//  2. Write every archive.
//  3. Save the record. It is the commit marker, so it is written *last*: a crash
//     before this leaves data no record names, which List skips and a future
//     step 1 (or a `gm savepoint gc`) reclaims.
//
// On failure the namespace is dropped again, so a partial snapshot can never be
// rolled back to.
func (s *Service) snapshotWhileStopped(
	ctx context.Context,
	insp model.Inspection,
	record model.SavepointRecord,
) error {
	if err := s.savepointBackend.Remove(ctx, record.ID); err != nil {
		return fmt.Errorf("clearing the savepoint directory: %w", err)
	}

	volumes, err := s.snapshotMounts(ctx, insp, record.ID)
	if err == nil {
		record.Volumes = volumes
		err = s.savepointRepo.Save(ctx, record)
	}
	if err != nil {
		if rmErr := s.savepointBackend.Remove(ctx, record.ID); rmErr != nil {
			zerolog.Ctx(ctx).Warn().Err(rmErr).Str("savepoint", record.ID).Msg("failed to remove partial snapshot")
		}
	}
	return err
}

// snapshotMounts snapshots every mount of the inspected project into the
// savepoint's directory, returning one VolumeRef per mount in mount order. The
// backend owns the per-mount fan-out; this only zips the stored sizes back onto
// the mounts to form the record entries.
func (s *Service) snapshotMounts(
	ctx context.Context,
	insp model.Inspection,
	savepointID string,
) ([]model.VolumeRef, error) {
	sizes, err := s.savepointBackend.Snapshot(ctx, savepointID, insp.ComposeProject, insp.Mounts)
	if err != nil {
		return nil, fmt.Errorf("exporting docker mounts/volumes: %w", err)
	}

	volumes := make([]model.VolumeRef, len(insp.Mounts))
	for i, m := range insp.Mounts {
		volumes[i] = model.VolumeRef{
			Service:   m.Service,
			MountPath: m.Target,
			Type:      m.Type,
			Source:    m.Source,
			SizeBytes: sizes[i],
		}
	}
	return volumes, nil
}

// ListSavepoints returns every committed savepoint's metadata.
func (s *Service) ListSavepoints(ctx context.Context) ([]model.Savepoint, error) {
	records, err := s.savepointRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	return savepointsOf(records), nil
}

// RemoveSavepoints deletes savepoints selected by req. In retention mode, when
// several rules are combined a savepoint survives if any rule retains it.
//
// A dry run takes no lock: it only reads, and it must not block on — or be
// blocked by — a rollback the user is watching. A real removal takes the lock
// before it lists, so the set it selects is the set it deletes.
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

	return model.RemoveResult{DryRun: req.DryRun, Removed: savepointsOf(removed)}, nil
}

// validateRemoveRequest rejects contradictory or incomplete remove requests:
// explicit IDs and retention rules are mutually exclusive, and at least one
// selector must be given. The errors it returns are the shared model sentinels,
// which the adapters match on.
func validateRemoveRequest(req model.RemoveRequest) error {
	// Checked before hasRetention, which counts only a *positive* RetainRecent: a
	// negative count would otherwise fall through and be reported as "no selector
	// provided".
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

// selectSavepointsToRemove splits records into the ones to keep and the ones to
// remove according to req: an explicit ID list when given, otherwise the
// retention rules. Only the removed half is destructive input; kept is returned
// so a caller can report or assert on the complement.
func (s *Service) selectSavepointsToRemove(
	records []model.SavepointRecord,
	req model.RemoveRequest,
) (kept, removed []model.SavepointRecord, err error) {
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
// each cutoff marks savepoints created before it as deletable. It reads the
// injected clock, so --retain-for is testable without wall-clock time.
func (s *Service) retentionCutoffs(req model.RemoveRequest) ([]time.Time, error) {
	var cutoffs []time.Time
	if req.RetainFor != "" {
		d, err := str2duration.ParseDuration(req.RetainFor)
		if err != nil {
			return nil, fmt.Errorf("invalid --retain-for %q: %w", req.RetainFor, err)
		}
		cutoffs = append(cutoffs, s.now().UTC().Add(-d))
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
	records []model.SavepointRecord,
	ids []string,
) (kept, removed []model.SavepointRecord, err error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	seen := make(map[string]bool, len(ids))
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
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("savepoint(s) not found: %s", strings.Join(missing, ", "))
	}
	return kept, removed, nil
}

// partitionSavepoints splits records into the ones to keep and the ones to
// remove. A record is removed only when every active rule marks it deletable:
// it is outside the retainRecent newest (when hasRecent) and older than every
// cutoff.
func partitionSavepoints(
	records []model.SavepointRecord,
	retainRecent int,
	cutoffs []time.Time,
) (kept, removed []model.SavepointRecord) {
	retainedByRecency := map[string]bool{}
	hasRecent := retainRecent > 0
	if hasRecent {
		byNewest := append([]model.SavepointRecord(nil), records...)
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

// applyRemoval performs the destructive step, uncommit-first: for each savepoint
// it deletes the record *before* the data.
//
// This inverts the order a monolithic manifest wanted. There, data went first so
// "the index never points at data that is gone", and that was safe only because
// rewriting the whole index was a single swap. With a per-savepoint marker,
// data-first is the dangerous path: a crash after some archives are gone but
// before the marker is removed leaves a savepoint that still lists and still
// resolves, whose rollback pre-flight then fails — or, if pre-flight is ever
// relaxed, restores a half-empty volume over live data. Uncommit-first makes the
// only observable states "fully present" and "gone".
//
// The cost is an orphaned namespace when the data delete fails after the record
// is gone: the savepoint no longer lists, so `gm savepoint remove <id>` cannot
// retry it. Those namespaces are reported here and reclaimed by a future
// `gm savepoint gc`.
func (s *Service) applyRemoval(ctx context.Context, removed []model.SavepointRecord) error {
	// Delete the savepoints concurrently. This is a plain errgroup.Group, not
	// WithContext: each removal is independent, and a cancelled sibling would abort
	// mid-delete and leave a half-deleted directory. Wait therefore surfaces only
	// the first failure, so each goroutine also records its own error at its own
	// index — that is what names the savepoints whose data survived.
	errs := make([]error, len(removed))
	g := new(errgroup.Group)
	g.SetLimit(maxConcurrentSavepointRemovals)
	for i, r := range removed {
		g.Go(func() error {
			errs[i] = s.removeSavepoint(ctx, r.ID)
			return errs[i]
		})
	}
	if err := g.Wait(); err != nil {
		return errors.Join(errs...)
	}
	return nil
}

// removeSavepoint deletes one savepoint: the record first, then the data it
// named. A failure after the record is gone leaves an orphaned directory, which
// is logged with the id so the user can reclaim the space.
func (s *Service) removeSavepoint(ctx context.Context, id string) error {
	if err := s.savepointRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("uncommit savepoint %s: %w", id, err)
	}
	if err := s.savepointBackend.Remove(ctx, id); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("savepoint", id).
			Msg("savepoint was uncommitted but its data could not be removed; the directory is orphaned")
		return fmt.Errorf("remove snapshot %s: %w", id, err)
	}
	return nil
}

// refLatest is the reserved ref that selects the newest savepoint by CreatedAt.
const refLatest = "latest"

// findSavepoint resolves a CLI ref to a savepoint record. The ref may be
// refLatest (the newest savepoint), an exact savepoint ID, or a savepoint name.
// IDs take precedence since they are unique; a name that matches several
// savepoints is an error rather than a silent guess, since rolling back to the
// wrong one is destructive.
func findSavepoint(records []model.SavepointRecord, ref string) (model.SavepointRecord, error) {
	if len(records) == 0 {
		return model.SavepointRecord{}, errors.New("no savepoints exist")
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
		if r.ID == ref {
			return r, nil
		}
	}
	var byName []model.SavepointRecord
	for _, r := range records {
		if r.Name == ref {
			byName = append(byName, r)
		}
	}
	switch len(byName) {
	case 0:
		return model.SavepointRecord{}, fmt.Errorf("savepoint not found: %s", ref)
	case 1:
		return byName[0], nil
	default:
		ids := make([]string, len(byName))
		for i, r := range byName {
			ids[i] = r.ID
		}
		return model.SavepointRecord{}, fmt.Errorf("%d savepoints are named %q; roll back by id instead: %s", len(byName), ref, strings.Join(ids, ", "))
	}
}
