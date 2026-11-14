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

// Package provider is the stages a workload is run through, one handle per stage; a provider
// implements them, core and gc call them. A stage that fails returns a nil handle.
package provider

import (
	"context"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// Workload is one workload of one session, a compose project for the compose provider, built at
// the DI root for one command.
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

// CaptureOptions are what a capture is planned for.
type CaptureOptions struct {
	// Restoring is the savepoint about to replace the workload, empty for none; when set, the
	// capture keeps everything it overwrites and holds the workload still.
	Restoring model.SavepointID
}

// CapturePolicyError refuses a capture whose policy could not be resolved.
type CapturePolicyError struct {
	Err error
}

func (e *CapturePolicyError) Error() string { return e.Err.Error() }

func (e *CapturePolicyError) Unwrap() error { return e.Err }

// PrepareOptions are what a savepoint is prepared to build by.
type PrepareOptions struct {
	// Deployment is what the build is of; the provider stamps it as given on what it creates,
	// adding when it built and the replicas.
	Deployment model.SavepointDeployment
	// Recovering says the rollback repairs an interrupted one: what the workload still holds is
	// that rollback's own writes, never something to refuse. The summary names it all the same;
	// what to say of it is the caller's.
	Recovering bool
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
	// Capture stores the workload under a savepoint not yet committed: no listing holds it.
	Capture(ctx context.Context, req CaptureRequest) (CapturedSavepoint, error)
	// PrepareSavepoint makes the savepoint id names, one captured of this held workload, ready to
	// build over the workload as it is held.
	PrepareSavepoint(ctx context.Context, id model.SavepointID, opts PrepareOptions) (PreparedSavepoint, error)
	// Teardown removes the held workload; its data stays. It may be called again, and then removes
	// whatever a Build since left in its place.
	Teardown(ctx context.Context) (VacantWorkload, error)
}

// CaptureRequest is what core asks a savepoint to carry. The provider stores every attribute
// with the savepoint and hands it back as given; it decides nothing by any of them.
type CaptureRequest struct {
	Name        string
	Description string
	Labels      map[string]string
	TakenBefore *model.SavepointRef
	// InResult says the savepoint is the command's own result: what it leaves out is repeated
	// there, and its warnings are marked so.
	InResult bool
}

// CapturedSavepoint is the workload stored and not committed: no listing holds it. Exactly one of
// Commit and Abandon ends it.
type CapturedSavepoint interface {
	// ID is the savepoint's id, minted by the capture.
	ID() model.SavepointID
	// Commit writes the savepoint's record, the last object: once it returns, it exists.
	// A Commit that fails has abandoned the capture itself.
	Commit(ctx context.Context) (model.Savepoint, error)
	// Abandon marks the capture failed with cause and leaves what was stored to a gc, whether ctx
	// is cancelled or not.
	Abandon(ctx context.Context, cause error)
}

// Savepoints is what the provider keeps, read and removed without touching any workload.
type Savepoints interface {
	// List returns every committed savepoint, oldest first, attributes as they were given.
	List(ctx context.Context) ([]model.Savepoint, error)
	// Remove uncommits the savepoint id names, then deletes what was stored under it; one already
	// gone is no error. Several savepoints may be removed at once.
	Remove(ctx context.Context, id model.SavepointID) error
}

// WorkloadName names one workload, a compose project, as its provider does; the provider validates it.
type WorkloadName string

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

// PreparedSavepoint is a savepoint ready to build.
type PreparedSavepoint interface {
	// Summary is what a rollback's result says of building it.
	Summary() PreparedSummary
}

// PreparedSummary is what building a prepared savepoint restores.
type PreparedSummary struct {
	// Volumes and WritableLayers are what building it restores.
	Volumes        []model.Volume
	WritableLayers []model.WritableLayerRestoreResult
	// Overwritten names what outlived an absent workload and the build overwrites.
	Overwritten []string
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
