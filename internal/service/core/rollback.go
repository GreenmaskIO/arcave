// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// revertBudget bounds a revert that runs past its command's own context: a torn
// revert leaves the environment torn, so it gets a deadline instead of the caller's.
const revertBudget = 10 * time.Minute

// noSafetySnapshotWarning says what a rollback taking no safety snapshot risks.
const noSafetySnapshotWarning = "no safety snapshot: the rollback cannot be undone, and after a failure " +
	"running the same rollback again is the way back"

// newSafetySnapshotLabels is the mark that makes an ordinary savepoint an undo: it
// hides the record from every scanning selector, and a backend with somewhere to
// put it renders it into its own store (cdc into a restic tag).
func newSafetySnapshotLabels() map[string]string {
	return map[string]string{
		model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue,
	}
}

// Rollback restores the compose project to the referenced savepoint, putting the safety snapshot
// back if it fails after `compose down`; a project with no containers goes to rollbackColdStart.
// The snapshot stays behind as the rollback's undo, for a gc to retire.
func (s *Service) Rollback(ctx context.Context, req model.RollbackRequest) (_ model.RollbackResult, err error) {
	// Outside the lock: a lost lock cancels the work under it, and the root says why.
	ctx, step := progress.Begin(ctx, progress.Step{
		Kind: kindRollback,
		Name: req.Ref(),
	})
	defer func() { step.End(err) }()
	// Ahead of the lock: a request that names no savepoint must not make a
	// concurrent workflow wait to be told so.
	if err := req.Validate(); err != nil {
		return model.RollbackResult{}, err
	}
	// Every container it builds is labelled with the arcave project, which status reads back.
	if err := s.arcaveProject.Validate(); err != nil {
		return model.RollbackResult{}, fmt.Errorf("arcave project of the deployment labels: %w", err)
	}
	// A missing savepoint fails right away instead of waiting for the session lock.
	// Any other outcome is resolved again under the lock.
	if req.ID != nil || req.Name != nil {
		if _, err := s.resolveSavepoint(ctx, req); errors.Is(err, errSavepointNotFound) {
			return model.RollbackResult{}, err
		}
	}

	return locked(ctx, s.SavepointStoreService, model.SessionLockOperationRollback,
		func(ctx context.Context, grantedLock model.GrantedSessionLock, session model.SessionRef) (model.RollbackResult, error) {
			return s.rollback(ctx, grantedLock, session, req)
		},
	)
}

// rollback is the workflow itself, run with the project lock held: it finds the savepoint and
// picks the path: a project with no containers, one no snapshot is taken of, or the live one.
func (s *Service) rollback(
	ctx context.Context, grantedLock model.GrantedSessionLock, session model.SessionRef, req model.RollbackRequest,
) (model.RollbackResult, error) {
	savepoint, err := s.resolveSavepoint(ctx, req)
	if err != nil {
		return model.RollbackResult{}, err
	}

	if err := savepoint.ID.Validate(); err != nil {
		return model.RollbackResult{}, fmt.Errorf("validate savepoint id: %w", err)
	}

	// After the resolve: the gate lets through the rollback that repairs an interrupted one,
	// and that is known only once the selector names a record.
	recovery, err := s.requireNoInflight(ctx, savepoint.ID)
	if err != nil {
		return model.RollbackResult{}, fmt.Errorf("require no inflight: %w", err)
	}

	live, err := s.workload.Inspect(ctx)
	if err != nil {
		logInterrupt(ctx, "rollback interrupted before anything was stopped: nothing changed")
		return model.RollbackResult{}, fmt.Errorf("prepare: %w", preferInterrupt(ctx, err))
	}
	switch {
	case live.Absent():
		return s.rollbackColdStart(ctx, req, session, savepoint, live, recovery)
	case recovery.recovering || req.SkipSafetySnapshot:
		return s.rollbackWithoutSnapshot(ctx, req, session, savepoint, live, recovery)
	default:
		return s.rollbackLive(ctx, grantedLock, req, session, savepoint, live)
	}
}

