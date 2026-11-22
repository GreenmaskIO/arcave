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

// noSafetySnapshotWarning says what a rollback taking no safety snapshot risks.
const noSafetySnapshotWarning = "no safety snapshot: the rollback cannot be undone, and after a failure " +
	"running the same rollback again is the way back"

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

	prepared, err := s.runPrepare(ctx, live, session, savepoint, recovery.recovering)
	if err != nil {
		return model.RollbackResult{}, err
	}
	// An interrupt that landed during the prepare stops here: a marker would refuse the next
	// workflow over a compose project nothing touched.
	if ctx.Err() != nil {
		return model.RollbackResult{}, newInterruptedError(ctx)
	}

	summary := prepared.Summary()
	leftover := summary.Overwritten
	for _, warning := range summary.Warnings {
		progress.Current(ctx).WarnInResult(warning.Message)
	}

	// No safety snapshot of its own: the marker tells the user how to recover.
	if err := s.putInflightMarker(ctx, newFirstMarkerPhase(recovery.recovering), savepoint.ID, recovery.safety); err != nil {
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker: %w", err)
	}

	restored, vacant, err := s.applySavepoint(ctx, prepared, savepoint.ID, recovery.safety, req.WaitTimeout,
		func(ctx context.Context) (provider.VacantWorkload, error) {
			return live.Teardown(ctx, prepared)
		})
	if err != nil {
		err = fmt.Errorf("rollback failed: %w", preferInterrupt(ctx, err))
		if ctx.Err() != nil {
			log.Info().Msg("rollback interrupted: removing what it created")
		}
		if discardErr := s.abortColdStart(ctx, vacant); discardErr != nil {
			return model.RollbackResult{}, fmt.Errorf("%w; what it created could not be removed: %w", err, discardErr)
		}
		return model.RollbackResult{}, err
	}

	// Nothing was orphaned, the teardown having found nothing to remove.
	s.cleanUpRollback(ctx, restored)
	return newRollbackResult(savepoint, prepared.Summary(), "", leftover), nil
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
	prepared, err := s.runPrepare(ctx, live, session, savepoint, recovery.recovering)
	if err != nil {
		return model.RollbackResult{}, err
	}
	// An interrupt that landed during the prepare stops here: a marker would refuse the next
	// workflow over a compose project nothing touched.
	if ctx.Err() != nil {
		return model.RollbackResult{}, newInterruptedError(ctx)
	}
	progress.Current(ctx).WarnInResult(noSafetySnapshotWarning)

	if err := s.putInflightMarker(ctx, newFirstMarkerPhase(recovery.recovering), savepoint.ID, recovery.safety); err != nil {
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker: %w", err)
	}

	restored, _, err := s.applySavepoint(ctx, prepared, savepoint.ID, recovery.safety, req.WaitTimeout,
		func(ctx context.Context) (provider.VacantWorkload, error) {
			return live.Teardown(ctx, prepared)
		})
	if err != nil {
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", preferInterrupt(ctx, err))
	}

	s.cleanUpRollback(ctx, restored)
	return newRollbackResult(savepoint, prepared.Summary(), "", nil), nil
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
		return model.RollbackResult{}, preferInterrupt(ctx, err)
	}

	held, safetySnapshot, err := s.snapshotAndMark(ctx, grantedLock, savepoint, plan)
	if err != nil {
		// Nothing was restored, so there is nothing to revert: only the services are brought back,
		// since a stop that failed partway may have stopped some.
		if ctx.Err() != nil {
			log.Info().Msg("rollback interrupted before anything was restored: restarting services")
		}
		if releaseErr := releaseHeld(ctx, plan, req.WaitTimeout); releaseErr != nil {
			return model.RollbackResult{}, fmt.Errorf("%w; the services were not resumed: %w", err, releaseErr)
		}
		return model.RollbackResult{}, err
	}

	restored, _, err := s.applySavepoint(ctx, prepared, savepoint.ID, safetySnapshot.ID, req.WaitTimeout, held.Teardown)
	if err != nil {
		err = fmt.Errorf("rollback failed: %w", preferInterrupt(ctx, err))
		if ctx.Err() != nil {
			log.Info().Msg("rollback interrupted: reverting to the pre-rollback state, interrupt again to abandon the revert")
		}

		// On a revertContext: the very Ctrl-C aborting the rollback would otherwise tear the
		// revert meant to save the environment.
		revertCtx, cancel := revertContext(ctx)
		defer cancel()
		reverted, revertErr := s.revert(revertCtx, held, safetySnapshot, savepoint.ID, session, req.WaitTimeout)
		if revertErr != nil {
			// The snapshot is now the last copy of the pre-rollback state; the marker stays with it.
			if markErr := s.putInflightMarker(revertCtx, model.InflightInconsistent, savepoint.ID, safetySnapshot.ID); markErr != nil {
				log.Warn().Err(markErr).Msg("failed to mark the inflight marker inconsistent")
			}
			return model.RollbackResult{}, fmt.Errorf("%w; the revert failed: %w", err, revertErr)
		}

		s.cleanUpRollback(revertCtx, reverted)
		return model.RollbackResult{}, fmt.Errorf("%w; reverted to the pre-rollback state", err)
	}

	s.cleanUpRollback(ctx, restored)
	return newRollbackResult(savepoint, prepared.Summary(), safetySnapshot.ID, nil), nil
}

