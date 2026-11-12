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
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// recoveryHint tells the user what state the interrupted rollback left and how to recover.
func recoveryHint(marker model.InflightMarker) string {
	hint := interruptedState(marker) + "; recover with `arcave rollback --session " + marker.Session.String() +
		" -p " + marker.ComposeProject.String() + " " + marker.RecoveryID().String() + "`"
	if marker.AcceptsAnySavepoint() {
		return hint + ", or name another savepoint"
	}
	return hint
}

// interruptedState describes what the interrupted rollback left behind, by the phase it reached.
func interruptedState(marker model.InflightMarker) string {
	switch {
	case marker.Safety != "" && marker.Target == marker.Safety:
		return "the recovery to the safety snapshot failed; volumes may be half-restored"
	// Ahead of restore-done: a failed cold start has removed what the restore wrote into.
	// Not "nothing changed": a forced up drops the compose project in this phase.
	case marker.Safety == "" && marker.Phase == model.InflightRecreating:
		return "a rollback that took no safety snapshot was interrupted before it restored any volume; " +
			"the containers may be gone"
	case marker.Safety == "":
		return "a rollback that took no safety snapshot was interrupted; volumes may be half-restored"
	case marker.Phase == model.InflightRestoreDone:
		return "every volume was restored and only the services were not started " +
			"(starting the compose project and removing .arcave/inflight.json keeps the rollback instead)"
	case marker.Phase == model.InflightInconsistent:
		return "the rollback failed and so did its revert"
	default:
		return "volumes may mix states"
	}
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
