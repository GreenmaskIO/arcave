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

// Package captureplan is one capture of a compose project decided before anything is touched.
package captureplan

import (
	"slices"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// StatePaused is the state of a container frozen, as the daemon reports it.
const StatePaused = "paused"

// Plan is one capture, decided before anything is touched: the compose project held still,
// what is captured, and what is left out.
type Plan struct {
	// Project is the live compose project, as its spec describes it; Spec is that artifact.
	Project *types.Project
	Spec    []byte

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
	// LabelsIgnored is why the capture follows the config and not the compose project's labels,
	// which it could not read; nil when it reads them.
	LabelsIgnored error
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

// PausedOnEntry are the replicas found frozen.
func (h ServiceHold) PausedOnEntry() []model.ContainerRef {
	var paused []model.ContainerRef
	for _, idle := range h.Idle {
		if idle.State == StatePaused {
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

// ServicesHeldBy are the services of the plan held by mode.
func (p Plan) ServicesHeldBy(mode model.CaptureMode) []ServiceHold {
	var held []ServiceHold
	for _, service := range p.Services {
		if service.heldMode() == mode {
			held = append(held, service)
		}
	}
	return held
}

// NewNotCovered is what a capture of plan leaves out, killed being the containers the stop
// killed; nil when it leaves out nothing.
func (p Plan) NewNotCovered(killed []string) *model.NotCovered {
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
