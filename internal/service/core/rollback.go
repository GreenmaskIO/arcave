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
	"strings"
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

// rollbackTarget is what a rollback builds the compose project from, prepared before anything
// is written: the savepoint's spec pointed at its loaded layers, and one backend per volume.
type rollbackTarget struct {
	project  *types.Project
	restored []model.ImageRestore
	backends []model.VolumeSnapshotBackend
}

// result reports a rollback to record that built t.
func (t rollbackTarget) result(record model.Savepoint) model.RollbackResult {
	return model.RollbackResult{
		Savepoint: record,
		Restored:  volumesOf(record),
		Images:    t.restored,
	}
}

func safetySnapshotName(restoring model.SavepointID) string {
	return safetySnapshotPrefix + "-" + restoring.String()
}

// safetySnapshotLabels is the mark that makes an ordinary savepoint an undo: it
// hides the record from every scanning selector, and a backend with somewhere to
// put it renders it into its own store (cdc into a restic tag).
func safetySnapshotLabels() map[string]string {
	return map[string]string{model.SafetySnapshotLabel: model.SafetySnapshotLabelValue}
}

// Rollback restores the compose project to the referenced savepoint, putting the safety snapshot
// back if it fails after `compose down`; a project with no containers goes to rollbackColdStart.
func (s *Service) Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error) {
	// Ahead of the lock: a request that names no savepoint must not make a
	// concurrent workflow wait to be told so.
	if err := req.Validate(); err != nil {
		return model.RollbackResult{}, err
	}

	return locked(ctx, s, func() (model.RollbackResult, error) {
		return s.rollback(ctx, req)
	})
}

// rollback is the workflow itself, run with the project lock held: it finds the savepoint
// and picks the path by whether the compose project has containers.
func (s *Service) rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error) {
	record, err := s.resolveSavepoint(ctx, req)
	if err != nil {
		return model.RollbackResult{}, err
	}

	if err := record.ID.Validate(); err != nil {
		return model.RollbackResult{}, fmt.Errorf("validate savepoint id: %w", err)
	}

	// After the resolve: the gate lets through the rollback that repairs an interrupted one,
	// and that is known only once the selector names a record.
	interrupted, err := s.requireNoInflight(ctx, record.ID)
	if err != nil {
		return model.RollbackResult{}, fmt.Errorf("require no inflight: %w", err)
	}

	res, err := s.introspect(ctx)
	if err != nil {
		logInterrupt(ctx, "rollback interrupted before anything was stopped: nothing changed")
		return model.RollbackResult{}, fmt.Errorf("prepare: %w", preferInterrupt(ctx, err))
	}
	if composeProjectAbsent(res) {
		return s.rollbackColdStart(ctx, req, record, res, interrupted)
	}
	return s.rollbackLive(ctx, req, record, res)
}

