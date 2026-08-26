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

package fullcopy

import (
	"github.com/greenmaskio/arcave/internal/model"
)

const testComposeProject = "demo"

func namedVolume(dockerName string, usedBy ...model.ServiceReference) model.Volume {
	return model.Volume{
		ObjectID:       model.NamedVolume{Name: dockerName}.ObjectID(),
		ComposeProject: testComposeProject,
		Type:           model.VolumeTypeNamed,
		Backend:        model.SnapshotBackendCopy,
		Named:          &model.VolumePropertiesNamed{VolumeName: dockerName, UsedBy: usedBy},
	}
}

func bindVolume(service, target string) model.Volume {
	return model.Volume{
		ObjectID:       model.ServiceVolume{Type: model.VolumeTypeBind, Target: target}.ObjectID(service),
		ComposeProject: testComposeProject,
		Type:           model.VolumeTypeBind,
		Backend:        model.SnapshotBackendCopy,
		Bind:           &model.VolumePropertiesBind{Service: service, Target: target, Source: "/host" + target},
	}
}

// bindAddress is what the engine answers for a bindVolume: compose-go resolves
// a bind source to an absolute host path, so no lookup is involved.
func bindAddress(target string) model.MountSource {
	return model.MountSource{Type: model.MountSourceBind, Source: "/host" + target}
}
