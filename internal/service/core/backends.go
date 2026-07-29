package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// buildBackends builds one backend per volume, bound to tier, in input order. All
// up front: an unregistered backend fails before any byte moves.
func (s *Service) buildBackends(
	volumes []model.Volume, tier model.BackendTier,
) ([]model.VolumeSnapshotBackend, error) {
	backends := make([]model.VolumeSnapshotBackend, len(volumes))
	for i, volume := range volumes {
		backend, err := s.snapshotBackendRegistry.New(model.BackendSpec{
			SessionStore: s.savepointStore,
			SafetyStore:  s.safetyBackupStore,
			Volume:       volume,
			Tier:         tier,
		})
		if err != nil {
			return nil, fmt.Errorf("building %s backend for volume %s: %w", volume.Backend, volume.ObjectID, err)
		}
		backends[i] = backend
	}
	return backends, nil
}

// performSavepoint captures every volume and commits the record last: that write
// is the commit, so a run that died halfway leaves data no record names.
//
// repo and store are parameters, and must be one tier's pair, because both tiers
// take savepoints — a rollback's undo is a savepoint in the safety-backup tree,
// with a record of its own.
//
// It opens by refusing a tree the new id already names. A create killed by a
// crash can have left an orphan there: no record names it, so List cannot see it
// and the id created from the clock does not avoid it, and its stale archives
// would otherwise be mixed into this savepoint. Dropping it here would delete
// data nobody asked to lose, silently — so the run stops instead and names it.
//
// The record comes back even on error, filled as far as the fan-out got, for the
// caller's cleanup to address.
func (s *Service) performSavepoint(
	ctx context.Context,
	repo savepointRepository,
	store storages.Storager,
	backends []model.VolumeSnapshotBackend,
	name, description string,
) (model.Savepoint, error) {
	createdAt := time.Now()
	record := model.Savepoint{
		ID:          model.SavepointID(strconv.FormatInt(createdAt.Unix(), 10)),
		Name:        name,
		Description: description,
		CreatedAt:   createdAt,
		Stats:       make([]model.SnapshotStat, len(backends)),
	}

	if err := requireEmptyTree(ctx, store, record.ID); err != nil {
		return record, err
	}

	err := s.forEachBackend(ctx, record.ID, backends, "snapshot",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			stat, err := backend.Snapshot(ctx, model.BackendSnapshotRequest{SnapshotID: record.ID})
			if err != nil {
				return fmt.Errorf("capture volume: %w", err)
			}
			record.Stats[i] = stat
			return nil
		})
	if err != nil {
		return record, err
	}

	if err := repo.Save(ctx, record); err != nil {
		return record, fmt.Errorf("commit savepoint %s: %w", record.ID, err)
	}
	return record, nil
}

// requireComplete gates a restore on every volume of record being there, naming
// all the gaps at once rather than the first: a half-restore is worse than none.
func (s *Service) requireComplete(
	ctx context.Context, record model.Savepoint, backends []model.VolumeSnapshotBackend,
) error {
	present := make([]bool, len(backends))
	err := s.forEachBackend(ctx, record.ID, backends, "exists",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			ok, err := backend.Exists(ctx, model.BackendExistsRequest{SnapshotID: record.ID})
			if err != nil {
				return fmt.Errorf("check capture: %w", err)
			}
			present[i] = ok
			return nil
		})
	if err != nil {
		return err
	}
	if missing := absentVolumes(backends, present); len(missing) > 0 {
		zerolog.Ctx(ctx).Error().
			Str("snapshot_id", record.ID.String()).
			Interface("volumes", missing).
			Msg("no capture exists for these volumes")
		return model.ErrMissingVolumeInSnapshot
	}
	return nil
}

// restoreVolumes writes every volume back, each carrying the payload its own
// capture reported; stats index alike with backends.
func (s *Service) restoreVolumes(
	ctx context.Context, record model.Savepoint, backends []model.VolumeSnapshotBackend,
) error {
	return s.forEachBackend(ctx, record.ID, backends, "restore",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			if err := backend.Restore(ctx, model.BackendRestoreRequest{
				SnapshotID: record.ID,
				Payload:    record.Stats[i].Payload,
			}); err != nil {
				return fmt.Errorf("restore capture: %w", err)
			}
			return nil
		})
}

