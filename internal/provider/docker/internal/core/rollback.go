package core

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// safetySnapshotPrefix opens the Name a rollback's undo carries; the id of the
// savepoint being restored completes it, so one a killed rollback abandoned says
// which savepoint it was undoing. The id and not the ref the user typed: once
// resolved, "latest" names no savepoint and a name may be one of several.
const safetySnapshotPrefix = "safety-snapshot"

// revertBudget bounds a revert that runs past its command's own context: a torn
// revert leaves the environment torn, so it gets a deadline instead of the caller's.
const revertBudget = 10 * time.Minute

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
//
// The inflight marker stands across the destructive window: a run killed inside it
// leaves the marker for the next command's gate.
func (s *Service) Rollback(ctx context.Context, req dockermodel.RollbackRequest) (dockermodel.RollbackResult, error) {
	// Ahead of the lock: a request that names no savepoint must not make a
	// concurrent workflow wait to be told so.
	if err := req.Validate(); err != nil {
		return dockermodel.RollbackResult{}, err
	}

	return withUnlock(ctx, s, func() (dockermodel.RollbackResult, error) {
		return s.rollback(ctx, req)
	})
}

// rollback is the workflow itself, run with the project lock held.
func (s *Service) rollback(ctx context.Context, req dockermodel.RollbackRequest) (dockermodel.RollbackResult, error) {
	log := zerolog.Ctx(ctx)

	record, err := s.resolveSavepoint(ctx, req)
	if err != nil {
		return dockermodel.RollbackResult{}, err
	}
	// The id gate, ahead of the whole workflow: a hand-planted record whose id
	// could name a parent directory fails before a backend is built or a service
	// stopped. forEachBackend re-checks ahead of every dispatch.
	if err := record.ID.Validate(); err != nil {
		return dockermodel.RollbackResult{}, fmt.Errorf("validate savepoint id: %w", err)
	}

	// After the resolve: the gate lets the marker's own remedy through, and which
	// savepoint that is is known only once the selector names a record.
	if err := s.requireNoInflight(ctx, record.ID); err != nil {
		return dockermodel.RollbackResult{}, fmt.Errorf("require no inflight: %w", err)
	}

	// Two backend sets over the one store, because the volume sets differ: one may
	// have been added or dropped since the savepoint was taken. Both ahead of the
	// stop — a backend that cannot be built leaves the project running.
	savepointBackends, err := s.buildBackends(volumesOf(record))
	if err != nil {
		return dockermodel.RollbackResult{}, fmt.Errorf("build backends: %w", err)
	}
	ws, err := s.prepare(ctx, req.ComposeFiles, req.WaitTimeout)
	if err != nil {
		return dockermodel.RollbackResult{}, fmt.Errorf("prepare: %w", err)
	}

	if err := s.stopServices(ctx, ws); err != nil {
		return dockermodel.RollbackResult{}, fmt.Errorf("stop services: %w", err)
	}

	// A capture of the live state, so a rollback that fails partway can be replayed
	// back. Dropped on every path but a failed revert, and committing a record —
	// which is what leaves a killed rollback's undo listable, and so applicable by
	// hand.
	log.Info().Msg("backing up current state")
	safetySnapshot, err := s.performSavepoint(
		ctx, ws.backends, safetySnapshotName(record.ID), "", safetySnapshotLabels(),
	)
	if err != nil {
		// Nothing was restored, so there is nothing to revert: restart best-effort and
		// report the capture error. What the fan-out wrote stays for a later gc,
		// logged by the capture itself.
		if err := s.startServices(ctx, ws); err != nil {
			log.Error().Err(err).Msg("services failed to restart after safety snapshot failure")
		}
		return dockermodel.RollbackResult{}, fmt.Errorf("pre-rollback safety snapshot failed, nothing changed: %w", err)
	}

	// The write-ahead bit, and it is strict: the marker must stand before the first
	// destructive write, or a kill past here is invisible to the next command's gate.
	if err := s.putInflightMarker(ctx, dockermodel.InflightRestoring, record.ID, safetySnapshot.ID); err != nil {
		if abortErr := s.abortBeforeRestore(ctx, safetySnapshot, ws); abortErr != nil {
			log.Error().Err(abortErr).Msg("services failed to restart after inflight marker failure")
		}
		return dockermodel.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	if err := s.applySavepoint(ctx, record, savepointBackends, safetySnapshot.ID, ws); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("rollback failed: reverting to the pre-rollback state")

		// Uncancellable, on its own budget: the very Ctrl-C aborting the rollback
		// would otherwise tear the revert meant to save the environment.
		revertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revertBudget)
		revertErr := s.revert(revertCtx, safetySnapshot, ws, record.ID)
		cancel()
		if revertErr != nil {
			// The one path that keeps the snapshot: it is now the last copy of the
			// pre-rollback state, and the marker stays with it.
			if markErr := s.putInflightMarker(ctx, dockermodel.InflightInconsistent, record.ID, safetySnapshot.ID); markErr != nil {
				log.Warn().Err(markErr).Msg("failed to mark the inflight marker inconsistent")
			}
			log.Error().Err(revertErr).Str("safety_snapshot_id", safetySnapshot.ID.String()).
				Msg("revert failed: environment left in an inconsistent state, safety snapshot retained; " +
					"it lists as a savepoint under `arcave savepoint list --include-safety-snapshots`" +
					"and can be rolled back to by id")
			return dockermodel.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
		}

		log.Info().Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("reverted to the pre-rollback state")
		s.releaseSafetySnapshot(ctx, safetySnapshot, ws.backends)
		return dockermodel.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
	}

	s.releaseSafetySnapshot(ctx, safetySnapshot, ws.backends)
	return dockermodel.RollbackResult{
		Savepoint: record,
		Restored:  volumesOf(record),
	}, nil
}

