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

import "encoding/json"

// RollbackResult is what a rollback did, and the JSON 'arcave rollback' prints.
type RollbackResult struct {
	RolledBackTo Savepoint `json:"rolled_back_to"`
	// Restored is what the provider sums the build up as: its units and what to say of it.
	Restored RestoreSummary `json:"restored"`
	// Detail is the provider's body of what the build restored.
	Detail json.RawMessage `json:"detail,omitempty"`
	// SafetySnapshot undoes the rollback: roll back to it by id, a committed savepoint a gc retires.
	// Omitted when none was taken.
	SafetySnapshot SavepointID `json:"safety_snapshot,omitempty"`
	// LeftoverVolumes are the docker names of the volumes that outlived the compose project's
	// containers and the rollback overwrote, with no safety snapshot holding what they held. Only a
	// rollback of a compose project with no containers names any; a recovery's, never.
	LeftoverVolumes []string `json:"leftover_volumes,omitempty"`
}

// RestoreSummary is what a provider sums a build of a savepoint up as.
type RestoreSummary struct {
	// Units count what the build restored by kind, in the order to print them.
	Units []UnitTally `json:"units"`
	// Notices say what the build warns of, the severe first.
	Notices []Notice `json:"notices,omitempty"`
}
