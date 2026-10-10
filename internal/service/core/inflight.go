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

// requireNoInflight refuses to run while .arcave/inflight.json says an earlier rollback was
// interrupted, except for the one rollback that repairs it: to the marker's safety snapshot,
// or, when the marker has none, to the interrupted rollback's own savepoint again.
// restoring is the savepoint the caller rolls back to, "" for any other workflow. It returns
// the marker it let through, zero when there is none.
func (s *Service) requireNoInflight(ctx context.Context, restoring model.SavepointID) (model.InflightMarker, error) {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return model.InflightMarker{}, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return model.InflightMarker{}, nil
	}
	if restoring != "" && marker.Safety == "" && restoring == marker.Target {
		zerolog.Ctx(ctx).Info().Str("interrupted_target", marker.Target.String()).
			Msg("re-running an interrupted rollback into a compose project that was not running; " +
				"the marker clears on success")
		return marker, nil
	}
	if restoring != "" && restoring == marker.Safety {
		zerolog.Ctx(ctx).Info().Str("interrupted_target", marker.Target.String()).
			Msg("rolling back to the safety snapshot of an interrupted rollback; the marker clears on success")
		return marker, nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("interrupted_target", marker.Target.String()).
		Str("safety_snapshot_id", marker.Safety.String()).
		Str("phase", string(marker.Phase)).
		Msg(recoveryHint(marker))
	return model.InflightMarker{}, err
}

// recoveryHint is the message telling the user how to recover from the interrupted rollback the
// marker records; it depends on the phase the rollback reached.
func recoveryHint(marker model.InflightMarker) string {
	if marker.Safety == "" {
		return "a rollback into a compose project that was not running was interrupted; volumes may be " +
			"half-restored: re-run `arcave rollback " + marker.Target.String() + "`"
	}
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

// putInflightMarker writes the marker for the rollback restoring target; safety is the
// safety snapshot that undoes it, empty when none was taken. A phase change rewrites it whole.
func (s *Service) putInflightMarker(ctx context.Context, phase model.InflightPhase, target, safety model.SavepointID) error {
	return s.inflightMarker.Put(ctx, model.InflightMarker{
		Phase:  phase,
		Target: target,
		Safety: safety,
		At:     time.Now(),
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
