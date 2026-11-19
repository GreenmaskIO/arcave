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
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/deploy"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/sessionstore"
)

// surveyedWorkload is the compose project as one label read found it.
type surveyedWorkload struct {
	service   *composeWorkload
	resources composemodel.ComposeProjectResources
}

// Resources names the containers, volumes and networks of the compose project.
func (w surveyedWorkload) Resources() model.DiscardedResources {
	return w.resources.Discarded()
}

// Present reports whether the daemon holds a container or a volume of the compose project: compose
// would reuse a volume it found, its old data mixed into the build. A network holds no data.
func (w surveyedWorkload) Present() bool {
	return len(w.resources.Containers) > 0 || len(w.resources.Volumes) > 0
}

// Owner judges whose environment the daemon holds by its replicas' deployment labels.
func (w surveyedWorkload) Owner(
	arcaveProject model.ArcaveProjectName, session model.SessionID,
) (*model.SavepointDeployment, error) {
	return deploy.Owner(w.resources, arcaveProject, session)
}

// PrepareSavepoint makes the savepoint id names ready to build once the drop has removed what the
// daemon holds: no leftover volume is judged, none surviving the drop.
func (w surveyedWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	if err := w.service.inspector.RequireDaemon(ctx); err != nil {
		return nil, err
	}
	savepoint, err := sessionstore.LoadSavepoint(ctx, w.service.savepointRepo, id)
	if err != nil {
		return nil, err
	}
	target, err := w.service.preparer.Prepare(ctx, savepoint, opts.Deployment, nil)
	if err != nil {
		return nil, err
	}
	return newPreparedSavepoint(target, false)
}

// Drop removes everything of the compose project and proves it gone; on an empty one it removes
// nothing.
func (w surveyedWorkload) Drop(ctx context.Context) (provider.DroppedWorkload, error) {
	if w.resources.IsEmpty() {
		return droppedWorkload{service: w.service}, nil
	}
	networks, err := w.service.deployer.Drop(ctx, w.resources)
	if err != nil {
		return nil, err
	}
	return droppedWorkload{
		service:      w.service,
		networksLeft: networks,
	}, nil
}
