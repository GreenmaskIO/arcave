package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// safetySnapshotPrefix opens the Name a rollback's undo carries; the id of the
// savepoint being restored completes it, so one a killed rollback abandoned says
// which savepoint it was undoing. The id and not the ref the user typed: once
// resolved, "latest" names no savepoint and a name may be one of several.
const safetySnapshotPrefix = "safety-snapshot"

func safetySnapshotName(restoring model.SavepointID) string {
	return safetySnapshotPrefix + "-" + restoring.String()
}

// safetySnapshotLabels is the mark that makes an ordinary savepoint an undo: it
// hides the record from every scanning selector, and a backend with somewhere to
// put it renders it into its own store (cdc into a restic tag).
func safetySnapshotLabels() map[string]string {
	return map[string]string{model.SafetySnapshotLabel: model.SafetySnapshotLabelValue}
}

// Rollback restores the environment to the referenced savepoint: stop, safety
// snapshot, restore, start. Any failure replays the safety snapshot. The project
// lock is held for the whole workflow, taken before anything is read.
func (s *Service) Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)

	// Ahead of the lock: a request that names no savepoint must not make a
	// concurrent workflow wait to be told so.
	if err := req.Validate(); err != nil {
		return model.RollbackResult{}, err
	}

	unlock, err := s.lockProject(ctx)
	if err != nil {
		return model.RollbackResult{}, err
	}
	defer unlock()

	record, err := s.resolveSavepoint(ctx, req)
	if err != nil {
		return model.RollbackResult{}, err
	}
	// The live state, decided exactly as a create would: a safety snapshot covering
	// less than a savepoint could not put the project back.
	currentVolumes, err := s.resolveBackupVolumes(ctx, req.ComposeFiles)
	if err != nil {
		return model.RollbackResult{}, err
	}

	// Two sets over the one store, because the volume sets differ: one may have
	// been added or dropped since the savepoint was taken. Both are built ahead of
	// the stop — a backend that cannot be built leaves the project running.
	savepointBackends, err := s.buildBackends(volumesOf(record))
	if err != nil {
		return model.RollbackResult{}, err
	}
	safetySnapshotBackends, err := s.buildBackends(currentVolumes)
	if err != nil {
		return model.RollbackResult{}, err
	}

	if err := s.requireComplete(ctx, record, savepointBackends); err != nil {
		return model.RollbackResult{}, err
	}

	log.Info().Msg("stopping services")
	if err := s.composeRunner.Stop(ctx, req.ComposeFiles...); err != nil {
		return model.RollbackResult{}, fmt.Errorf("stopping services: %w", err)
	}

	// A capture of the live state, so a rollback that fails partway can be replayed
	// back. Dropped on every path but a failed revert, and committing a record —
	// which is what leaves a killed rollback's undo listable, and so applicable by
	// hand.
	log.Info().Msg("backing up current state")
	safetySnapshot, err := s.performSavepoint(
		ctx, safetySnapshotBackends, safetySnapshotName(record.ID), "", safetySnapshotLabels())
	if err != nil {
		// Nothing changed: restart best-effort and report the capture error. A refused
		// capture directory holds objects this run did not write, and keeping them is
		// the point of the refusal, so the abort leaves them.
		dropSnapshot := !errors.Is(err, errCaptureDirectoryNotEmpty)
		if err := s.abortBeforeRestore(ctx, safetySnapshot, safetySnapshotBackends, req.ComposeFiles, req.WaitTimeout, dropSnapshot); err != nil {
			log.Error().Err(err).Msg("services failed to restart after safety snapshot failure")
		}
		return model.RollbackResult{}, fmt.Errorf("pre-rollback safety snapshot failed, nothing changed: %w", err)
	}

	if err := s.applySavepoint(ctx, record, savepointBackends, req.ComposeFiles, req.WaitTimeout); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("rollback failed: reverting to the pre-rollback state")

		revertErr := s.revert(ctx, safetySnapshot, safetySnapshotBackends, req.ComposeFiles, req.WaitTimeout)
		if revertErr != nil {
			// The one path that keeps the snapshot: it is now the last copy of the
			// pre-rollback state.
			log.Error().Err(revertErr).Str("safety_snapshot_id", safetySnapshot.ID.String()).
				Msg("revert failed: environment left in an inconsistent state, safety snapshot retained; " +
					"it lists as a savepoint under `arcave savepoint list --include-safety-snapshots` and can be rolled back to by id")
			return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
		}

		log.Info().Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("reverted to the pre-rollback state")
		if err := s.dropSavepoint(ctx, safetySnapshot, safetySnapshotBackends); err != nil {
			log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("failed to remove safety snapshot")
		}
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
	}

	if err := s.dropSavepoint(ctx, safetySnapshot, safetySnapshotBackends); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("failed to remove safety snapshot")
	}
	return model.RollbackResult{
		Savepoint: record,
		Restored:  volumesOf(record),
	}, nil
}