// resolveSavepoint resolves req's selector to the record to roll back to.
// Resolution stays in memory: the store has no index to query.
//
// It reads every record, safety snapshots included, and findSavepoint decides
// which of them this selector may reach.
func (s *Service) resolveSavepoint(ctx context.Context, req model.RollbackRequest) (model.Savepoint, error) {
	savepoints, err := s.listSavepoints(ctx, true)
	if err != nil {
		return model.Savepoint{}, err
	}
	savepoint, err := findSavepoint(savepoints, req)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("finding savepoint %q: %w", req.Ref(), err)
	}
	return savepoint, nil
}

// newDeployment is the build of savepoint from session in the arcave project, as the deployment
// labels name it; the provider adds when it builds, and the replicas.
func (s *Service) newDeployment(session model.SessionRef, savepoint model.Savepoint) model.SavepointDeployment {
	return model.SavepointDeployment{
		ArcaveProject: s.arcaveProject,
		Session:       session,
		Savepoint: model.DeployedSavepoint{
			ID:               savepoint.ID,
			Name:             savepoint.Name,
			CreatedAt:        savepoint.CreatedAt,
			IsSafetySnapshot: savepoint.IsSafetySnapshot(),
		},
	}
}

// rollbackLive rolls back a compose project that has containers: stop it, take a safety
// snapshot, `compose down`, build it from the savepoint, and put the snapshot back on failure.
func (s *Service) rollbackLive(
	ctx context.Context,
	grantedLock model.GrantedSessionLock,
	req model.RollbackRequest,
	session model.SessionRef,
	savepoint model.Savepoint,
	live provider.LiveWorkload,
) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)

	// Planned before the stop, so a project that cannot be captured stays running.
	prepareCtx, prepare := progress.Begin(ctx, progress.Step{Kind: kindRollbackPrepare})
	plan, prepared, err := s.prepareLive(prepareCtx, live, session, savepoint)
	prepare.End(err)
	if err != nil {
		logInterrupt(ctx, "rollback interrupted before anything was stopped: nothing changed")
		return model.RollbackResult{}, preferInterrupt(ctx, err)
	}

	// A capture of the live state, so a rollback that fails partway can be replayed
	// back. Its committed record stays: rolling back to it by id undoes the rollback.
	held, safetySnapshot, err := takeSafetySnapshot(ctx, grantedLock, savepoint, plan)
	if err != nil {
		// Nothing was restored, so there is nothing to revert: restart best-effort, since a stop
		// that failed partway may have stopped some services. What the fan-out wrote stays for a
		// later gc, logged by the capture itself.
		logInterrupt(ctx, "rollback interrupted during the safety snapshot: restarting services, nothing changed")
		abortBeforeRestore(ctx, plan, req.WaitTimeout)
		return model.RollbackResult{}, err
	}

	// An interrupt that landed during the safety snapshot stops here: nothing is torn
	// down yet, so there is nothing to replay.
	if err := context.Cause(ctx); err != nil {
		logInterrupt(ctx, "rollback interrupted after the safety snapshot: restarting services, nothing changed")
		logKeptSafetySnapshot(ctx, safetySnapshot.ID)
		abortBeforeRestore(ctx, plan, req.WaitTimeout)
		return model.RollbackResult{}, fmt.Errorf("rollback interrupted, nothing changed: %w", err)
	}

	// The marker is written before the first destructive step, and `compose down` is one.
	if err := s.putInflightMarker(ctx, model.InflightRecreating, savepoint.ID, safetySnapshot.ID); err != nil {
		logKeptSafetySnapshot(ctx, safetySnapshot.ID)
		abortBeforeRestore(ctx, plan, req.WaitTimeout)
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	restored, err := runStep(ctx, kindRollbackApply, func(ctx context.Context) (provider.RestoredWorkload, error) {
		vacant, err := held.Teardown(ctx)
		if err != nil {
			return nil, err
		}
		return s.buildFromSavepoint(ctx, vacant, prepared, savepoint.ID, safetySnapshot.ID, req.WaitTimeout)
	})
	if err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("rollback failed: reverting to the pre-rollback state")
		logInterrupt(ctx, "rollback interrupted: reverting to the pre-rollback state, interrupt again to abandon the revert")
		err = preferInterrupt(ctx, err)

		// On a revertContext: the very Ctrl-C aborting the rollback would otherwise tear the
		// revert meant to save the environment, and the cleanup after it.
		revertCtx, cancel := revertContext(ctx)
		defer cancel()
		reverted, revertErr := s.revert(revertCtx, held, safetySnapshot, savepoint.ID, session, req.WaitTimeout)
		if revertErr != nil {
			// The snapshot is now the last copy of the pre-rollback state; the marker stays with it.
			if markErr := s.putInflightMarker(revertCtx, model.InflightInconsistent, savepoint.ID, safetySnapshot.ID); markErr != nil {
				log.Warn().Err(markErr).Msg("failed to mark the inflight marker inconsistent")
			}
			log.Error().Err(revertErr).Str("safety_snapshot_id", safetySnapshot.ID.String()).
				Msg("revert failed: environment left in an inconsistent state, safety snapshot retained; " +
					"it lists as a savepoint under `arcave savepoint list --include-safety-snapshots` " +
					"and can be rolled back to by id")
			return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
		}

		log.Info().Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("reverted to the pre-rollback state")
		s.cleanUpRollback(revertCtx, reverted)
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
	}

	// On a revertContext: the rollback has succeeded, and an interrupt now must not strand the
	// inflight marker or the orphaned volumes.
	cleanupCtx, cancel := revertContext(ctx)
	defer cancel()
	s.cleanUpRollback(cleanupCtx, restored)
	return newRollbackResult(savepoint, prepared.Summary(), safetySnapshot.ID, nil), nil
}

