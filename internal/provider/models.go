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
	"github.com/greenmaskio/arcave/internal/model"
)

// WorkloadName names one workload as its provider does; the provider validates it.
type WorkloadName string

// CaptureOptions are what a capture is planned for.
type CaptureOptions struct {
	// Restoring is the savepoint about to replace the workload, empty for none; when set, the
	// capture keeps everything it overwrites and holds the workload still.
	Restoring model.SavepointID
}

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

// CaptureRequest is what core asks a savepoint to carry. The provider stores every attribute
// with the savepoint and hands it back as given; it decides nothing by any of them.
type CaptureRequest struct {
	// ID is the savepoint's id, minted by core.
	ID          model.SavepointID
	Name        string
	Description string
	Labels      map[string]string
	TakenBefore *model.SavepointRef
	// InResult says the savepoint is the command's own result: what it leaves out is repeated
	// there, and its warnings are marked so.
	InResult bool
}

// PreparedSummary is what building a prepared savepoint restores.
type PreparedSummary struct {
	// Volumes and WritableLayers are what building it restores.
	Volumes        []model.Volume
	WritableLayers []model.WritableLayerRestoreResult
	// Overwritten names what outlived an absent workload and the build overwrites.
	Overwritten []string
}
