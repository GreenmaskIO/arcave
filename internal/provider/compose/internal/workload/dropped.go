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
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/deploy"
)

// droppedWorkload is the compose project dropped, and the networks the drop left.
type droppedWorkload struct {
	service      *composeWorkload
	networksLeft []deploy.LeftNetwork
}

// Notices names the networks the drop left, and why the daemon kept them.
func (d droppedWorkload) Notices() []model.Notice {
	notices := make([]model.Notice, 0, len(d.networksLeft))
	for _, network := range d.networksLeft {
		notices = append(notices, model.Notice{
			Message: fmt.Sprintf("network %s left behind: %v", network.Name, network.Cause),
		})
	}
	return notices
}

// Vacant is the dropped compose project ready to build prepared: nothing of it survived, so a
// failed build owes it no volume and orphaned none.
func (d droppedWorkload) Vacant(prepared provider.PreparedSavepoint) (provider.VacantWorkload, error) {
	target, err := toPreparedSavepoint(prepared)
	if err != nil {
		return nil, err
	}
	return vacantWorkload{
		service:              d.service,
		anonymousVolumeNames: map[string]string{},
		ownedBefore:          map[string]bool{},
		downProject:          target.Project,
	}, nil
}

// RemoveRestoredLayers takes off every restored layer tag of this arcave project's compose project.
func (d droppedWorkload) RemoveRestoredLayers(ctx context.Context) ([]model.ImageRef, []model.Notice) {
	return d.service.deployer.RemoveRestoredLayers(ctx)
}
