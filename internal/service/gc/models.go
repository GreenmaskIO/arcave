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

package gc

import (
	"time"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

// DaemonScope is what a daemon-level collector judges: the daemon, against every compose
// project of the session.
type DaemonScope struct {
	ComposeProjects []model.DockerComposeProjectName
	// Now is when the gc started.
	Now time.Time
	// RestoredLayerGrace is how old an unused layer tag must be to count as garbage.
	RestoredLayerGrace time.Duration
}

// ComposeProjectScope is what a compose-project collector judges: one compose project's
// store and the records that keep its objects alive.
type ComposeProjectScope struct {
	ComposeProject model.DockerComposeProjectName
	Store          storages.Navigator
	// Objects lists Store once, recursively, for every collector of the scope; a namespace of
	// Deps.UnlistedNamespaces is left out.
	Objects []storages.ObjectStat
	// Savepoints are the compose project's committed records, safety snapshots included: the mark.
	Savepoints []model.Savepoint
	// Dead are the savepoint trees no record commits, each left by a run that died.
	Dead []model.SavepointID
}
