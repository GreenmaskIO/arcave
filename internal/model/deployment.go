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
	"time"
)

// DeployedSavepoint is what a deployment says of the savepoint it was built from, as its record
// read when it was built.
type DeployedSavepoint struct {
	ID               SavepointID `json:"id"`
	Name             string      `json:"name,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	IsSafetySnapshot bool        `json:"safety_snapshot"`
}

// String names the savepoint by its name and id, by its id when it has no name.
func (m DeployedSavepoint) String() string {
	if m.Name == "" {
		return m.ID.String()
	}
	return m.Name + " (" + m.ID.String() + ")"
}

// SavepointDeployment is one build of a compose project from a savepoint, as the labels of every
// container it built spell it: what a status reads with no store.
type SavepointDeployment struct {
	DeployedAt time.Time `json:"deployed_at"`
	// ArcaveProject is the arcave project whose store holds Session.
	ArcaveProject ArcaveProjectName `json:"arcave_project"`
	Session       SessionRef        `json:"session"`
	Savepoint     DeployedSavepoint `json:"savepoint"`
	// ServiceReplicas are the replica numbers each service was built with, a gap included.
	ServiceReplicas map[string][]int `json:"service_replicas,omitempty"`
}
