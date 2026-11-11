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

import "fmt"

// RollbackResult is what a rollback did, and the JSON 'arcave rollback' prints. Restored names each
// unit by the same object id the storage layout is keyed by.
type RollbackResult struct {
	RolledBackTo   Savepoint                    `json:"rolled_back_to"`
	Restored       []Volume                     `json:"restored"`
	WritableLayers []WritableLayerRestoreResult `json:"writable_layers,omitempty"`
	// SafetySnapshot undoes the rollback: roll back to it by id, a committed savepoint a gc retires.
	// Omitted when none was taken.
	SafetySnapshot SavepointID `json:"safety_snapshot,omitempty"`
	// LeftoverVolumes are the docker names of the volumes that outlived the compose project's
	// containers and the rollback overwrote, with no safety snapshot holding what they held. Only a
	// rollback of a compose project with no containers names any; a recovery's, never.
	LeftoverVolumes []string `json:"leftover_volumes,omitempty"`
}

// Notices spells out what the rollback warns of: every overwritten leftover volume, then the
// containers running on the savepoint's writable layers. Nil when there is nothing to say.
func (m RollbackResult) Notices() []Notice {
	var notices []Notice
	for _, name := range m.LeftoverVolumes {
		notices = append(notices, NewLeftoverVolumeNotice(name))
	}
	if len(m.WritableLayers) > 0 {
		notices = append(notices, Notice{
			Message: "the containers run on the savepoint's writable layers",
			Hint:    "`docker compose up` of your compose file recreates them on its images and drops those layers",
		})
	}
	return notices
}

// NewLeftoverVolumeNotice says that a rollback overwrote a volume no safety snapshot holds.
func NewLeftoverVolumeNotice(name string) Notice {
	return Notice{
		Message: fmt.Sprintf("volume %s outlived the compose project's containers and was overwritten: "+
			"no safety snapshot holds what it held", name),
		Hint:   "rollback.leftover_volumes: refuse refuses such a rollback before anything is written",
		Severe: true,
	}
}
