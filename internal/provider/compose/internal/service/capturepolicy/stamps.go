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

package capturepolicy

import "github.com/greenmaskio/arcave/internal/model"

// Stamps spells every decision of policy out as io.arcave.capture.* service labels: what
// a savepoint's spec records, so the spec, and every container a rollback builds from it,
// says what was captured and why. A named volume's decision goes on every service mounting
// it, never on the volume, whose labels compose cannot change without recreating it. A
// mount is stamped by target alone, so the replicas' anonymous volumes at one target are
// stamped with one decision, the last replica's.
func Stamps(introspection model.IntrospectionResult, capturePolicy model.CapturePolicy) map[string]map[string]string {
	stampsByService := map[string]map[string]string{}
	for _, svc := range introspection.Project.Services {
		stamps := map[string]string{}
		serviceDecision := capturePolicy.DecideService(svc.Name)
		writableLayer := serviceDecision.WritableLayer
		stamps[string(model.SystemLabelWritableLayerStamp)] = stamp(writableLayer.CaptureDecision, string(writableLayer.Backend))
		stamps[string(model.SystemLabelImageStamp)] = stamp(serviceDecision.Image, "")

		for _, mount := range svc.Volumes {
			var decision model.VolumeDecision
			switch {
			case mount.Type == model.VolumeTypeNamed && mount.InheritedFrom == "":
				decision = capturePolicy.DecideNamedVolume(mount.VolumeName)
			case isMountOwnedByService(mount):
				decision = capturePolicy.DecideMount(model.MountRef{Service: svc.Name, Replica: mount.Replica, Target: mount.Target})
			default:
				continue
			}
			stamps[string(model.NewMountStampLabel(mount.Target))] = stamp(decision.CaptureDecision, string(decision.Backend))
		}
		stampsByService[svc.Name] = stamps
	}
	return stampsByService
}

// stamp renders one decision; backend is named only for a unit captured.
func stamp(decision model.CaptureDecision, backend string) string {
	if !decision.Capture {
		backend = ""
	}
	origin := decision.Origin
	if origin == "" {
		origin = model.OriginConfig
	}
	return model.CaptureStamp{
		Capture: decision.Capture, Backend: backend, Origin: origin, Source: decision.Source,
	}.String()
}