// prepareLive plans, ahead of the stop, the safety snapshot's capture and prepares the savepoint
// the rollback builds; what the safety snapshot leaves out is logged as its own.
func (s *Service) prepareLive(
	ctx context.Context, live provider.LiveWorkload, session model.SessionRef, savepoint model.Savepoint,
) (provider.CapturePlan, provider.PreparedSavepoint, error) {
	plan, err := s.planSafetySnapshot(ctx, live, savepoint)
	if err != nil {
		return nil, nil, err
	}
	prepared, err := live.PrepareSavepoint(ctx, savepoint.ID, provider.PrepareOptions{
		Deployment: s.newDeployment(session, savepoint),
	})
	if err != nil {
		return nil, nil, err
	}
	return plan, prepared, nil
}

// planSafetySnapshot plans the safety snapshot of the live compose project, taken before
// savepoint is restored; what it leaves out is logged as its own.
func (s *Service) planSafetySnapshot(
	ctx context.Context, live provider.LiveWorkload, savepoint model.Savepoint,
) (provider.CapturePlan, error) {
	var (
		safetyCtx = zerolog.Ctx(ctx).With().Bool("safety_snapshot", true).Logger().WithContext(ctx)
		policyErr *provider.CapturePolicyError
	)
	plan, err := live.PlanCapture(safetyCtx, provider.CaptureOptions{Restoring: savepoint.ID})
	if errors.As(err, &policyErr) {
		return nil, fmt.Errorf("resolve the safety snapshot's capture policy: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("plan the safety snapshot: %w", err)
	}
	if labelsIgnored := plan.LabelsIgnored(); labelsIgnored != nil {
		zerolog.Ctx(safetyCtx).Warn().Err(labelsIgnored).
			Msg("the safety snapshot ignores the labels and follows the config")
	}
	return plan, nil
}

// runPrepare prepares savepoint over the live compose project under the rollback's prepare step;
// onInterrupt says what an interrupt there leaves.
func (s *Service) runPrepare(
	ctx context.Context,
	live provider.LiveWorkload,
	session model.SessionRef,
	savepoint model.Savepoint,
	recovering bool,
	onInterrupt string,
) (provider.PreparedSavepoint, error) {
	prepareCtx, prepare := progress.Begin(ctx, progress.Step{Kind: kindRollbackPrepare})
	prepared, err := live.PrepareSavepoint(prepareCtx, savepoint.ID, provider.PrepareOptions{
		Deployment: s.newDeployment(session, savepoint),
		Recovering: recovering,
	})
	prepare.End(err)
	if err != nil {
		logInterrupt(ctx, onInterrupt)
		return nil, preferInterrupt(ctx, err)
	}
	return prepared, nil
}

// takeSafetySnapshot stops the services and captures the live state, the safety snapshot's phase;
// on failure the services are the caller's to restart. The held project is the revert's.
func takeSafetySnapshot(
	ctx context.Context, grantedLock model.GrantedSessionLock, savepoint model.Savepoint, plan provider.CapturePlan,
) (_ provider.HeldWorkload, _ model.Savepoint, err error) {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindRollbackSafetySnapshot})
	defer func() { step.End(err) }()
	held, err := plan.Hold(ctx)
	if err != nil {
		return nil, model.Savepoint{}, fmt.Errorf("stop services: %w", preferInterrupt(ctx, err))
	}
	// No name: one would read as the savepoint restored. TakenBefore is the resolved target, never
	// the typed ref, since "latest" names none and a name may be removed.
	safetySnapshot, err := commitCaptured(ctx, grantedLock, held, provider.CaptureRequest{
		Labels: newSafetySnapshotLabels(),
		TakenBefore: &model.SavepointRef{
			ID:   savepoint.ID,
			Name: savepoint.Name,
		},
	})
	if err != nil {
		return nil, model.Savepoint{}, fmt.Errorf("pre-rollback safety snapshot failed, nothing changed: %w", err)
	}
	return held, safetySnapshot, nil
}

