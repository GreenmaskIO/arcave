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

package provider

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

// Savepoints is what the provider keeps, read and removed without touching any workload.
type Savepoints interface {
	// List returns every committed savepoint, oldest first, attributes as they were given.
	List(ctx context.Context) ([]model.Savepoint, error)
	// Load returns the savepoint id names; found is false for one never committed or removed since.
	Load(ctx context.Context, id model.SavepointID) (savepoint model.Savepoint, found bool, err error)
	// Remove uncommits the savepoint id names, then deletes what was stored under it; one already
	// gone is no error. Several savepoints may be removed at once.
	Remove(ctx context.Context, id model.SavepointID) error
}

// Session is one session as the provider holds it: every workload with savepoints in it, and
// whatever those savepoints left in the store and on the daemon.
type Session interface {
	// ID is the session's id.
	ID() model.SessionID
	// Workloads names the workloads the session holds anything of, running or not, sorted.
	Workloads(ctx context.Context) ([]WorkloadName, error)
	// Savepoints opens one workload's savepoints.
	Savepoints(workload WorkloadName) (Savepoints, error)
	// FindGarbage finds what no committed savepoint and nothing running holds among dataKinds, none
	// named being every one, and changes nothing; a place it could not read is an aborted report.
	FindGarbage(ctx context.Context, dataKinds []model.GarbageDataKind) []Garbage
}

// Garbage is one kind's findings in one place.
type Garbage interface {
	// Report is what was found, with no deletion.
	Report() model.GarbageReport
	// Delete carries the report's ops out until ctx is done; what it could not is in the deletion.
	Delete(ctx context.Context) model.GarbageDeletion
}
