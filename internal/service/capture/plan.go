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

package capture

import (
	"slices"
	"time"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// Plan is one capture, decided before anything is touched: the compose project held still,
// what is captured, and what is left out.
type Plan struct {
	// Project is the live compose project, as its spec describes it; Spec is that artifact.
	Project     *types.Project
	Spec        []byte
	WaitTimeout time.Duration

	// What the capture takes: one backend per volume, the distinct images the captured
	// services run on, and the writable layers, each with its backend.
	Volumes        []model.VolumeSnapshotBackend
	Images         []model.ImageCaptureRequest
	WritableLayers []model.WritableLayerCaptureRequest

	// Containers are every replica, to name the ones a stop killed.
	Containers []model.ContainerRef
	// NotCovered is what the capture leaves out, as known before the stop.
	NotCovered model.NotCovered
}

// Request is what one capture is recorded as.
type Request struct {
	Name        string
	Description string
	Labels      map[string]string
	// TakenBefore is the rollback target of a safety snapshot; nil for any other savepoint.
	TakenBefore *model.SavepointRef
}

// newNotCovered is what a capture of plan leaves out, killed being the containers the stop
// killed; nil when it leaves out nothing.
func (p Plan) newNotCovered(killed []string) *model.NotCovered {
	// A copy of the plan's whole, so a field model.NotCovered gains is carried with no line here.
	notCovered := p.NotCovered
	notCovered.KilledContainers = p.newKilledContainers(killed)
	if notCovered.IsEmpty() {
		return nil
	}
	return &notCovered
}

// newKilledContainers names the replica behind each container id the stop killed.
func (p Plan) newKilledContainers(ids []string) []model.KilledContainer {
	var killedContainers []model.KilledContainer
	for _, id := range ids {
		killed := model.KilledContainer{Container: id}
		if i := slices.IndexFunc(p.Containers, func(c model.ContainerRef) bool { return c.ID == id }); i >= 0 {
			ref := p.Containers[i]
			killed = model.KilledContainer{
				Service:   ref.Service,
				Replica:   ref.Replica,
				Container: ref.Name,
			}
		}
		killedContainers = append(killedContainers, killed)
	}
	return killedContainers
}
