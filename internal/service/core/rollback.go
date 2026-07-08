package core

import (
	"context"
	"fmt"
	"strconv"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// maxConcurrentVolumeOperations caps how many mount export/restore operations
// run in parallel, bounding pressure on Docker and the host filesystem.
const maxConcurrentVolumeOperations = 5

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
	safetyBackupID, safetyBackup, err := s.createSafetyBackup(ctx, currentMounts, composeProject)
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

		revertErr := s.revert(ctx, safetyBackup, req.ComposeFiles, composeProject, req.WaitTimeout)
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
// touched, so a missing archive aborts the rollback untouched.
func (s *Service) preflightArchives(ctx context.Context, record model.SavepointRecord) error {
	for _, v := range record.Volumes {
		exists, err := s.savepointBackend.Exists(ctx, v.Snapshot.SnapshotRef)
		if err != nil {
			return fmt.Errorf("checking savepoint archive for %s %s: %w", v.Service, v.MountPath, err)
		}
		if !exists {
			return fmt.Errorf("savepoint archive missing for %s %s", v.Service, v.MountPath)
		}
	}
	return nil
}

// safetyBackupMount pairs one mount of the safety backup with the backend handle
// to its stored data, so revert can restore it without recomputing the storage
// key.
type safetyBackupMount struct {
	mount model.Mount
	ref   model.SnapshotRef
}

// createSafetyBackup captures the current live state of every mount in the
// safety-backup store so a failed restore can be undone. It returns the backup id
// even on error, so the caller can clean up a partial backup.
func (s *Service) createSafetyBackup(
	ctx context.Context, currentMounts []model.Mount, composeProject string,
) (string, []safetyBackupMount, error) {
	id := strconv.FormatInt(s.now().Unix(), 10)
	backedUp := make([]safetyBackupMount, len(currentMounts))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for i, mt := range currentMounts {
		g.Go(func() error {
			zerolog.Ctx(gctx).Info().
				Str("service", mt.Service).
				Str("mount", mt.Target).
				Msg("backing up mount")

			meta, err := s.safetyBackupBackend.Create(gctx, model.VolumeSnapshotRequest{
				Namespace:      model.SafetyBackupNamespace(id),
				ComposeProject: composeProject,
				Volume: model.VolumeKey{
					Service:   mt.Service,
					MountPath: mt.Target,
				},
			})
			if err != nil {
				return fmt.Errorf("safety backup of %s %s: %w", mt.Service, mt.Target, err)
			}
			backedUp[i] = safetyBackupMount{mount: mt, ref: meta.SnapshotRef}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return id, nil, err
	}
	return id, backedUp, nil
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
// restore-vs-restart distinction.
func (s *Service) applySavepoint(
	ctx context.Context,
	record model.SavepointRecord,
	composeFiles []string,
	composeProject string,
	waitTimeout int,
) error {
	if err := s.restoreSavepoint(ctx, record, composeProject); err != nil {
		return err
	}
	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("services did not become healthy after restore: %w", err)
	}
	return nil
}

// restoreSavepoint extracts every savepoint volume back into its running
// container's mount. It restores the *savepoint's* archives (not the safety
// backup — that is revert's job).
func (s *Service) restoreSavepoint(
	ctx context.Context,
	record model.SavepointRecord,
	composeProject string,
) error {
	log := zerolog.Ctx(ctx)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for _, v := range record.Volumes {
		g.Go(func() error {
			log.Info().
				Str("service", v.Service).
				Str("mount", v.MountPath).
				Msg("restoring mount")

			return s.savepointBackend.Restore(gctx, model.VolumeRestoreRequest{
				ComposeProject: composeProject,
				Volume:         model.VolumeKey{Service: v.Service, MountPath: v.MountPath},
				Snapshot:       v.Snapshot.SnapshotRef,
			})
		})
	}
	return g.Wait()
}

// revert replays the safety backup to return the environment to its pre-rollback
// state: stop services, restore every backed-up mount, restart. nil means the
// environment is fully back to where it started; the caller pairs any error with
// the original rollback failure.
func (s *Service) revert(
	ctx context.Context,
	safetyBackup []safetyBackupMount,
	composeFiles []string,
	composeProject string,
	waitTimeout int,
) error {
	if err := s.composeRunner.Stop(ctx, composeFiles...); err != nil {
		return fmt.Errorf("stopping services: %w", err)
	}

	// gctx, not ctx: errgroup cancels its derived context as soon as Wait returns,
	// so reusing it for the restart below would fail with context.Canceled.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for _, m := range safetyBackup {
		g.Go(func() error {
			return s.safetyBackupBackend.Restore(gctx, model.VolumeRestoreRequest{
				ComposeProject: composeProject,
				Volume: model.VolumeKey{
					Service:   m.mount.Service,
					MountPath: m.mount.Target,
				},
				Snapshot: m.ref,
			})
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("restoring safety backup: %w", err)
	}

	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, waitTimeout, composeFiles...); err != nil {
		return fmt.Errorf("restarting services: %w", err)
	}
	return nil
}

// removeSafetyBackup deletes one safety backup's namespace. The shared
// safety-backup store is left in place once empty: pruning it would mean asking
// a Storager to delete its own root, the one key the directory backend refuses
// (directory.ErrUnsafeKey), and an empty .gm/tmp costs nothing.
func (s *Service) removeSafetyBackup(ctx context.Context, safetyBackupID string) error {
	return s.safetyBackupBackend.Remove(ctx, model.SafetyBackupNamespace(safetyBackupID))
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
