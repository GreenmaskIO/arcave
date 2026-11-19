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
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/core/internal/workflow"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// savepointPreparer is a workload handle a savepoint is prepared over: a live workload, or a
// surveyed one an up drops.
type savepointPreparer interface {
	PrepareSavepoint(ctx context.Context, id model.SavepointID, opts provider.PrepareOptions) (provider.PreparedSavepoint, error)
}

// runPrepare prepares savepoint over the live compose project under the rollback's prepare step;
// onInterrupt says what an interrupt there leaves.
func (s *Service) runPrepare(
	ctx context.Context,
	live savepointPreparer,
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
		workflow.LogInterrupt(ctx, onInterrupt)
		return nil, preferInterrupt(ctx, err)
	}
	return prepared, nil
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

// newFirstMarkerPhase is the phase the first marker of a rollback taking no snapshot is written
// with: a recovery must not claim that nothing was written.
func newFirstMarkerPhase(recovering bool) model.InflightPhase {
	if recovering {
		return model.InflightRestoring
	}
	return model.InflightRecreating
}

// putInflightMarker writes the marker for the rollback restoring targetID; safety is the safety
// snapshot it names, empty for none. A phase change rewrites it whole.
func (s *Service) putInflightMarker(ctx context.Context, phase model.InflightPhase, targetID, safety model.SavepointID) error {
	return s.inflightMarker.Put(ctx, model.InflightMarker{
		Phase:          phase,
		Session:        s.session,
		ComposeProject: s.composeProject,
		Target:         targetID,
		Safety:         safety,
		At:             time.Now(),
	})
}

// applySavepoint vacates the workload and builds prepared on it, under the rollback's apply step.
// vacant is what vacate reached, nil when it failed: the caller's to discard.
func (s *Service) applySavepoint(
	ctx context.Context,
	prepared provider.PreparedSavepoint,
	markerTarget, safetyID model.SavepointID,
	waitTimeout time.Duration,
	vacate func(ctx context.Context) (provider.VacantWorkload, error),
) (provider.RestoredWorkload, provider.VacantWorkload, error) {
	var vacant provider.VacantWorkload
	restored, err := runStep(ctx, kindRollbackApply, func(ctx context.Context) (provider.RestoredWorkload, error) {
		var err error
		if vacant, err = vacate(ctx); err != nil {
			return nil, err
		}
		return s.buildFromSavepoint(ctx, vacant, prepared, markerTarget, safetyID, waitTimeout)
	})
	if err != nil {
		return nil, vacant, err
	}
	return restored, vacant, nil
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
		log.Error().Str("phase", string(marker.Phase)).Msg(workflow.RecoveryHint(marker))
		return
	}
	if err := s.inflightMarker.Clear(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to clear the inflight marker")
	}
}

// cleanUpRollback ends a consistent rollback: it reaps what the teardown orphaned and clears the
// inflight marker. The safety snapshot stays.
func (s *Service) cleanUpRollback(ctx context.Context, restored provider.RestoredWorkload) {
	// On a revertContext: the rollback is consistent, and an interrupt now must not strand the
	// inflight marker or the orphaned volumes.
	ctx, cancel := revertContext(ctx)
	defer cancel()
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
		Restored:        prepared.Restored,
		Detail:          prepared.Detail,
		SafetySnapshot:  safetySnapshot,
		LeftoverVolumes: leftover,
	}
}