// logKeptSafetySnapshot names the safety snapshot a rollback stopped short of using: hidden from
// `savepoint list`, it takes a retention slot until a gc retires it.
func logKeptSafetySnapshot(ctx context.Context, id model.SavepointID) {
	zerolog.Ctx(ctx).Warn().Str("safety_snapshot_id", id.String()).
		Msg("the rollback changed nothing; its safety snapshot stays until a gc retires it")
}

// abortBeforeRestore brings the held services back, best-effort; nothing was restored, so
// there is nothing to revert.
func abortBeforeRestore(ctx context.Context, plan provider.CapturePlan, waitTimeout time.Duration) {
	if err := releaseHeld(ctx, plan, waitTimeout); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("services failed to restart after the aborted rollback")
	}
}

// buildFromSavepoint builds prepared on vacant, restores every volume and starts the services,
// phasing the marker for markerTarget; safetyID is empty when the marker names none.
func (s *Service) buildFromSavepoint(
	ctx context.Context,
	vacant provider.VacantWorkload,
	prepared provider.PreparedSavepoint,
	markerTarget, safetyID model.SavepointID,
	waitTimeout time.Duration,
) (provider.RestoredWorkload, error) {
	built, err := vacant.Build(ctx, prepared)
	if err != nil {
		return nil, err
	}

	// Mandatory, unlike the phases that only relax one: a marker still reading
	// "recreating" would tell the next command the data is untouched.
	if err := s.putInflightMarker(ctx, model.InflightRestoring, markerTarget, safetyID); err != nil {
		return nil, fmt.Errorf("phasing the inflight marker to restoring: %w", err)
	}

	restored, err := built.RestoreData(ctx)
	if err != nil {
		return nil, err
	}

	// Before the restart: a kill during the start must read as "the data is
	// consistent", not "mid-restore". A failed update keeps the safer phase.
	if err := s.putInflightMarker(ctx, model.InflightRestoreDone, markerTarget, safetyID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker forward")
	}

	if err := restored.Start(ctx, waitTimeout); err != nil {
		return nil, fmt.Errorf("services did not become healthy after restore: %w", err)
	}
	return restored, nil
}