// rollbackLive rolls back a compose project that has containers: stop it, take a safety
// snapshot, `compose down`, build it from the savepoint, and put the snapshot back on failure.
func (s *Service) rollbackLive(
	ctx context.Context, req model.RollbackRequest, record model.Savepoint, res model.IntrospectionResult,
) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)

	// First, so a daemon the image backend cannot serve is refused before any layer
	// is loaded.
	ws, err := s.workingSetOf(ctx, res, req.WaitTimeout)
	if err != nil {
		logInterrupt(ctx, "rollback interrupted before anything was stopped: nothing changed")
		return model.RollbackResult{}, fmt.Errorf("prepare: %w", preferInterrupt(ctx, err))
	}

	// Ahead of the stop: whatever cannot be prepared leaves the project running.
	target, err := s.prepareTarget(ctx, record)
	if err != nil {
		logInterrupt(ctx, "rollback interrupted before anything was stopped: nothing changed")
		return model.RollbackResult{}, preferInterrupt(ctx, err)
	}

	killed, err := s.stopServices(ctx, ws)
	if err != nil {
		// A stop that failed or was interrupted partway may have stopped some services.
		logInterrupt(ctx, "rollback interrupted while stopping services: restarting them, nothing changed")
		if err := s.restartServices(ctx, ws); err != nil {
			log.Error().Err(err).Msg("services failed to restart after stop failure")
		}
		return model.RollbackResult{}, fmt.Errorf("stop services: %w", preferInterrupt(ctx, err))
	}

	// A capture of the live state, so a rollback that fails partway can be replayed
	// back. Dropped on every path but a failed revert, and committing a record —
	// which is what leaves a killed rollback's undo listable, and so applicable by
	// hand.
	log.Info().Msg("backing up current state")
	safetySnapshot, err := s.performSavepoint(
		ctx, ws, killed, safetySnapshotName(record.ID), "", safetySnapshotLabels(),
	)
	if err != nil {
		// Nothing was restored, so there is nothing to revert: restart best-effort and
		// report the capture error. What the fan-out wrote stays for a later gc,
		// logged by the capture itself.
		logInterrupt(ctx, "rollback interrupted during the safety snapshot: restarting services, nothing changed")
		if err := s.restartServices(ctx, ws); err != nil {
			log.Error().Err(err).Msg("services failed to restart after safety snapshot failure")
		}
		return model.RollbackResult{}, fmt.Errorf("pre-rollback safety snapshot failed, nothing changed: %w", err)
	}

	// An interrupt that landed during the safety snapshot stops here: nothing is torn
	// down yet, so there is nothing to replay.
	if err := ctx.Err(); err != nil {
		logInterrupt(ctx, "rollback interrupted after the safety snapshot: restarting services, nothing changed")
		s.abortBeforeRestore(ctx, safetySnapshot, ws)
		return model.RollbackResult{}, fmt.Errorf("rollback interrupted, nothing changed: %w", err)
	}

	// The marker is written before the first destructive step, and `compose down` is one.
	if err := s.putInflightMarker(ctx, model.InflightRecreating, record.ID, safetySnapshot.ID); err != nil {
		s.abortBeforeRestore(ctx, safetySnapshot, ws)
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	if err := s.applySavepoint(ctx, record, target, safetySnapshot.ID, ws); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("rollback failed: reverting to the pre-rollback state")
		logInterrupt(ctx, "rollback interrupted: reverting to the pre-rollback state, interrupt again to abandon the revert")
		err = preferInterrupt(ctx, err)

		// On a revertContext: the very Ctrl-C aborting the rollback would otherwise tear the
		// revert meant to save the environment, and the cleanup after it.
		revertCtx, cancel := revertContext(ctx)
		defer cancel()
		if revertErr := s.revert(revertCtx, safetySnapshot, ws, record.ID); revertErr != nil {
			// The one path that keeps the snapshot: it is now the last copy of the
			// pre-rollback state, and the marker stays with it.
			if markErr := s.putInflightMarker(revertCtx, model.InflightInconsistent, record.ID, safetySnapshot.ID); markErr != nil {
				log.Warn().Err(markErr).Msg("failed to mark the inflight marker inconsistent")
			}
			log.Error().Err(revertErr).Str("safety_snapshot_id", safetySnapshot.ID.String()).
				Msg("revert failed: environment left in an inconsistent state, safety snapshot retained; " +
					"it lists as a savepoint under `arcave savepoint list --include-safety-snapshots` " +
					"and can be rolled back to by id")
			return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
		}

		log.Info().Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("reverted to the pre-rollback state")
		s.reapAnonymousVolumes(revertCtx, ws, volumesOf(safetySnapshot))
		s.releaseSafetySnapshot(revertCtx, safetySnapshot, ws.backends)
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", err)
	}

	// On a revertContext: the rollback has succeeded, and an interrupt now must not strand the
	// safety snapshot or the orphaned volumes.
	cleanupCtx, cancel := revertContext(ctx)
	defer cancel()
	s.reapAnonymousVolumes(cleanupCtx, ws, volumesOf(record))
	s.releaseSafetySnapshot(cleanupCtx, safetySnapshot, ws.backends)
	return target.result(record), nil
}

// prepareTarget builds the savepoint's backends, loads its spec and its layers into the
// daemon, and points the spec at those layers. It writes nothing a rollback restores into.
func (s *Service) prepareTarget(ctx context.Context, record model.Savepoint) (rollbackTarget, error) {
	// Their own set, not the live one's: a volume may have been added or dropped since.
	backends, err := s.buildBackends(volumesOf(record))
	if err != nil {
		return rollbackTarget{}, fmt.Errorf("build backends: %w", err)
	}
	project, err := s.loadSavepointSpec(ctx, record)
	if err != nil {
		return rollbackTarget{}, fmt.Errorf("load the savepoint's compose spec: %w", err)
	}
	// Also the check of the archives themselves: one the daemon will not take fails here.
	restored, err := s.restoreImages(ctx, record, project.Name)
	if err != nil {
		return rollbackTarget{}, fmt.Errorf("restore the savepoint's writable layers: %w", err)
	}
	return rollbackTarget{
		project:  retargetImages(project, restored),
		restored: restored,
		backends: backends,
	}, nil
}

