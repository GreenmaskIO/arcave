package core

import (
	"context"
	"fmt"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
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
// snapshot, tear the stack down, rebuild it from the spec the savepoint recorded,
// restore, start. Any failure past the teardown replays the safety snapshot; the
// project lock and the inflight marker stand across the whole workflow. The
// rebuild discards the containers' writable layer, which no savepoint holds.
func (s *Service) Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error) {
	// Ahead of the lock: a request that names no savepoint must not make a
	// concurrent workflow wait to be told so.
	if err := req.Validate(); err != nil {
		return model.RollbackResult{}, err
	}

	return withUnlock(ctx, s, func() (model.RollbackResult, error) {
		return s.rollback(ctx, req)
	})
}

// rollback is the workflow itself, run with the project lock held.
func (s *Service) rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)

	record, err := s.resolveSavepoint(ctx, req)
	if err != nil {
		return model.RollbackResult{}, err
	}

	if err := record.ID.Validate(); err != nil {
		return model.RollbackResult{}, fmt.Errorf("validate savepoint id: %w", err)
	}

	// After the resolve: the gate lets the marker's own remedy through, and which
	// savepoint that is is known only once the selector names a record.
	if err := s.requireNoInflight(ctx, record.ID); err != nil {
		return model.RollbackResult{}, fmt.Errorf("require no inflight: %w", err)
	}

	// Two backend sets over the one store, because the volume sets differ: one may
	// have been added or dropped since the savepoint was taken. Both ahead of the
	// stop — a backend that cannot be built leaves the project running.
	savepointBackends, err := s.buildBackends(volumesOf(record))
	if err != nil {
		return model.RollbackResult{}, fmt.Errorf("build backends: %w", err)
	}

	// Ahead of the stop: a rollback that cannot say what to rebuild must not tear
	// anything down.
	target, err := s.loadSavepointSpec(ctx, record)
	if err != nil {
		return model.RollbackResult{}, fmt.Errorf("load the savepoint's compose spec: %w", err)
	}

	ws, err := s.prepare(ctx, req.WaitTimeout)
	if err != nil {
		return model.RollbackResult{}, fmt.Errorf("prepare: %w", err)
	}

	if err := s.stopServices(ctx, ws); err != nil {
		return model.RollbackResult{}, fmt.Errorf("stop services: %w", err)
	}

	// A capture of the live state, so a rollback that fails partway can be replayed
	// back. Dropped on every path but a failed revert, and committing a record —
	// which is what leaves a killed rollback's undo listable, and so applicable by
	// hand.
	log.Info().Msg("backing up current state")
	safetySnapshot, err := s.performSavepoint(
		ctx, ws.backends, ws.spec, safetySnapshotName(record.ID), "", safetySnapshotLabels(),
	)
	if err != nil {
		// Nothing was restored, so there is nothing to revert: restart best-effort and
		// report the capture error. What the fan-out wrote stays for a later gc,
		// logged by the capture itself.
		if err := s.startServices(ctx, ws); err != nil {
			log.Error().Err(err).Msg("services failed to restart after safety snapshot failure")
		}
		return model.RollbackResult{}, fmt.Errorf("pre-rollback safety snapshot failed, nothing changed: %w", err)
	}

	// The marker must stand before the first destructive act, and the teardown is one.
	if err := s.putInflightMarker(ctx, model.InflightRecreating, record.ID, safetySnapshot.ID); err != nil {
		if abortErr := s.abortBeforeRestore(ctx, safetySnapshot, ws); abortErr != nil {
			log.Error().Err(abortErr).Msg("services failed to restart after inflight marker failure")
		}
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	if err := s.applySavepoint(ctx, record, target, savepointBackends, safetySnapshot.ID, ws); err != nil {
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
			if markErr := s.putInflightMarker(ctx, model.InflightInconsistent, record.ID, safetySnapshot.ID); markErr != nil {
				log.Warn().Err(markErr).Msg("failed to mark the inflight marker inconsistent")
			}
			log.Error().Err(revertErr).Str("safety_snapshot_id", safetySnapshot.ID.String()).
				Msg("revert failed: environment left in an inconsistent state, safety snapshot retained; " +
					"it lists as a savepoint under `arcave savepoint list --include-safety-snapshots` " +
					"and can be rolled back to by id")
			return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
		}

		log.Info().Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("reverted to the pre-rollback state")
		s.reapAnonymousVolumes(ctx, ws, volumesOf(safetySnapshot))
		s.releaseSafetySnapshot(ctx, safetySnapshot, ws.backends)
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
	}

	s.reapAnonymousVolumes(ctx, ws, volumesOf(record))
	s.releaseSafetySnapshot(ctx, safetySnapshot, ws.backends)
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

