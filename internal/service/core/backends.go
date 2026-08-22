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

// buildBackends builds one backend per volume, all rooted at the one store, in
// input order. All up front: an unregistered backend fails before any byte moves.
func (s *Service) buildBackends(volumes []model.Volume) ([]model.VolumeSnapshotBackend, error) {
	backends := make([]model.VolumeSnapshotBackend, len(volumes))
	for i, volume := range volumes {
		backend, err := s.snapshotBackendRegistry.New(model.BackendSpec{
			Store:  s.store,
			Volume: volume,
		})
		if err != nil {
			return nil, fmt.Errorf("building %s backend for volume %s: %w", volume.Backend, volume.ObjectID, err)
		}
		backends[i] = backend
	}
	return backends, nil
}

// captureVolumes takes one capture per backend under a fresh id and returns the
// uncommitted savepoint they make up. On error the savepoint is zero: what a
// half-finished fan-out wrote is garbage no caller may address, logged here —
// the one place that knows the id — and left for a later gc.
//
// It opens by refusing a tree the new id already names — see requireEmptyTree.
func (s *Service) captureVolumes(
	ctx context.Context,
	backends []model.VolumeSnapshotBackend,
	name, description string,
	labels map[string]string,
) (model.Savepoint, error) {
	createdAt := time.Now()
	record := model.Savepoint{
		ID:          model.SavepointID(strconv.FormatInt(createdAt.Unix(), 10)),
		Name:        name,
		Description: description,
		Labels:      labels,
		CreatedAt:   createdAt,
		Stats:       make([]model.SnapshotStat, len(backends)),
	}

	if err := s.requireEmptyTree(ctx, record.ID); err != nil {
		return model.Savepoint{}, err
	}

	err := s.forEachBackend(ctx, record.ID, backends, "snapshot",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			stat, err := backend.Snapshot(ctx, model.BackendSnapshotRequest{
				SnapshotID: record.ID,
				Labels:     record.Labels,
			})
			if err != nil {
				return fmt.Errorf("capture volume: %w", err)
			}
			record.Stats[i] = stat
			return nil
		})
	if err != nil {
		// No record names what the fan-out wrote, so nothing reads it, and a failure
		// path is the worst moment to delete data.
		zerolog.Ctx(ctx).Warn().Str("snapshot_id", record.ID.String()).
			Msg("the partial capture is retained for a later gc")
		return model.Savepoint{}, err
	}
	return record, nil
}

// performSavepoint captures every volume, writes the stack's compose spec, and
// commits the record last: that write is the commit, so a run that died halfway
// leaves data no record names.
//
// A rollback's safety snapshot goes through it too, labelled rather than special:
// the record is what makes the backup a killed rollback abandoned listable, and
// therefore rollable-back-to by hand.
//
// On error the record is zero: an uncommitted capture is garbage, not a value.
func (s *Service) performSavepoint(
	ctx context.Context,
	backends []model.VolumeSnapshotBackend,
	spec []byte,
	name string,
	description string,
	labels map[string]string,
) (model.Savepoint, error) {
	record, err := s.captureVolumes(ctx, backends, name, description, labels)
	if err != nil {
		return model.Savepoint{}, err
	}

	if err := s.savepointRepo.SaveSpec(ctx, record.ID, spec); err != nil {
		return model.Savepoint{}, fmt.Errorf("write compose spec of savepoint %s: %w", record.ID, err)
	}
	record.Spec = &model.SpecRef{Format: model.SpecFormatComposeYAML, Digest: specDigest(spec)}

	if err := s.savepointRepo.SaveSavepoint(ctx, record); err != nil {
		return model.Savepoint{}, fmt.Errorf("commit savepoint %s: %w", record.ID, err)
	}
	return record, nil
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
// It is also what a failed capture wants: Delete is idempotent — a namespace
// with no record is already uncommitted — so one function serves both.
//
// The sweep runs whatever the backends reported: it is what reclaims an archive a
// capture that died mid-fan-out left behind, which no stat names.
func (s *Service) dropSavepoint(
	ctx context.Context,
	record model.Savepoint,
	backends []model.VolumeSnapshotBackend,
) error {
	if err := s.savepointRepo.DeleteSavepoint(ctx, record.ID); err != nil {
		return fmt.Errorf("uncommit savepoint %s: %w", record.ID, err)
	}

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

	sweepErr := s.sweepTree(ctx, record.ID)
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

// requireEmptyTree refuses an id whose capture directory already holds objects:
// an interrupted run's remains, or — an id being Unix seconds — a live savepoint
// captured and rolled back inside one tick. Telling them apart needs the reserved
// record key, which this layer may not name, so the message advises neither removal.
func (s *Service) requireEmptyTree(ctx context.Context, id model.SavepointID) error {
	objects, err := s.store.List(ctx, id.String())
	if err != nil {
		return fmt.Errorf("reading the capture directory %s: %w", id, err)
	}
	if len(objects) > 0 {
		return fmt.Errorf("%w: %s holds %d object(s) — a savepoint of this same second, or an interrupted run's remains; retry, and check what is there before removing it",
			errCaptureDirectoryNotEmpty, id, len(objects))
	}
	return nil
}

// sweepTree drops a whole capture directory — the one storage key the service
// addresses itself; everything below it is the backends'. An absent tree is not
// an error.
func (s *Service) sweepTree(ctx context.Context, id model.SavepointID) error {
	if err := s.store.DeleteAll(ctx, id.String()); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return err
	}
	return nil
}
