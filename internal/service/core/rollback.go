package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// safetyBackupName is the Name a rollback's undo commits its record under, for
// whoever reads that record after a rollback died mid-flight.
const safetyBackupName = "safety-backup"

// Rollback restores the environment to the referenced savepoint: stop, safety
// backup, restore, start. Any failure replays the safety backup. The project
// lock is held for the whole workflow, taken before anything is read.
func (s *Service) Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)

	unlock, err := s.lockProject(ctx)
	if err != nil {
		return model.RollbackResult{}, err
	}
	defer unlock()

	record, err := s.resolveSavepoint(ctx, req.Ref)
	if err != nil {
		return model.RollbackResult{}, err
	}
	// The live state, decided exactly as a create would: a safety backup covering
	// less than a savepoint could not put the project back.
	currentVolumes, err := s.resolveBackupVolumes(ctx, req.ComposeFiles)
	if err != nil {
		return model.RollbackResult{}, err
	}

	// One set of backends per tier: a volume may have been added or dropped
	// since the savepoint was taken.
	savepointBackends, err := s.buildBackends(volumesOf(record), model.BackendTierSavepoint)
	if err != nil {
		return model.RollbackResult{}, err
	}
	safetyBackends, err := s.buildBackends(currentVolumes, model.BackendTierSafety)
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

	// A savepoint of the live state, so a rollback that fails partway can be
	// replayed back. Dropped on every path but a failed revert.
	log.Info().Msg("backing up current state")
	safetyBackup, err := s.performSavepoint(ctx, s.safetyBackupRepo, s.safetyBackupStore, safetyBackends, safetyBackupName, "")
	if err != nil {
		// Nothing changed: restart best-effort and report the backup error. A
		// refused capture directory holds objects this run did not write, and
		// keeping them is the point of the refusal, so the abort leaves them.
		dropBackup := !errors.Is(err, errCaptureDirectoryNotEmpty)
		if err := s.abortBeforeRestore(ctx, safetyBackup, safetyBackends, req.ComposeFiles, req.WaitTimeout, dropBackup); err != nil {
			log.Error().Err(err).Msg("services failed to restart after safety backup failure")
		}
		return model.RollbackResult{}, fmt.Errorf("pre-rollback safety backup failed, nothing changed: %w", err)
	}

	if err := s.applySavepoint(ctx, record, savepointBackends, req.ComposeFiles, req.WaitTimeout); err != nil {
		log.Warn().Err(err).Str("safety_backup_id", safetyBackup.ID.String()).
			Msg("rollback failed: reverting to the pre-rollback state")

		revertErr := s.revert(ctx, safetyBackup, safetyBackends, req.ComposeFiles, req.WaitTimeout)
		if revertErr != nil {
			// The one path that keeps the backup: it is now the last copy of the
			// pre-rollback state.
			log.Error().Err(revertErr).Str("safety_backup_id", safetyBackup.ID.String()).
				Msg("revert failed: environment left in an inconsistent state, safety backup retained")
			return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
		}

		log.Info().Str("safety_backup_id", safetyBackup.ID.String()).Msg("reverted to the pre-rollback state")
		if err := s.dropSafetyBackup(ctx, safetyBackup, safetyBackends); err != nil {
			log.Warn().Err(err).Str("safety_backup_id", safetyBackup.ID.String()).Msg("failed to remove safety backup")
		}
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
	}

	if err := s.dropSafetyBackup(ctx, safetyBackup, safetyBackends); err != nil {
		log.Warn().Err(err).Str("safety_backup_id", safetyBackup.ID.String()).Msg("failed to remove safety backup")
	}
	return model.RollbackResult{
		Savepoint: record,
		Restored:  volumesOf(record),
	}, nil
}

// resolveSavepoint resolves ref to the record to roll back to. Resolution stays
// in memory: the store has no index to query.
func (s *Service) resolveSavepoint(ctx context.Context, ref string) (model.Savepoint, error) {
	records, err := s.savepointRepo.List(ctx)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("listing savepoints: %w", err)
	}
	record, err := findSavepoint(records, ref)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("finding savepoint %q: %w", ref, err)
	}
	return record, nil
}

// abortBeforeRestore drops the partial backup and restarts the services; nothing
// was restored, so there is nothing to revert. dropBackup is false when the
// backup never started because its directory was refused: those objects are not
// this run's to delete.
func (s *Service) abortBeforeRestore(
	ctx context.Context,
	safetyBackup model.Savepoint,
	safetyBackends []model.VolumeSnapshotBackend,
	composeFiles []string,
	waitTimeout time.Duration,
	dropBackup bool,
) error {
	log := zerolog.Ctx(ctx)
	if dropBackup {
		if err := s.dropSafetyBackup(ctx, safetyBackup, safetyBackends); err != nil {
			log.Warn().Err(err).Str("safety_backup_id", safetyBackup.ID.String()).Msg("failed to remove incomplete safety backup")
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

// revert replays the safety backup: stop, restore every volume through the
// instances that captured it, restart. It runs on the caller's context, not the
// fan-out's — an errgroup cancels its own the moment Wait returns.
func (s *Service) revert(
	ctx context.Context,
	safetyBackup model.Savepoint,
	safetyBackends []model.VolumeSnapshotBackend,
	composeFiles []string,
	waitTimeout time.Duration,
) error {
	if err := s.composeRunner.Stop(ctx, composeFiles...); err != nil {
		return fmt.Errorf("stopping services: %w", err)
	}
	// One missing capture would make this a half-revert: a mix of savepoint and
	// live data.
	if err := s.requireComplete(ctx, safetyBackup, safetyBackends); err != nil {
		return fmt.Errorf("safety backup incomplete, nothing reverted: %w", err)
	}
	if err := s.restoreVolumes(ctx, safetyBackup, safetyBackends); err != nil {
		return err
	}

	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("restarting services: %w", err)
	}
	return nil
}

// dropSafetyBackup pairs the safety tier's repository with its store; crossing
// the tiers would uncommit a record in one tree and sweep the other.
func (s *Service) dropSafetyBackup(
	ctx context.Context, safetyBackup model.Savepoint, safetyBackends []model.VolumeSnapshotBackend,
) error {
	return s.dropSavepoint(ctx, s.safetyBackupRepo, s.safetyBackupStore, safetyBackup, safetyBackends)
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