// revert replays the safety snapshot on the caller's context, not the fan-out's: load its layers,
// tear down, rebuild, restore, restart. session is the rollback's; the start waits waitTimeout.
func (s *Service) revert(
	ctx context.Context,
	held provider.HeldWorkload,
	safetySnapshot model.Savepoint,
	targetID model.SavepointID,
	session model.SessionRef,
	waitTimeout time.Duration,
) (_ provider.RestoredWorkload, err error) {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindRollbackRevert})
	defer func() { step.End(err) }()
	// The safety snapshot holds the live compose project's layers, so the undo puts them back.
	prepared, err := held.PrepareSavepoint(ctx, safetySnapshot.ID, provider.PrepareOptions{
		Deployment: s.newDeployment(session, safetySnapshot),
	})
	if err != nil {
		return nil, fmt.Errorf("prepare the safety snapshot: %w", err)
	}

	// Back to recreating: the compose project is about to be torn down again.
	if err := s.putInflightMarker(ctx, model.InflightRecreating, targetID, safetySnapshot.ID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker back before revert")
	}

	vacant, err := held.Teardown(ctx)
	if err != nil {
		return nil, err
	}
	return s.buildFromSavepoint(ctx, vacant, prepared, targetID, safetySnapshot.ID, waitTimeout)
}

// cleanUpRollback ends a consistent rollback: it reaps what the teardown orphaned and clears the
// inflight marker. The safety snapshot stays.
func (s *Service) cleanUpRollback(ctx context.Context, restored provider.RestoredWorkload) {
	// Run, not Begin: with nothing to reap and no warning, a lazy step would not report at all.
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindRollbackCleanup})
	defer step.End(nil)
	restored.Reap(ctx)
	if err := s.inflightMarker.Clear(ctx); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to clear the inflight marker")
	}
}

// newRollbackResult reports a rollback to savepoint that built prepared, and the safety snapshot
// that undoes it.
func newRollbackResult(
	savepoint model.Savepoint, prepared provider.PreparedSummary, safetySnapshot model.SavepointID, leftover []string,
) model.RollbackResult {
	return model.RollbackResult{
		RolledBackTo:    savepoint,
		Restored:        prepared.Volumes,
		WritableLayers:  prepared.WritableLayers,
		SafetySnapshot:  safetySnapshot,
		LeftoverVolumes: leftover,
	}
}

// rollbackWithoutSnapshot rolls back a compose project that has containers and captures nothing of
// it, neither volumes nor writable layers: a request skipping the snapshot, or a recovery, whose
// partial writes no undo may capture. Nothing of the live project can refuse it: `compose down` goes
// by the target's spec and the project's name. A failure past the marker leaves things as they are,
// and the same rollback again recovers.
func (s *Service) rollbackWithoutSnapshot(
	ctx context.Context,
	req model.RollbackRequest,
	session model.SessionRef,
	savepoint model.Savepoint,
	live provider.LiveWorkload,
	recovery rollbackRecovery,
) (model.RollbackResult, error) {
	prepared, err := s.runPrepare(
		ctx, live, session, savepoint, recovery.recovering,
		"rollback interrupted before anything was stopped: nothing changed")
	if err != nil {
		return model.RollbackResult{}, err
	}
	// An interrupt that landed during the prepare stops here: a marker would refuse the next
	// workflow over a compose project nothing touched.
	if err := context.Cause(ctx); err != nil {
		logInterrupt(ctx, "rollback interrupted before anything was stopped: nothing changed")
		return model.RollbackResult{}, fmt.Errorf("rollback interrupted, nothing changed: %w", err)
	}
	progress.Current(ctx).WarnInResult(noSafetySnapshotWarning)

	if err := s.putInflightMarker(ctx, newFirstMarkerPhase(recovery.recovering), savepoint.ID, recovery.safety); err != nil {
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	restored, err := runStep(ctx, kindRollbackApply, func(ctx context.Context) (provider.RestoredWorkload, error) {
		vacant, err := live.Teardown(ctx, prepared)
		if err != nil {
			return nil, err
		}
		return s.buildFromSavepoint(ctx, vacant, prepared, savepoint.ID, recovery.safety, req.WaitTimeout)
	})
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("the inflight marker stands: run the same rollback again")
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", preferInterrupt(ctx, err))
	}

	// On a revertContext: the rollback has succeeded, and an interrupt now must not strand the
	// inflight marker or the orphaned volumes.
	cleanupCtx, cancel := revertContext(ctx)
	defer cancel()
	s.cleanUpRollback(cleanupCtx, restored)
	return newRollbackResult(savepoint, prepared.Summary(), "", nil), nil
}

