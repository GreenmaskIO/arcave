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
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// Workload is one workload of one session, built at the DI root for one command.
type Workload interface {
	// Inspect reads the workload as it runs and refuses a daemon it cannot work on. It changes
	// nothing.
	Inspect(ctx context.Context) (LiveWorkload, error)
}

// LiveWorkload is the workload as Inspect found it; nothing is touched.
type LiveWorkload interface {
	// Absent tells whether nothing of the workload exists to hold or to tear down: it was never
	// created, or was removed. One that exists and does not run is not absent.
	Absent() bool
	// PlanCapture decides what a capture takes and what it leaves out; it changes nothing. An
	// absent workload is model.ErrComposeProjectAbsent, and a capture that would take nothing is
	// refused too.
	PlanCapture(ctx context.Context, opts CaptureOptions) (CapturePlan, error)
	// PrepareSavepoint makes the savepoint id names ready to build; whatever can refuse it, the
	// provider's overwrite policy included, refuses here, before anything of the workload is
	// stopped or written. It may stage what the build needs beside the workload.
	PrepareSavepoint(ctx context.Context, id model.SavepointID, opts PrepareOptions) (PreparedSavepoint, error)
	// Teardown resumes what is frozen and removes the workload as prepared describes it, whatever
	// built what stands there, capturing nothing of it; on an absent one it removes nothing. Its
	// data stays.
	Teardown(ctx context.Context, prepared PreparedSavepoint) (VacantWorkload, error)
}

// CapturePlan is one capture, decided; nothing is touched.
type CapturePlan interface {
	// LabelsIgnored is why the plan follows the config and not the workload's own labels, which it
	// could not read; nil when it reads them.
	LabelsIgnored() error
	// Hold holds the workload as the plan decided: stopped, frozen, or left as it runs.
	Hold(ctx context.Context) (HeldWorkload, error)
	// Release sets it running again, whether Hold ran, failed partway or never ran. The caller
	// hands it a ctx that outlives the interrupt: Release does not strip its cancellation.
	Release(ctx context.Context, waitTimeout time.Duration) error
}

// HeldWorkload is the workload held as its plan decided.
type HeldWorkload interface {
	// Capture stores the workload under req.ID, a savepoint not yet committed: no listing holds it.
	Capture(ctx context.Context, req CaptureRequest) (CapturedSavepoint, error)
	// PrepareSavepoint makes the savepoint id names, one captured of this held workload, ready to
	// build over the workload as it is held.
	PrepareSavepoint(ctx context.Context, id model.SavepointID, opts PrepareOptions) (PreparedSavepoint, error)
	// Teardown removes the held workload; its data stays. It may be called again, and then removes
	// whatever a Build since left in its place.
	Teardown(ctx context.Context) (VacantWorkload, error)
}

// CapturedSavepoint is the workload stored and not committed: no listing holds it. One that is
// never committed is left to a gc.
type CapturedSavepoint interface {
	// Commit writes the savepoint's record, the last object: once it returns, it exists.
	Commit(ctx context.Context) (model.Savepoint, error)
}

// PreparedSavepoint is a savepoint ready to build.
type PreparedSavepoint interface {
	// Summary is what a rollback's result says of building it.
	Summary() PreparedSummary
}

// VacantWorkload is the workload torn down: nothing of it is left but its data.
type VacantWorkload interface {
	// Build creates the workload prepared describes: nothing restored, nothing running.
	Build(ctx context.Context, prepared PreparedSavepoint) (BuiltWorkload, error)
	// Discard removes what Build created, the storage it created included. Best-effort.
	Discard(ctx context.Context) error
}

// BuiltWorkload is the workload created and empty.
type BuiltWorkload interface {
	// RestoreData puts the prepared savepoint's data in place.
	RestoreData(ctx context.Context) (RestoredWorkload, error)
}

// RestoredWorkload is the workload holding the savepoint's data, not running.
type RestoredWorkload interface {
	// Start sets it running and waits for it to be healthy.
	Start(ctx context.Context, waitTimeout time.Duration) error
	// Reap removes what the teardown orphaned and the restore replaced. Best-effort.
	Reap(ctx context.Context)
}
