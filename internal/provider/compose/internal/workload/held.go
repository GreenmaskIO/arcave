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

package workload

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/hold"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/sessionstore"
)

// heldWorkload is the project held still, what holding it did, the anonymous volumes its teardown
// orphans and the volumes it owned when it was inspected.
type heldWorkload struct {
	service              *composeWorkload
	plan                 composemodel.Plan
	outcome              hold.Outcome
	anonymousVolumeNames map[string]string
	ownedBefore          map[string]bool
}

// Capture stores what is held under a new savepoint, not yet committed.
func (h heldWorkload) Capture(ctx context.Context, req provider.CaptureRequest) (provider.CapturedSavepoint, error) {
	return h.service.capturer.Capture(ctx, h.plan, h.outcome, req)
}

// PrepareSavepoint loads the layers of the savepoint id names back into the daemon and points the
// held project at them, over the backends that captured it: it reads no spec and refuses no image.
func (h heldWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	savepoint, err := sessionstore.LoadSavepoint(ctx, h.service.savepointRepo, id)
	if err != nil {
		return nil, err
	}
	target, err := h.service.preparer.PrepareHeld(ctx, savepoint, h.plan, opts.Deployment)
	if err != nil {
		return nil, err
	}
	return newPreparedSavepoint(target, false)
}

// Teardown runs `compose down` of the held project.
func (h heldWorkload) Teardown(ctx context.Context) (provider.VacantWorkload, error) {
	if err := h.service.deployer.Down(ctx, h.plan.Project); err != nil {
		return nil, err
	}
	return vacantWorkload{
		service:              h.service,
		anonymousVolumeNames: h.anonymousVolumeNames,
		ownedBefore:          h.ownedBefore,
		downProject:          h.plan.Project,
	}, nil
}
