package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// Rollback restores the environment to the referenced savepoint. It stops the
// services, takes a safety backup of their current state so a failed restore can
// be undone, wipes and re-extracts each savepoint volume, then restarts the
// services. If any restore step fails, the safety backup is replayed to return
// the environment to where it started. waitTimeout bounds the health check on
// restart; progress is logged via the logger carried by ctx.
//
// It holds the project lock for the whole workflow, taken before anything is
// read: a rollback that cannot have the project must not stop its services, and
// must not race a concurrent savepoint create over `docker compose stop/up`.
//
// "Safety backup" is the only kind of backup this flow takes — every symbol below
// carries the full name, so nothing reads as a second, different backup.
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
	insp, err := s.composeInspector.Inspect(ctx, req.ComposeFiles...)
	if err != nil {
		return model.RollbackResult{}, fmt.Errorf("inspecting compose files: %w", err)
	}
	currentMounts, composeProject := insp.Mounts, insp.ComposeProject

	if err := s.preflightArchives(ctx, record); err != nil {
		return model.RollbackResult{}, err
	}

	log.Info().Msg("stopping services")
	if err := s.composeRunner.Stop(ctx, req.ComposeFiles...); err != nil {
		return model.RollbackResult{}, fmt.Errorf("stopping services: %w", err)
	}

	// The safety backup captures the current live state of every mount before we
	// touch anything, so a rollback that fails partway (a bad restore, or services
	// that never come up healthy) can be replayed back to the pre-rollback state.
	// It is consumed by revert below and deleted only on full success.
	safetyBackupID, err := s.createSafetyBackup(ctx, currentMounts, composeProject)
	if err != nil {
		// The safety backup failed before anything was restored, so nothing
		// changed. Bring the services back up best-effort — a restart failure on
		// top changes nothing and is only logged — then report the backup error.
		if err := s.abortBeforeRestore(ctx, req.ComposeFiles, safetyBackupID, req.WaitTimeout); err != nil {
			log.Error().Err(err).Msg("services failed to restart after safety backup failure")
		}
		return model.RollbackResult{}, fmt.Errorf("pre-rollback safety backup failed, nothing changed: %w", err)
	}

	// Restore + start; on any failure, revert to the safety backup.
	if err := s.applySavepoint(ctx, record, req.ComposeFiles, composeProject, req.WaitTimeout); err != nil {
		log.Warn().Err(err).Str("safety_backup_id", safetyBackupID).
			Msg("rollback failed; reverting to the pre-rollback state")

		revertErr := s.revert(ctx, safetyBackupID, currentMounts, req.ComposeFiles, composeProject, req.WaitTimeout)
		if revertErr != nil {
			log.Error().Err(revertErr).Str("safety_backup_id", safetyBackupID).
				Msg("revert failed; environment left in an inconsistent state, safety backup retained")
		} else {
			log.Info().Str("safety_backup_id", safetyBackupID).
				Msg("reverted to the pre-rollback state, safety backup retained")
		}
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
	}

	if err := s.removeSafetyBackup(ctx, safetyBackupID); err != nil {
		log.Warn().Err(err).Str("safety_backup_id", safetyBackupID).Msg("failed to remove safety backup")
	}
	return model.RollbackResult{
		Savepoint: record.Savepoint,
		Restored:  restoredMounts(record),
	}, nil
}

// resolveSavepoint lists the committed savepoints and resolves ref to the record
// to roll back to. Resolution stays in memory: the store has no index to query,
// and findSavepoint already takes the whole slice.
func (s *Service) resolveSavepoint(ctx context.Context, ref string) (model.SavepointRecord, error) {
	records, err := s.savepointRepo.List(ctx)
	if err != nil {
		return model.SavepointRecord{}, err
	}
	record, err := findSavepoint(records, ref)
	if err != nil {
		return model.SavepointRecord{}, fmt.Errorf("finding savepoint %q: %w", ref, err)
	}
	return record, nil
}

// preflightArchives verifies every savepoint archive exists before anything is
// touched, so a missing archive aborts the rollback untouched. It names every gap
// at once rather than aborting on the first.
func (s *Service) preflightArchives(ctx context.Context, record model.SavepointRecord) error {
	missing, err := s.savepointBackend.Missing(ctx, record.ID, mountsOf(record.Volumes))
	if err != nil {
		return fmt.Errorf("checking savepoint archives: %w", err)
	}
	if len(missing) > 0 {
		names := make([]string, len(missing))
		for i, m := range missing {
			names[i] = m.Service + " " + m.Target
		}
		return fmt.Errorf("savepoint archive missing for %s", strings.Join(names, ", "))
	}
	return nil
}