// rollbackColdStart rolls back a compose project with no containers (never started, or taken
// down): nothing is stopped or snapshotted, so a failure only removes what it created.
func (s *Service) rollbackColdStart(
	ctx context.Context,
	req model.RollbackRequest,
	record model.Savepoint,
	res model.IntrospectionResult,
	interrupted model.InflightMarker,
) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)
	log.Info().Msg("the compose project is not running: building it from the savepoint")

	// Only loading layers needs Docker 25+; a savepoint without layers runs on any daemon.
	if len(record.Images) > 0 {
		if err := requireDaemon(ctx, res.Daemon, s.minDaemonMajor); err != nil {
			return model.RollbackResult{}, fmt.Errorf("prepare: %w", err)
		}
	}

	// Repeating an interrupted rollback: the volumes it finds are its own partial writes, so
	// they are not refused, and the marker must not claim again that nothing was written.
	repeating := interrupted.Target == record.ID && interrupted.Safety == ""
	owned := ownedVolumes(res)
	leftover := leftoverVolumes(owned, record)
	if len(leftover) > 0 && req.LeftoverVolumes == model.LeftoverVolumesRefuse && !repeating {
		return model.RollbackResult{}, fmt.Errorf("%w: %s", model.ErrLeftoverVolumes, strings.Join(leftover, ", "))
	}

	target, err := s.prepareTarget(ctx, record)
	if err != nil {
		logInterrupt(ctx, "rollback interrupted before anything was created: nothing changed")
		return model.RollbackResult{}, preferInterrupt(ctx, err)
	}

	for _, name := range leftover {
		log.Warn().Str("volume", name).
			Msg("the compose project is not running but this volume survives; the rollback overwrites it, " +
				"and no safety snapshot is taken")
	}

	// No safety snapshot: the marker tells the user to run this same rollback again.
	phase := model.InflightRecreating
	if repeating && interrupted.Phase != model.InflightRecreating {
		phase = model.InflightRestoring
	}
	if err := s.putInflightMarker(ctx, phase, record.ID, ""); err != nil {
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	if err := s.buildFromSavepoint(ctx, record, target, "", req.WaitTimeout); err != nil {
		logInterrupt(ctx, "rollback interrupted: removing what it created")
		s.abortColdStart(ctx, target.project, owned)
		return model.RollbackResult{}, fmt.Errorf("rollback failed: %w", preferInterrupt(ctx, err))
	}

	if err := s.inflightMarker.Clear(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to clear the inflight marker")
	}
	return target.result(record), nil
}

// ownedVolumes are the docker names of the volumes the compose project owns.
func ownedVolumes(res model.IntrospectionResult) map[string]bool {
	owned := map[string]bool{}
	for _, v := range res.Project.Volumes {
		if !v.External {
			owned[v.Name] = true
		}
	}
	return owned
}

// leftoverVolumes names the volumes that outlived the project's containers and record overwrites.
func leftoverVolumes(owned map[string]bool, record model.Savepoint) []string {
	var names []string
	for _, v := range volumesOf(record) {
		if v.Named != nil && owned[v.Named.VolumeName] {
			names = append(names, v.Named.VolumeName)
		}
	}
	return names
}

// abortColdStart removes what a failed rollbackColdStart created, new volumes included, and
// clears the inflight marker if no volume was written yet; all best-effort.
func (s *Service) abortColdStart(ctx context.Context, target *types.Project, ownedBefore map[string]bool) {
	log := zerolog.Ctx(ctx)
	// On a revertContext: the Ctrl-C that aborted the rollback must not leave a half-created
	// compose project behind.
	ctx, cancel := revertContext(ctx)
	defer cancel()

	if err := s.composeDown(ctx, target); err != nil {
		log.Error().Err(err).Msg("failed to remove what the failed rollback created")
	}
	s.removeCreatedVolumes(ctx, ownedBefore)

	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to read the inflight marker back")
		return
	}
	if !found {
		return
	}
	if marker.Phase != model.InflightRecreating {
		log.Error().Str("phase", string(marker.Phase)).Msg(recoveryHint(marker))
		return
	}
	if err := s.inflightMarker.Clear(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to clear the inflight marker")
	}
}