// dropSavepoint uncommits the record first, then drops the data it named.
// Reversing that leaves a savepoint that still lists but is half gone, so the
// order is pinned by a test.
//
// repo and store must be one tier's pair.
func (s *Service) dropSavepoint(
	ctx context.Context,
	repo savepointRepository,
	store storages.Storager,
	record model.Savepoint,
	backends []model.VolumeSnapshotBackend,
) error {
	if err := repo.Delete(ctx, record.ID); err != nil {
		return fmt.Errorf("uncommit savepoint %s: %w", record.ID, err)
	}
	return s.dropCaptures(ctx, store, record, backends)
}

// dropCaptures drops each backend's data, then sweeps the directory they shared.
// It is dropSavepoint without the uncommit, which is what a failed capture wants:
// no record was ever written, so there is nothing to uncommit.
//
// The sweep runs whatever the backends reported: it is what reclaims an archive a
// capture that died mid-fan-out left behind, which no stat names.
func (s *Service) dropCaptures(
	ctx context.Context,
	store storages.Storager,
	record model.Savepoint,
	backends []model.VolumeSnapshotBackend,
) error {
	removeErr := s.forEachBackend(ctx, record.ID, backends, "remove",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			if err := backend.Remove(ctx, model.BackendRemoveRequest{
				SnapshotID: record.ID,
				Payload:    record.Stats[i].Payload,
			}); err != nil {
				return fmt.Errorf("remove capture: %w", err)
			}
			return nil
		})

	sweepErr := sweepTree(ctx, store, record.ID)
	if sweepErr != nil {
		sweepErr = fmt.Errorf("remove capture directory %s: %w", record.ID, sweepErr)
	}
	return errors.Join(removeErr, sweepErr)
}

// forEachBackend runs op over every backend concurrently and waits for all of
// them, so the caller's next group starts only once this one finished
// everywhere. op gets the index to write into a caller-preallocated slice.
// snapshotID is validated here once, ahead of every dispatch.
func (s *Service) forEachBackend(
	ctx context.Context,
	snapshotID model.SavepointID,
	backends []model.VolumeSnapshotBackend,
	opName string,
	op func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error,
) error {
	if err := snapshotID.Validate(); err != nil {
		return fmt.Errorf("validate savepoint id: %w", err)
	}

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, backend := range backends {
		eg.Go(func() error {
			opCtx := volumeOpContext(egCtx, backend, snapshotID, opName)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Info().Msg("volume operation started")
			if err := op(opCtx, i, backend); err != nil {
				return fmt.Errorf("%s %s: %w", opName, backend.Volume().ObjectID, err)
			}
			log.Info().Dur("duration", time.Since(started)).Msg("volume operation finished")
			return nil
		})
	}
	return eg.Wait()
}

// volumeOpContext attributes every line the operation logs.
func volumeOpContext(
	ctx context.Context, backend model.VolumeSnapshotBackend, snapshotID model.SavepointID, opName string,
) context.Context {
	volume := backend.Volume()
	return zerolog.Ctx(ctx).With().
		Str("volume", volume.ObjectID).
		Str("backend", string(volume.Backend)).
		Str("snapshot_id", snapshotID.String()).
		Str("op", opName).
		Logger().
		WithContext(ctx)
}

func absentVolumes(backends []model.VolumeSnapshotBackend, present []bool) []model.Volume {
	var missing []model.Volume
	for i, backend := range backends {
		if !present[i] {
			missing = append(missing, backend.Volume())
		}
	}
	return missing
}

// requireEmptyTree refuses an id whose capture directory already holds objects.
// Only an interrupted run can have left them — a committed savepoint's id is
// never created twice — and they are the last copy of whatever it captured, so
// they are reported rather than deleted. An absent tree lists empty, which is
// the ordinary case.
func requireEmptyTree(ctx context.Context, store storages.Storager, id model.SavepointID) error {
	objects, err := store.List(ctx, id.String())
	if err != nil {
		return fmt.Errorf("reading the capture directory %s: %w", id, err)
	}
	if len(objects) > 0 {
		return fmt.Errorf("%w: %s holds %d object(s) from an interrupted run; remove it and retry",
			errCaptureDirectoryNotEmpty, id, len(objects))
	}
	return nil
}

// sweepTree drops a whole capture directory — the one storage key the service
// addresses itself; everything below it is the backends'. An absent tree is not
// an error.
func sweepTree(ctx context.Context, store storages.Storager, id model.SavepointID) error {
	if err := store.DeleteAll(ctx, id.String()); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return err
	}
	return nil
}