// createSafetyBackup captures the current live state of every mount in the
// safety-backup store so a failed restore can be undone, returning the backup id.
// The id doubles as the safety backup's storage directory, so revert re-addresses
// each mount from it and the same currentMounts — there is no per-mount handle to
// carry. The id is returned even on error, so the caller can clean up a partial
// backup.
func (s *Service) createSafetyBackup(
	ctx context.Context, currentMounts []model.Mount, composeProject string,
) (string, error) {
	id := strconv.FormatInt(s.now().Unix(), 10)
	zerolog.Ctx(ctx).Info().Msg("backing up current state")
	if _, err := s.safetyBackupBackend.Snapshot(ctx, id, composeProject, currentMounts); err != nil {
		return id, err
	}
	return id, nil
}

// abortBeforeRestore recovers from a failed safety backup, before any savepoint
// volume was restored: it drops the partial backup and restarts the services.
// Nothing was changed, so there is nothing to revert. nil means the services
// are back up; the caller pairs any error with the original backup failure.
func (s *Service) abortBeforeRestore(ctx context.Context, composeFiles []string, safetyBackupID string, waitTimeout int) error {
	log := zerolog.Ctx(ctx)
	if err := s.removeSafetyBackup(ctx, safetyBackupID); err != nil {
		log.Warn().Err(err).Str("safety_backup_id", safetyBackupID).Msg("failed to remove incomplete safety backup")
	}
	log.Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("restarting services: %w", err)
	}
	return nil
}

// applySavepoint restores every savepoint volume and then starts and
// health-checks the services. Both steps share the same revert path, so merging
// them gives the caller a single revert call site. A restore failure is returned
// raw; an unhealthy start is wrapped, so the returned error carries the
// restore-vs-restart distinction. The backend owns the per-mount fan-out; this
// only names the savepoint's mounts (not the safety backup — that is revert's job).
func (s *Service) applySavepoint(
	ctx context.Context,
	record model.SavepointRecord,
	composeFiles []string,
	composeProject string,
	waitTimeout int,
) error {
	if err := s.savepointBackend.Restore(ctx, record.ID, composeProject, mountsOf(record.Volumes)); err != nil {
		return err
	}
	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("services did not become healthy after restore: %w", err)
	}
	return nil
}

// revert replays the safety backup to return the environment to its pre-rollback
// state: stop services, restore every backed-up mount, restart. It re-addresses
// each mount from the safety backup id and the same currentMounts createSafetyBackup
// captured, since the storage key is derived, not stored. nil means the environment
// is fully back to where it started; the caller pairs any error with the original
// rollback failure.
func (s *Service) revert(
	ctx context.Context,
	safetyBackupID string,
	currentMounts []model.Mount,
	composeFiles []string,
	composeProject string,
	waitTimeout int,
) error {
	if err := s.composeRunner.Stop(ctx, composeFiles...); err != nil {
		return fmt.Errorf("stopping services: %w", err)
	}
	if err := s.safetyBackupBackend.Restore(ctx, safetyBackupID, composeProject, currentMounts); err != nil {
		return fmt.Errorf("restoring safety backup: %w", err)
	}
	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("restarting services: %w", err)
	}
	return nil
}

// removeSafetyBackup deletes one safety backup's directory. The shared
// safety-backup store is left in place once empty: pruning it would mean asking
// a Storager to delete its own root, the one key the directory backend refuses
// (directory.ErrUnsafeKey), and an empty .arcave/tmp costs nothing.
func (s *Service) removeSafetyBackup(ctx context.Context, safetyBackupID string) error {
	return s.safetyBackupBackend.Remove(ctx, safetyBackupID)
}

// restoredMounts projects the savepoint's volumes to the result's mount list.
func restoredMounts(savepointRecord model.SavepointRecord) []model.Mount {
	restored := make([]model.Mount, 0, len(savepointRecord.Volumes))
	for _, v := range savepointRecord.Volumes {
		restored = append(restored, model.Mount{
			Service: v.Service,
			Target:  v.MountPath,
		})
	}
	return restored
}

// mountsOf projects a record's volumes back to the mounts the backend addresses,
// so pre-flight and restore can pass the whole savepoint at once.
func mountsOf(vols []model.VolumeRef) []model.Mount {
	mounts := make([]model.Mount, len(vols))
	for i, v := range vols {
		mounts[i] = model.Mount{Service: v.Service, Target: v.MountPath, Type: v.Type, Source: v.Source}
	}
	return mounts
}