// removeCreatedVolumes drops the volumes the compose project owns now and did not own
// before: what the failed rollback created, holding nothing the savepoint does not.
func (s *Service) removeCreatedVolumes(ctx context.Context, ownedBefore map[string]bool) {
	log := zerolog.Ctx(ctx)
	res, err := s.introspect(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("cannot tell which volumes the failed rollback created; they are left behind")
		return
	}
	for name := range ownedVolumes(res) {
		if ownedBefore[name] {
			continue
		}
		if err := s.volumeRemover.RemoveVolume(ctx, name); err != nil {
			log.Warn().Err(err).Str("volume", name).Msg("volume the failed rollback created left behind")
			continue
		}
		log.Debug().Str("volume", name).Msg("removed the volume the failed rollback created")
	}
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

// abortBeforeRestore drops the committed safety snapshot and restarts the services, both
// best-effort and on a revertContext; nothing was restored, so there is nothing to revert.
// The snapshot is whole here — a partial one is left for a gc instead, by the caller that
// produced it.
func (s *Service) abortBeforeRestore(ctx context.Context, safetySnapshot model.Savepoint, ws workingSet) {
	log := zerolog.Ctx(ctx)
	ctx, cancel := revertContext(ctx)
	defer cancel()

	if err := s.dropSavepoint(ctx, safetySnapshot, ws.backends); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("failed to remove the safety snapshot")
	}
	if err := s.startServices(ctx, ws); err != nil {
		log.Error().Err(err).Msg("services failed to restart after the aborted rollback")
	}
}

// applySavepoint runs `compose down` and builds the compose project back from the savepoint;
// the caller handles every failure with one revert path.
func (s *Service) applySavepoint(
	ctx context.Context,
	record model.Savepoint,
	target rollbackTarget,
	safetyID model.SavepointID,
	ws workingSet,
) error {
	if err := s.composeDown(ctx, ws.project); err != nil {
		return err
	}
	return s.buildFromSavepoint(ctx, record, target, safetyID, ws.waitTimeout)
}

// buildFromSavepoint creates the compose project from the savepoint's spec, restores
// every volume and starts the services; safetyID is empty when no safety snapshot was taken.
func (s *Service) buildFromSavepoint(
	ctx context.Context,
	record model.Savepoint,
	target rollbackTarget,
	safetyID model.SavepointID,
	waitTimeout time.Duration,
) error {
	if err := s.composeCreate(ctx, target.project); err != nil {
		return err
	}
	if err := s.rebuildDivergentReplicas(ctx, target.restored); err != nil {
		return err
	}

	// Mandatory, unlike the phases that only relax one: a marker still reading
	// "recreating" would tell the next command the data is untouched.
	if err := s.putInflightMarker(ctx, model.InflightRestoring, record.ID, safetyID); err != nil {
		return fmt.Errorf("phasing the inflight marker to restoring: %w", err)
	}

	if err := s.restoreVolumes(ctx, record, target.backends); err != nil {
		return err
	}

	// Before the restart: a kill during the start must read as "the data is
	// consistent", not "mid-restore". A failed update keeps the safer phase.
	if err := s.putInflightMarker(ctx, model.InflightRestoreDone, record.ID, safetyID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker forward")
	}

	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Start(ctx, target.project, waitTimeout); err != nil {
		return fmt.Errorf("services did not become healthy after restore: %w", err)
	}
	return nil
}

// revert replays the safety snapshot through the very instances that took it: load
// its layers back, tear down, rebuild the compose project it was taken of on them, restore,
// restart. It runs on the caller's context, not the fan-out's — an errgroup cancels
// its own the moment Wait returns.
func (s *Service) revert(
	ctx context.Context,
	safetySnapshot model.Savepoint,
	ws workingSet,
	target model.SavepointID,
) error {
	// The safety snapshot holds the layers the live compose project had, so the undo has to
	// put them back — the same parity the capture side already has.
	restored, err := s.restoreImages(ctx, safetySnapshot, ws.project.Name)
	if err != nil {
		return fmt.Errorf("restore the safety snapshot's writable layers: %w", err)
	}
	live := retargetImages(ws.project, restored)

	// Back to recreating: the compose project is about to be torn down again.
	if err := s.putInflightMarker(ctx, model.InflightRecreating, target, safetySnapshot.ID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to phase the inflight marker back before revert")
	}

	if err := s.composeDown(ctx, ws.project); err != nil {
		return err
	}
	if err := s.composeCreate(ctx, live); err != nil {
		return err
	}
	if err := s.rebuildDivergentReplicas(ctx, restored); err != nil {
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
