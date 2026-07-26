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
			want:    "volume/demo_pgdata",
		},
		{
			name:    "bind mount is identified by service and target",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeBind, Source: "/host/cfg", Target: "/etc/cfg"},
			want:    "service/app/etc/cfg",
		},
		{
			name:    "anonymous volume is identified by service and target",
			service: "db",
			volume:  ServiceVolume{Type: VolumeTypeAnon, Target: "/data"},
			want:    "service/db/data",
		},
		{
			name:    "unsupported mount has no identity",
			service: "app",
			volume:  ServiceVolume{Type: VolumeTypeUnsupported, RawType: "tmpfs", Target: "/scratch"},
			want:    "",
		},
		{
			name:    "a volume and a service of the same name do not collide",
			service: "db",
			volume:  ServiceVolume{Type: VolumeTypeBind, Source: "/host/db", Target: "/db"},
			want:    "service/db/db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.volume.StorageID(tt.service)

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
	got := named.StorageID()

	// Assert
	assert.Equal(t, "volume/demo_pgdata", got)
	assert.Equal(t, got, reference.StorageID("db"))
}
