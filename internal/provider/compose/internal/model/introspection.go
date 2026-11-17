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

package model

import "github.com/greenmaskio/arcave/internal/model"

// IsAbsent reports whether the daemon holds no container of the compose project:
// it was never started, or was taken down.
func IsAbsent(introspection model.IntrospectionResult) bool {
	return len(introspection.Project.Services) == 0
}

// OwnedVolumes are the docker names of the volumes the compose project owns.
func OwnedVolumes(introspection model.IntrospectionResult) map[string]bool {
	owned := map[string]bool{}
	for _, v := range introspection.Project.Volumes {
		if !v.External {
			owned[v.Name] = true
		}
	}
	return owned
}

// NewAnonymousVolumeNames indexes the docker names of the compose project's anonymous volumes by
// the identity their data is captured under.
func NewAnonymousVolumeNames(introspection model.IntrospectionResult) map[string]string {
	names := map[string]string{}
	for _, svc := range introspection.Project.Services {
		for _, mount := range svc.Volumes {
			if mount.Type == model.VolumeTypeAnon && mount.VolumeName != "" {
				names[mount.ObjectID(svc.Name)] = mount.VolumeName
			}
		}
	}
	return names
}

// VolumesOf projects a savepoint's stats back to volumes.
func VolumesOf(savepoint model.Savepoint) []model.Volume {
	volumes := make([]model.Volume, 0, len(savepoint.Stats))
	for _, stat := range savepoint.Stats {
		volumes = append(volumes, stat.Volume)
	}
	return volumes
}
