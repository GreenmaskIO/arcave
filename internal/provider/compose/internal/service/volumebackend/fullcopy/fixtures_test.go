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
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

const testComposeProject = "demo"

func namedVolume(dockerName string, usedBy ...composemodel.ServiceReference) composemodel.Volume {
	return composemodel.Volume{
		ObjectID:       composemodel.NamedVolume{Name: dockerName}.ObjectID(),
		ComposeProject: testComposeProject,
		Type:           composemodel.VolumeTypeNamed,
		Backend:        composeconfig.SnapshotBackendFullCopy,
		Named:          &composemodel.VolumePropertiesNamed{VolumeName: dockerName, UsedBy: usedBy},
	}
}

func bindVolume(service, target string) composemodel.Volume {
	return composemodel.Volume{
		ObjectID:       composemodel.ServiceVolume{Type: composemodel.VolumeTypeBind, Target: target}.ObjectID(service),
		ComposeProject: testComposeProject,
		Type:           composemodel.VolumeTypeBind,
		Backend:        composeconfig.SnapshotBackendFullCopy,
		Bind:           &composemodel.VolumePropertiesBind{Service: service, Target: target, Source: "/host" + target},
	}
}

// bindAddress is what the engine answers for a bindVolume: compose-go resolves
// a bind source to an absolute host path, so no lookup is involved.
func bindAddress(target string) composemodel.MountSource {
	return composemodel.MountSource{Type: composemodel.MountSourceBind, Source: "/host" + target}
}
