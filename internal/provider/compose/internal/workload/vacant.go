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

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/provider"
)

// vacantWorkload is the compose project with nothing of it running: the anonymous volumes its teardown
// orphaned, the volumes it owned before the build, and the project a discard takes down.
type vacantWorkload struct {
	service              *composeWorkload
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
