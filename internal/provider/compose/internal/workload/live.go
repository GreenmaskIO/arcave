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
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/sessionstore"
)

// liveWorkload is the compose project as one introspection found it.
type liveWorkload struct {
	service       *composeWorkload
	introspection composemodel.IntrospectionResult
}

// Absent reports whether the daemon holds no container of the compose project.
func (l liveWorkload) Absent() bool {
	return composemodel.IsAbsent(l.introspection)
}

// PlanCapture decides the capture of the live compose project as opts says. The record of
// opts.Restoring is read first, and one that cannot be read is no provider.CapturePolicyError.
func (l liveWorkload) PlanCapture(ctx context.Context, opts provider.CaptureOptions) (provider.CapturePlan, error) {
	var (
		restoring *composemodel.Savepoint
		plan      composemodel.Plan
		err       error
	)
	if opts.Restoring != "" {
		savepoint, err := sessionstore.LoadSavepoint(ctx, l.service.savepointRepo, opts.Restoring)
		if err != nil {
			return nil, err
		}
		restoring = &savepoint
	}
	if plan, err = l.service.planner.Plan(ctx, l.introspection, restoring); err != nil {
		return nil, err
	}
	return l.service.newCapturePlan(plan, l.introspection), nil
}

// PrepareSavepoint makes the savepoint id names ready to build. Over an absent compose project, a
// leftover volume it restores into is refused or named as overwritten, as the policy says.
func (l liveWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	savepoint, err := sessionstore.LoadSavepoint(ctx, l.service.savepointRepo, id)
	if err != nil {
		return nil, err
	}
	var overwritten []string
	if l.Absent() {
		if overwritten, err = l.service.preparer.NewOverwritten(l.introspection, savepoint, opts.Recovering); err != nil {
			return nil, err
		}
	}
	target, err := l.service.preparer.Prepare(ctx, savepoint, opts.Deployment, overwritten)
	if err != nil {
		return nil, err
	}
	return newPreparedSavepoint(target, opts.Recovering)
}

// Teardown resumes the replicas found frozen and runs `compose down` of prepared's project; over
// an absent compose project it removes nothing.
func (l liveWorkload) Teardown(ctx context.Context, prepared provider.PreparedSavepoint) (provider.VacantWorkload, error) {
	target, err := toPreparedSavepoint(prepared)
	if err != nil {
		return nil, err
	}
	if !l.Absent() {
		l.service.deployer.UnpauseOnEntry(ctx, l.introspection)
		if err := l.service.deployer.Down(ctx, target.Project); err != nil {
			return nil, err
		}
	}
	return vacantWorkload{
		service:              l.service,
		anonymousVolumeNames: composemodel.NewAnonymousVolumeNames(l.introspection),
		ownedBefore:          composemodel.OwnedVolumes(l.introspection),
		downProject:          target.Project,
	}, nil
}