// resolveSavepoint resolves req's selector to the record to roll back to.
// Resolution stays in memory: the store has no index to query.
//
// It reads every record, safety snapshots included, and findSavepoint decides
// which of them this selector may reach.
func (s *Service) resolveSavepoint(ctx context.Context, req model.RollbackRequest) (model.Savepoint, error) {
	records, err := s.listSavepoints(ctx, true)
	if err != nil {
		return model.Savepoint{}, err
	}
	record, err := findSavepoint(records, req)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("finding savepoint %q: %w", req.Ref(), err)
	}
	return record, nil
}

// abortBeforeRestore drops the partial snapshot and restarts the services; nothing
// was restored, so there is nothing to revert. dropSnapshot is false when the
// snapshot never started because its id was refused: whatever sits under that id
// is not this run's to delete.
func (s *Service) abortBeforeRestore(
	ctx context.Context,
	safetySnapshot model.Savepoint,
	safetySnapshotBackends []model.VolumeSnapshotBackend,
	composeFiles []string,
	waitTimeout time.Duration,
	dropSnapshot bool,
) error {
	log := zerolog.Ctx(ctx)
	if dropSnapshot {
		if err := s.dropSavepoint(ctx, safetySnapshot, safetySnapshotBackends); err != nil {
			log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("failed to remove incomplete safety snapshot")
		}
	}
	log.Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("restarting services: %w", err)
	}
	return nil
}

// applySavepoint restores every volume, then starts and health-checks the
// services: both failures share the caller's one revert path.
func (s *Service) applySavepoint(
	ctx context.Context,
	record model.Savepoint,
	savepointBackends []model.VolumeSnapshotBackend,
	composeFiles []string,
	waitTimeout time.Duration,
) error {
	if err := s.restoreVolumes(ctx, record, savepointBackends); err != nil {
		return err
	}

	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("services did not become healthy after restore: %w", err)
	}
	return nil
}

// revert replays the safety snapshot: stop, restore every volume through the
// instances that captured it, restart. It runs on the caller's context, not the
// fan-out's — an errgroup cancels its own the moment Wait returns.
func (s *Service) revert(
	ctx context.Context,
	safetySnapshot model.Savepoint,
	safetySnapshotBackends []model.VolumeSnapshotBackend,
	composeFiles []string,
	waitTimeout time.Duration,
) error {
	if err := s.composeRunner.Stop(ctx, composeFiles...); err != nil {
		return fmt.Errorf("stopping services: %w", err)
	}
	// One missing capture would make this a half-revert: a mix of savepoint and
	// live data.
	if err := s.requireComplete(ctx, safetySnapshot, safetySnapshotBackends); err != nil {
		return fmt.Errorf("safety snapshot incomplete, nothing reverted: %w", err)
	}
	if err := s.restoreVolumes(ctx, safetySnapshot, safetySnapshotBackends); err != nil {
		return err
	}

	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("restarting services: %w", err)
	}
	return nil
}

// volumesOf projects a savepoint's stats back to volumes. Non-nil, so an empty
// savepoint marshals restored as [] rather than null.
func volumesOf(record model.Savepoint) []model.Volume {
	volumes := make([]model.Volume, 0, len(record.Stats))
	for _, stat := range record.Stats {
		volumes = append(volumes, stat.Volume)
	}
	return volumes
}
