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

// requireNoInflight refuses a mutating workflow while an interrupted rollback's
// marker stands. remedyTarget is the savepoint the caller is about to restore
// (empty when it restores nothing); the marker's own safety snapshot passes.
func (s *Service) requireNoInflight(ctx context.Context, remedyTarget model.SavepointID) error {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return nil
	}
	if remedyTarget != "" && remedyTarget == marker.Safety {
		zerolog.Ctx(ctx).Info().Str("interrupted_target", marker.Target.String()).
			Msg("rolling back to the safety snapshot of an interrupted rollback; the marker clears on success")
		return nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("interrupted_target", marker.Target.String()).
		Str("safety_snapshot_id", marker.Safety.String()).
		Str("phase", string(marker.Phase)).
		Msg(remedyOf(marker))
	return err
}

// remedyOf says how to get out of the phase the marker stands at.
func remedyOf(marker model.InflightMarker) string {
	switch marker.Phase {
	case model.InflightRestoreDone:
		return "every volume was restored and only the services were not started: start the compose project, then remove " +
			".arcave/inflight.json — or roll back to the safety snapshot to undo the rollback"
	case model.InflightInconsistent:
		return "the rollback failed and its revert failed too: roll back to the safety snapshot to return " +
			"to the pre-rollback state"
	default:
		return "volumes may mix states: roll back to the safety snapshot to return to the pre-rollback state"
	}
}

// putInflightMarker writes the marker for the rollback restoring target, with safety as
// its remedy. A phase transition rewrites the whole marker.
func (s *Service) putInflightMarker(ctx context.Context, phase model.InflightPhase, target, safety model.SavepointID) error {
	return s.inflightMarker.Put(ctx, model.InflightMarker{
		Phase:  phase,
		Target: target,
		Safety: safety,
		At:     time.Now(),
	})
}

// releaseSafetySnapshot ends a consistent rollback: marker first, snapshot second,
// so a failed clear never leaves a marker naming a deleted remedy.
func (s *Service) releaseSafetySnapshot(
	ctx context.Context, safetySnapshot model.Savepoint, backends []model.VolumeSnapshotBackend,
) {
	log := zerolog.Ctx(ctx)
	if err := s.inflightMarker.Clear(ctx); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).
			Msg("failed to clear the inflight marker; the safety snapshot is retained so the marker's remedy stays real")
		return
	}
	if err := s.dropSavepoint(ctx, safetySnapshot, backends); err != nil {
		log.Warn().Err(err).Str("safety_snapshot_id", safetySnapshot.ID.String()).Msg("failed to remove safety snapshot")
	}
}