// snapshotAndMark takes the safety snapshot and writes the inflight marker: all that precedes the
// first destructive step. On error nothing was restored, and the services are the caller's to restart.
func (s *Service) snapshotAndMark(
	ctx context.Context, grantedLock model.GrantedSessionLock, savepoint model.Savepoint, plan provider.CapturePlan,
) (provider.HeldWorkload, model.Savepoint, error) {
	// A capture of the live state, so a rollback that fails partway can be replayed back. Its
	// committed record stays: rolling back to it by id undoes the rollback.
	held, safetySnapshot, err := s.takeSafetySnapshot(ctx, grantedLock, savepoint, plan)
	if err != nil {
		return nil, model.Savepoint{}, err
	}
	zerolog.Ctx(ctx).Debug().Str("safety_snapshot", safetySnapshot.ID.String()).Msg("safety snapshot committed")
	// An interrupt that landed during the safety snapshot stops here: nothing is torn down yet.
	if ctx.Err() != nil {
		return nil, model.Savepoint{}, newInterruptedError(ctx)
	}
	// The marker is written before the first destructive step, and `compose down` is one.
	if err := s.putInflightMarker(ctx, model.InflightRecreating, savepoint.ID, safetySnapshot.ID); err != nil {
		return nil, model.Savepoint{}, fmt.Errorf("recording the inflight marker: %w", err)
	}
	return held, safetySnapshot, nil
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
	safetyCtx := zerolog.Ctx(ctx).With().Bool("safety_snapshot", true).Logger().WithContext(ctx)
	plan, err := live.PlanCapture(safetyCtx, provider.CaptureOptions{Restoring: savepoint.ID})
	if _, isPolicyErr := errors.AsType[*provider.CapturePolicyError](err); isPolicyErr {
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

// takeSafetySnapshot stops the services and captures the live state, the safety snapshot's phase;
// on failure the services are the caller's to restart. The held project is the revert's.
func (s *Service) takeSafetySnapshot(
	ctx context.Context, grantedLock model.GrantedSessionLock, savepoint model.Savepoint, plan provider.CapturePlan,
) (_ provider.HeldWorkload, _ model.Savepoint, err error) {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindRollbackSafetySnapshot})
	defer func() { step.End(err) }()
	// No name: one would read as the savepoint restored. TakenBefore is the resolved target, never
	// the typed ref, since "latest" names none and a name may be removed.
	req := provider.CaptureRequest{
		ID:     model.NewSavepointID(),
		Labels: newSafetySnapshotLabels(),
		TakenBefore: &model.SavepointRef{
			ID:   savepoint.ID,
			Name: savepoint.Name,
		},
	}
	beatCtx, beat, err := s.startSavepointHeartbeat(ctx, req.ID)
	if err != nil {
		return nil, model.Savepoint{}, fmt.Errorf("pre-rollback safety snapshot failed: %w",
			preferInterrupt(ctx, err))
	}
	held, err := plan.Hold(beatCtx)
	if err != nil {
		err = beat.fail(ctx, beatCtx, err)
		return nil, model.Savepoint{}, newCaptureRetainedError(req.ID,
			fmt.Errorf("stop services: %w", preferInterrupt(ctx, err)))
	}
	safetySnapshot, err := captureAndCommit(ctx, beatCtx, beat, grantedLock, held, req)
	if err != nil {
		return nil, model.Savepoint{}, newCaptureRetainedError(req.ID,
			fmt.Errorf("pre-rollback safety snapshot failed: %w", preferInterrupt(ctx, err)))
	}
	return held, safetySnapshot, nil
}

// newSafetySnapshotLabels is the mark that makes an ordinary savepoint an undo: it
// hides the record from every scanning selector, and a backend with somewhere to
// put it renders it into its own store (cdc into a restic tag).
func newSafetySnapshotLabels() map[string]string {
	return map[string]string{
		model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue,
	}
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
