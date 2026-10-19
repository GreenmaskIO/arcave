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
)

// requireNoInflight refuses to run while .arcave/inflight.json stands, except for the rollback
// that recovers from it. restoring is the savepoint the caller rolls back to, "" for any other
// workflow. It returns the marker it let through, zero when there is none.
func (s *Service) requireNoInflight(ctx context.Context, restoring model.SavepointID) (model.InflightMarker, error) {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return model.InflightMarker{}, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return model.InflightMarker{}, nil
	}
	if s.isRecoveryRollback(marker, restoring) {
		zerolog.Ctx(ctx).Info().Str("interrupted_target", marker.Target.String()).
			Msg("recovering from an interrupted rollback; the marker clears on success")
		return marker, nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("interrupted_target", marker.Target.String()).
		Str("session", marker.Session.String()).
		Str("compose_project", marker.ComposeProject.String()).
		Str("safety_snapshot_id", marker.Safety.String()).
		Str("phase", string(marker.Phase)).
		Msg(recoveryHint(marker))
	return model.InflightMarker{}, err
}

// isRecoveryRollback reports whether a rollback to id, in this session and compose project,
// is the one that recovers from the interrupted rollback the marker records.
func (s *Service) isRecoveryRollback(marker model.InflightMarker, id model.SavepointID) bool {
	return id == marker.RecoveryID() && marker.Session == s.session && marker.ComposeProject == s.composeProject
}

// recoveryHint tells the user what state the interrupted rollback left and how to recover.
func recoveryHint(marker model.InflightMarker) string {
	return interruptedState(marker) + "; recover with `arcave rollback --session " + marker.Session.String() +
		" -p " + marker.ComposeProject.String() + " " + marker.RecoveryID().String() + "`"
}

// interruptedState describes what the interrupted rollback left behind, by the phase it reached.
func interruptedState(marker model.InflightMarker) string {
	switch {
	case marker.Safety == "":
		return "a rollback into a compose project that was not running was interrupted; volumes may be half-restored"
	case marker.Phase == model.InflightRestoreDone:
		return "every volume was restored and only the services were not started " +
			"(starting the compose project and removing .arcave/inflight.json keeps the rollback instead)"
	case marker.Phase == model.InflightInconsistent:
		return "the rollback failed and so did its revert"
	default:
		return "volumes may mix states"
	}
}

// putInflightMarker writes the marker for the rollback restoring targetID; safety is the
// safety snapshot that undoes it, empty when none was taken. A phase change rewrites it whole.
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

// releaseSafetySnapshot ends a consistent rollback: marker first, snapshot second,
// so a failed clear never leaves a marker pointing at a safety snapshot that is gone.
func (s *Service) releaseSafetySnapshot(
	ctx context.Context, safetySnapshot model.Savepoint, backends []model.VolumeSnapshotBackend,
) {
	log := zerolog.Ctx(ctx)
	if err := s.inflightMarker.Clear(ctx); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("failed to clear the inflight marker; the safety snapshot it points at is kept")
		return
	}
	if err := s.dropSavepoint(ctx, safetySnapshot, backends); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("failed to remove safety snapshot")
	}
}