// resolveSavepoint resolves req's selector to the record to roll back to.
// Resolution stays in memory: the store has no index to query.
//
// It reads every record, safety snapshots included, and findSavepoint decides
// which of them this selector may reach.
func (s *Service) resolveSavepoint(ctx context.Context, req dockermodel.RollbackRequest) (dockermodel.Savepoint, error) {
	records, err := s.listSavepoints(ctx, true)
	if err != nil {
		return dockermodel.Savepoint{}, err
	}
	record, err := findSavepoint(records, req)
	if err != nil {
		return dockermodel.Savepoint{}, fmt.Errorf("finding savepoint %q: %w", req.Ref(), err)
	}
	return record, nil
}

// abortBeforeRestore drops the committed safety snapshot and restarts the services;
// nothing was restored, so there is nothing to revert. The snapshot is whole here —
// a partial one is left for a gc instead, by the caller that produced it.
func (s *Service) abortBeforeRestore(ctx context.Context, safetySnapshot dockermodel.Savepoint, ws workingSet) error {
	if err := s.dropSavepoint(ctx, safetySnapshot, ws.backends); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("failed to remove the safety snapshot")
	}
	return s.startServices(ctx, ws)
}

// applySavepoint restores every volume, then starts and health-checks the
// services: both failures share the caller's one revert path.
func (s *Service) applySavepoint(
	ctx context.Context,
	record dockermodel.Savepoint,
	savepointBackends []dockermodel.VolumeSnapshotBackend,
	safetyID model.SavepointID,
	ws workingSet,
) error {
	if err := s.restoreVolumes(ctx, record, savepointBackends); err != nil {
		return err
	}

	// Before the restart: a kill during Up must read as "the data is consistent",
	// not "mid-restore". A failed update keeps the conservative phase, so it warns.
	if err := s.putInflightMarker(ctx, dockermodel.InflightRestoreDone, record.ID, safetyID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker forward")
	}

	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, ws.waitTimeout, ws.composeFiles...); err != nil {
		return fmt.Errorf("services did not become healthy after restore: %w", err)
	}
	return nil
}

// revert replays the safety snapshot: stop, restore every volume through the
// instances that captured it, restart. It runs on the caller's context, not the
// fan-out's — an errgroup cancels its own the moment Wait returns.
func (s *Service) revert(
	ctx context.Context,
	safetySnapshot dockermodel.Savepoint,
	ws workingSet,
	target model.SavepointID,
) error {
	// Back to restoring: the revert writes volumes again, so a kill inside it must
	// not read as "the data is consistent".
	if err := s.putInflightMarker(ctx, dockermodel.InflightRestoring, target, safetySnapshot.ID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker back before revert")
	}

	if err := s.stopServices(ctx, ws); err != nil {
		return err
	}
	if err := s.restoreVolumes(ctx, safetySnapshot, ws.backends); err != nil {
		return err
	}
	return s.startServices(ctx, ws)
}

// volumesOf projects a savepoint's stats back to volumes. Non-nil, so an empty
// savepoint marshals restored as [] rather than null.
func volumesOf(record dockermodel.Savepoint) []dockermodel.Volume {
	volumes := make([]dockermodel.Volume, 0, len(record.Stats))
	for _, stat := range record.Stats {
		volumes = append(volumes, stat.Volume)
	}
	return volumes
}
