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
func Stamps(res model.IntrospectionResult, policy model.CapturePolicy) map[string]map[string]string {
	all := map[string]map[string]string{}
	for _, svc := range res.Project.Services {
		stamps := map[string]string{}
		images := policy.DecideService(svc.Name)
		stamps[model.StampWritableLayer] = stamp(images.WritableLayer.CaptureDecision, string(images.WritableLayer.Backend))
		stamps[model.StampImage] = stamp(images.Image, "")

		for _, sv := range svc.Volumes {
			var decision model.VolumeDecision
			switch {
			case sv.Type == model.VolumeTypeNamed && sv.InheritedFrom == "":
				decision = policy.DecideNamedVolume(sv.VolumeName)
			case isMountOwnedByService(sv):
				decision = policy.DecideMount(model.MountRef{Service: svc.Name, Replica: sv.Replica, Target: sv.Target})
			default:
				continue
			}
			stamps[model.MountStamp(sv.Target)] = stamp(decision.CaptureDecision, string(decision.Backend))
		}
		all[svc.Name] = stamps
	}
	return all
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
