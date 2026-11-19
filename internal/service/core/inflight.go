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
	"github.com/greenmaskio/arcave/internal/service/core/internal/workflow"
)

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

// rollbackRecovery is what a rollback takes over from the interrupted one it recovers from; zero
// when it recovers from none.
type rollbackRecovery struct {
	recovering bool
	// safety is the safety snapshot the standing marker names: the recovery's marker keeps naming
	// it, as the last copy of the state before them both.
	safety model.SavepointID
}

// requireNoInflight refuses while an inflight marker stands, except for the rollback to restoring
// ("" for any other workflow) that recovers from it, and reports what that recovery takes over.
func (s *Service) requireNoInflight(ctx context.Context, restoring model.SavepointID) (rollbackRecovery, error) {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return rollbackRecovery{}, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return rollbackRecovery{}, nil
	}
	if s.isRecoveryRollback(marker, restoring) {
		zerolog.Ctx(ctx).Info().Str("interrupted_target", marker.Target.String()).
			Msg("recovering from an interrupted rollback; the marker clears on success")
		return rollbackRecovery{
			recovering: true,
			safety:     marker.Safety,
		}, nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("interrupted_target", marker.Target.String()).
		Str("session", marker.Session.String()).
		Str("compose_project", marker.ComposeProject.String()).
		Str("safety_snapshot_id", marker.Safety.String()).
		Str("phase", string(marker.Phase)).
		Msg(workflow.RecoveryHint(marker))
	return rollbackRecovery{}, err
}

// isRecoveryRollback reports whether a rollback to id, in this session and compose project,
// recovers from marker: one to its safety snapshot, or to any savepoint if the marker accepts any.
func (s *Service) isRecoveryRollback(marker model.InflightMarker, id model.SavepointID) bool {
	if id == "" || marker.Session != s.session || marker.ComposeProject != s.composeProject {
		return false
	}
	return marker.AcceptsAnySavepoint() || id == marker.Safety
}
