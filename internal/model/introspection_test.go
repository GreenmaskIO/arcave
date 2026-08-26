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

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceVolume_StorageID(t *testing.T) {
	tests := []struct {
		name    string
		service string
		volume  ServiceVolume
		want    string
	}{
		{
			name:    "named volume is identified by its docker name",
			service: "db",
			volume:  ServiceVolume{Type: VolumeTypeNamed, VolumeName: "demo_pgdata", Target: "/var/lib/postgresql/data"},
			want:    "volume-named-demo_pgdata",
		},
		{
			name:    "bind mount is identified by service and target",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeBind, Source: "/host/cfg", Target: "/etc/cfg"},
			want:    "volume-service-app-etc-cfg",
		},
		{
			name:    "anonymous volume is identified by service and target",
			service: "db",
			volume:  ServiceVolume{Type: VolumeTypeAnon, Target: "/data"},
			want:    "volume-service-db-data",
		},
		{
			name:    "a target of several segments flattens into one",
			service: "db",
			volume:  ServiceVolume{Type: VolumeTypeAnon, Target: "/var/lib/postgresql/data"},
			want:    "volume-service-db-var-lib-postgresql-data",
		},
		{
			name:    "tmpfs has no identity",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
			want:    "",
		},
		{
			name:    "npipe has no identity",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeNPipe, RawType: "npipe", Target: `\\.\pipe\docker_engine`},
			want:    "",
		},
		{
			name:    "cluster volume has no identity",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeCluster, RawType: "cluster", Source: "csi-data", Target: "/data"},
			want:    "",
		},
		{
			name:    "image mount has no identity",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeImage, RawType: "image", Source: "assets:1", Target: "/srv/assets"},
			want:    "",
		},
		{
			name:    "an unknown mount type has no identity",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeUnknown, RawType: "something-new", Target: "/data"},
			want:    "",
		},
		{
			name:    "a volume and a service of the same name do not collide",
			service: "db",
			volume:  ServiceVolume{Type: VolumeTypeBind, Source: "/host/db", Target: "/db"},
			want:    "volume-service-db-db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.volume.ObjectID(tt.service)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// A named ServiceVolume is only a reference to its NamedVolume, so the ids match.
func TestNamedVolume_StorageID(t *testing.T) {
	// Arrange
	named := NamedVolume{Key: "pgdata", Name: "demo_pgdata"}
	reference := ServiceVolume{Type: VolumeTypeNamed, VolumeName: "demo_pgdata"}

	// Act
	got := named.ObjectID()

	// Assert
	assert.Equal(t, "volume-named-demo_pgdata", got)
	assert.Equal(t, got, reference.ObjectID("db"))
}
