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
	// Safety is the safety snapshot that undoes the interrupted rollback; empty when the compose
	// project had no containers, where running the same rollback again is the fix.
	Safety SavepointID `json:"safety"`
	At     time.Time   `json:"at"`
}

// RecoveryID is the savepoint to roll back to to recover: the safety snapshot, or, when the
// rollback took none, its own target again.
func (m InflightMarker) RecoveryID() SavepointID {
	if m.Safety == "" {
		return m.Target
	}
	return m.Safety
}

// InterruptedRollbackError refuses a mutating workflow while a marker stands. What
// happened and what to do about it is the Marker's; a caller reads it with errors.As.
type InterruptedRollbackError struct {
	Marker InflightMarker
}

func (m *InterruptedRollbackError) Error() string {
	return fmt.Sprintf("rollback to savepoint %s was interrupted (%s)", m.Marker.Target, m.Marker.Phase)
}