// abortBeforeRestore drops the committed safety snapshot and restarts the services;
// nothing was restored, so there is nothing to revert. The snapshot is whole here —
// a partial one is left for a gc instead, by the caller that produced it.
func (s *Service) abortBeforeRestore(ctx context.Context, safetySnapshot model.Savepoint, ws workingSet) error {
	if err := s.dropSavepoint(ctx, safetySnapshot, ws.backends); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("failed to remove the safety snapshot")
	}
	return s.startServices(ctx, ws)
}

// applySavepoint tears the stack down, rebuilds it from the savepoint's spec,
// restores every volume and starts the services; every failure shares the caller's
// one revert path. The rebuild goes first — an anonymous volume needs its container.
func (s *Service) applySavepoint(
	ctx context.Context,
	record model.Savepoint,
	target *types.Project,
	savepointBackends []model.VolumeSnapshotBackend,
	safetyID model.SavepointID,
	ws workingSet,
) error {
	if err := s.tearDownStack(ctx, ws.project); err != nil {
		return err
	}
	if err := s.buildStack(ctx, target); err != nil {
		return err
	}

	// Mandatory, unlike the phases that only relax one: a marker still reading
	// "recreating" would tell the next command the data is untouched.
	if err := s.putInflightMarker(ctx, model.InflightRestoring, record.ID, safetyID); err != nil {
		return fmt.Errorf("phasing the inflight marker to restoring: %w", err)
	}

	if err := s.restoreVolumes(ctx, record, savepointBackends); err != nil {
		return err
	}

	// Before the restart: a kill during the start must read as "the data is
	// consistent", not "mid-restore". A failed update keeps the safer phase.
	if err := s.putInflightMarker(ctx, model.InflightRestoreDone, record.ID, safetyID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker forward")
	}

	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Start(ctx, target, ws.waitTimeout); err != nil {
		return fmt.Errorf("services did not become healthy after restore: %w", err)
	}
	return nil
}

// revert replays the safety snapshot through the very instances that took it: tear
// down, rebuild the stack it was taken of, restore, restart. It runs on the caller's
// context, not the fan-out's — an errgroup cancels its own the moment Wait returns.
func (s *Service) revert(
	ctx context.Context,
	safetySnapshot model.Savepoint,
	ws workingSet,
	target model.SavepointID,
) error {
	// Back to recreating: the stack is about to be torn down again.
	if err := s.putInflightMarker(ctx, model.InflightRecreating, target, safetySnapshot.ID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker back before revert")
	}

	if err := s.tearDownStack(ctx, ws.project); err != nil {
		return err
	}
	if err := s.buildStack(ctx, ws.project); err != nil {
		return err
	}

	if err := s.putInflightMarker(ctx, model.InflightRestoring, target, safetySnapshot.ID); err != nil {
		return fmt.Errorf("phasing the inflight marker to restoring: %w", err)
	}
	if err := s.restoreVolumes(ctx, safetySnapshot, ws.backends); err != nil {
		return err
	}

	if err := s.putInflightMarker(ctx, model.InflightRestoreDone, target, safetySnapshot.ID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker forward")
	}
	return s.startServices(ctx, ws)
}

// reapAnonymousVolumes drops the anonymous volumes the teardown orphaned. Only
// those a capture was restored into: anything else there is its own last copy.
func (s *Service) reapAnonymousVolumes(ctx context.Context, ws workingSet, restored []model.Volume) {
	log := zerolog.Ctx(ctx)
	for _, volume := range restored {
		name, ok := ws.anonVolumes[volume.ObjectID]
		if !ok {
			continue
		}
		if err := s.volumeRemover.RemoveVolume(ctx, name); err != nil {
			log.Warn().Err(err).Str("volume", name).Msg("orphaned anonymous volume left behind")
			continue
		}
		log.Debug().Str("volume", name).Msg("removed the anonymous volume the teardown orphaned")
	}
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