// newFirstMarkerPhase is the phase the first marker of a rollback taking no snapshot is written
// with: a recovery must not claim that nothing was written.
func newFirstMarkerPhase(recovering bool) model.InflightPhase {
	if recovering {
		return model.InflightRestoring
	}
	return model.InflightRecreating
}

// rollbackColdStart rolls back a compose project with no containers (never started, or taken
// down): nothing is stopped or snapshotted, so a failure only removes what it created.
func (s *Service) rollbackColdStart(
	ctx context.Context,
	req model.RollbackRequest,
	session model.SessionRef,
	savepoint model.Savepoint,
	live provider.LiveWorkload,
	recovery rollbackRecovery,
) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)
	log.Info().Msg("the compose project is not running: building it from the savepoint")

	prepared, err := s.runPrepare(ctx, live, session, savepoint, recovery.recovering,
		"rollback interrupted before anything was created: nothing changed")
	if err != nil {
		return model.RollbackResult{}, err
	}

	leftover := prepared.Summary().Overwritten
	if recovery.recovering {
		// What the project still owns is the interrupted rollback's partial writes, not volumes a
		// `compose down` left: overwriting them is the recovery, and no leftover to name.
		for _, name := range leftover {
			log.Info().Str("volume", name).
				Msg("the compose project is not running but this volume survives; the rollback overwrites it, " +
					"and no safety snapshot is taken")
		}
		leftover = nil
	}
	for _, name := range leftover {
		progress.Current(ctx).WarnInResult(model.NewLeftoverVolumeNotice(name).Message)
	}

	// No safety snapshot of its own: the marker tells the user how to recover.
	if err := s.putInflightMarker(ctx, newFirstMarkerPhase(recovery.recovering), savepoint.ID, recovery.safety); err != nil {
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	var vacant provider.VacantWorkload
	restored, err := runStep(ctx, kindRollbackApply, func(ctx context.Context) (provider.RestoredWorkload, error) {
		var err error
		if vacant, err = live.Teardown(ctx, prepared); err != nil {
			return nil, err
		}
		return s.buildFromSavepoint(ctx, vacant, prepared, savepoint.ID, recovery.safety, req.WaitTimeout)
	})
	if err != nil {
		logInterrupt(ctx, "rollback interrupted: removing what it created")
		s.abortColdStart(ctx, vacant)
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", preferInterrupt(ctx, err))
	}

	// On a revertContext, as every path's cleanup; nothing was orphaned, the teardown having
	// found nothing to remove.
	cleanupCtx, cancel := revertContext(ctx)
	defer cancel()
	s.cleanUpRollback(cleanupCtx, restored)
	return newRollbackResult(savepoint, prepared.Summary(), "", leftover), nil
}

// abortColdStart removes what a failed rollbackColdStart created, new volumes included, and
// clears the inflight marker if no volume was written yet; all best-effort.
func (s *Service) abortColdStart(ctx context.Context, vacant provider.VacantWorkload) {
	log := zerolog.Ctx(ctx)
	// On a revertContext: the Ctrl-C that aborted the rollback must not leave a half-created
	// compose project behind.
	ctx, cancel := revertContext(ctx)
	defer cancel()
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindRollbackRevert})
	var discardErr error
	if vacant != nil {
		discardErr = vacant.Discard(ctx)
	}
	defer step.End(discardErr)

	if discardErr != nil {
		log.Error().Err(discardErr).Msg("failed to remove what the failed rollback created")
	}

	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to read the inflight marker back")
		return
	}
	if !found {
		return
	}
	if marker.Phase != model.InflightRecreating {
		// restore-done would say only the start is missing, and what the restore wrote into has just gone.
		if marker.Phase == model.InflightRestoreDone {
			marker.Phase = model.InflightRestoring
			if err := s.putInflightMarker(ctx, marker.Phase, marker.Target, marker.Safety); err != nil {
				log.Warn().Err(err).Msg("failed to phase the inflight marker back")
			}
		}
		log.Error().Str("phase", string(marker.Phase)).Msg(recoveryHint(marker))
		return
	}
	if err := s.inflightMarker.Clear(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to clear the inflight marker")
	}
}
