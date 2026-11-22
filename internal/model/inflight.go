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

package model

import (
	"fmt"
	"time"
)

// InflightPhase says how far an interrupted rollback got, which decides how to recover from it.
type InflightPhase string

const (
	// InflightRecreating covers `compose down` up to the containers existing again. The
	// volumes are untouched: recovering means rebuilding the containers, not restoring data.
	InflightRecreating   InflightPhase = "recreating"
	InflightRestoring    InflightPhase = "restoring"
	InflightRestoreDone  InflightPhase = "restore-done"
	InflightInconsistent InflightPhase = "inconsistent"
)

// InflightMarker is the intent record a rollback writes before its first destructive
// write and clears once the environment is consistent again.
type InflightMarker struct {
	Phase InflightPhase `json:"phase"`
	// Session and ComposeProject say where Target lives: a forked savepoint keeps its
	// id, so the id alone names no single savepoint.
	Session        SessionID                `json:"session"`
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	Target         SavepointID              `json:"target"` // the savepoint being restored
	// Safety is the safety snapshot that undoes the interrupted rollback; empty when it took none,
	// where a rollback to any savepoint is the fix, the same one first.
	Safety SavepointID `json:"safety"`
	At     time.Time   `json:"at"`
}

// AcceptsAnySavepoint reports whether a rollback to any savepoint recovers, not only to RecoveryID:
// the rollback took no safety snapshot, or a recovery to it has failed, and one that cannot come up
// must hold nothing hostage.
func (m InflightMarker) AcceptsAnySavepoint() bool {
	return m.Safety == "" || m.Target == m.Safety
}

// RecoveryID is the savepoint a recovery is told to roll back to: the safety snapshot, or, when the
// rollback took none, its own target again.
func (m InflightMarker) RecoveryID() SavepointID {
	if m.Safety == "" {
		return m.Target
	}
	return m.Safety
}

// describeState says what an interrupted or failed rollback left behind, by the phase it reached.
func (m InflightMarker) describeState() string {
	switch {
	case m.Safety != "" && m.Target == m.Safety:
		return "the recovery to the safety snapshot failed; volumes may be half-restored"
	// Ahead of restore-done: a failed cold start has removed what the restore wrote into.
	// The containers may be gone: a forced up drops the compose project in this phase.
	case m.Safety == "" && m.Phase == InflightRecreating:
		return "a rollback that took no safety snapshot was interrupted before it restored any volume; " +
			"the containers may be gone"
	case m.Safety == "":
		return "a rollback that took no safety snapshot was interrupted; volumes may be half-restored"
	case m.Phase == InflightRestoreDone:
		return "every volume was restored and only the services were not started"
	case m.Phase == InflightInconsistent:
		return "the rollback failed and so did its revert"
	default:
		return "volumes may mix states"
	}
}

// InterruptedRollbackError refuses a mutating workflow while a marker stands. What
// happened is the Marker's; the way to recover is the adapter's to name.
type InterruptedRollbackError struct {
	Marker InflightMarker
}

func (m *InterruptedRollbackError) Error() string {
	return fmt.Sprintf("rollback to savepoint %s was interrupted (%s): %s",
		m.Marker.Target, m.Marker.Phase, m.Marker.describeState())
}
