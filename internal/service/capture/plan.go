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

// statePaused is the state of a container frozen, as the daemon reports it.
const statePaused = "paused"

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

	// Services are how each service is held still, its replicas as they were found.
	Services []ServiceHold
	// Containers are every replica, to name the ones a stop killed.
	Containers []model.ContainerRef
	// NotCovered is what the capture leaves out, as known before the stop.
	NotCovered model.NotCovered
}

// ServiceHold is how one service is held still and the replicas it was found with.
type ServiceHold struct {
	Service string
	Mode    model.CaptureMode
	// Running replicas are the ones a hold acts on; Idle ones are already still.
	Running []model.ContainerRef
	Idle    []IdleReplica
}

// IdleReplica is a replica found not running, or frozen; State is the daemon's word for it.
type IdleReplica struct {
	Ref   model.ContainerRef
	State string
}

// Request is what one capture is recorded as.
type Request struct {
	Name        string
	Description string
	Labels      map[string]string
	// TakenBefore is the rollback target of a safety snapshot; nil for any other savepoint.
	TakenBefore *model.SavepointRef
}

// pausedOnEntry are the replicas found frozen.
func (h ServiceHold) pausedOnEntry() []model.ContainerRef {
	var paused []model.ContainerRef
	for _, idle := range h.Idle {
		if idle.State == statePaused {
			paused = append(paused, idle.Ref)
		}
	}
	return paused
}

// heldMode is the mode the service is held by; an unknown one stops it.
func (h ServiceHold) heldMode() model.CaptureMode {
	switch h.Mode {
	case model.CaptureModePause, model.CaptureModeLive:
		return h.Mode
	default:
		return model.CaptureModeStop
	}
}

// servicesHeldBy are the services of the plan held by mode.
func (p Plan) servicesHeldBy(mode model.CaptureMode) []ServiceHold {
	var held []ServiceHold
	for _, service := range p.Services {
		if service.heldMode() == mode {
			held = append(held, service)
		}
	}
	return held
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
