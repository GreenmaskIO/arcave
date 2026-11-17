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

package compose

import (
	"context"
	"errors"
	"time"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/sessionstore"
)

// errForeignPrepared refuses a prepared savepoint another provider handed out.
var errForeignPrepared = errors.New("the savepoint was prepared by another provider")

// preparedSavepoint is a savepoint ready to build, as this provider hands it to core.
type preparedSavepoint struct {
	composemodel.Target
}

// Summary is what building the prepared savepoint restores, and the volumes it overwrites.
func (p preparedSavepoint) Summary() provider.PreparedSummary {
	return provider.PreparedSummary{
		Volumes:        composemodel.VolumesOf(p.Savepoint),
		WritableLayers: p.Restored,
		Overwritten:    p.Overwritten,
	}
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
	return preparedSavepoint{Target: target}, nil
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

// vacantWorkload is the compose project with nothing of it running: the anonymous volumes its teardown
// orphaned, the volumes it owned before the build, and the project a discard takes down.
type vacantWorkload struct {
	service              *Provider
	anonymousVolumeNames map[string]string
	ownedBefore          map[string]bool
	downProject          *types.Project
}

// Build creates the compose project from the prepared savepoint's spec, its containers labelled
// with the deployment, and rebuilds every replica compose cannot name on its own layer and number.
func (v vacantWorkload) Build(ctx context.Context, prepared provider.PreparedSavepoint) (provider.BuiltWorkload, error) {
	target, err := toPreparedSavepoint(prepared)
	if err != nil {
		return nil, err
	}
	project, err := v.service.deployer.Build(ctx, target.Target)
	if err != nil {
		return nil, err
	}
	return builtWorkload{
		vacant:  v,
		target:  target,
		project: project,
	}, nil
}

// Discard runs `compose down` of the project and drops the volumes it owns now and did not own
// before; it returns the down's error.
func (v vacantWorkload) Discard(ctx context.Context) error {
	return v.service.deployer.Discard(ctx, v.downProject, v.ownedBefore)
}

// builtWorkload is the compose project created from the prepared savepoint, labelled, and empty.
type builtWorkload struct {
	vacant  vacantWorkload
	target  preparedSavepoint
	project *types.Project
}

// RestoreData writes every volume of the savepoint back.
func (b builtWorkload) RestoreData(ctx context.Context) (provider.RestoredWorkload, error) {
	if err := b.vacant.service.deployer.RestoreVolumes(ctx, b.target.Savepoint, b.target.Backends); err != nil {
		return nil, err
	}
	return restoredWorkload(b), nil
}

// restoredWorkload is the compose project holding the savepoint's data, not running.
type restoredWorkload struct {
	vacant  vacantWorkload
	target  preparedSavepoint
	project *types.Project
}

// Start starts the services and waits on their health checks.
func (r restoredWorkload) Start(ctx context.Context, waitTimeout time.Duration) error {
	return r.vacant.service.deployer.Start(ctx, r.project, waitTimeout)
}

// Reap drops the anonymous volumes the teardown orphaned and the restore replaced, and says where
// the restored writable layers live.
func (r restoredWorkload) Reap(ctx context.Context) {
	r.vacant.service.deployer.Reap(ctx, r.vacant.anonymousVolumeNames, r.target.Target)
}

// toPreparedSavepoint is prepared as this provider prepared it.
func toPreparedSavepoint(prepared provider.PreparedSavepoint) (preparedSavepoint, error) {
	target, ok := prepared.(preparedSavepoint)
	if !ok {
		return preparedSavepoint{}, errForeignPrepared
	}
	return target, nil
}
