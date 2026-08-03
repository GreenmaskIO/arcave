package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// What the compose project alone can answer: a named volume by its docker name,
// a bind by its host path. Everything else has to ask the daemon.
func TestVolume_Address(t *testing.T) {
	tests := []struct {
		name   string
		volume Volume
		wantOK bool
		want   VolumeAddress
	}{
		{
			name:   "a named volume is its docker name",
			volume: Volume{Type: VolumeTypeNamed, Named: &VolumePropertiesNamed{VolumeName: "demo_pgdata"}},
			wantOK: true,
			want:   VolumeAddress{Kind: VolumeAddressVolume, Source: "demo_pgdata"},
		},
		{
			name: "a named volume nobody mounts is still addressable",
			volume: Volume{
				Type:  VolumeTypeNamed,
				Named: &VolumePropertiesNamed{VolumeName: "demo_orphan"},
			},
			wantOK: true,
			want:   VolumeAddress{Kind: VolumeAddressVolume, Source: "demo_orphan"},
		},
		{
			name: "a named volume mounted only at a subpath is addressed whole",
			volume: Volume{
				Type: VolumeTypeNamed,
				Named: &VolumePropertiesNamed{
					VolumeName: "demo_assets",
					UsedBy:     []ServiceReference{{Service: "web", Target: "/srv", Subpath: "public"}},
				},
			},
			wantOK: true,
			want:   VolumeAddress{Kind: VolumeAddressVolume, Source: "demo_assets"},
		},
		{
			name:   "a named volume carrying no docker name cannot be addressed",
			volume: Volume{Type: VolumeTypeNamed, Named: &VolumePropertiesNamed{}},
		},
		{
			name: "a bind is its host path",
			volume: Volume{
				Type: VolumeTypeBind,
				Bind: &VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "/host/cfg"},
			},
			wantOK: true,
			want:   VolumeAddress{Kind: VolumeAddressBind, Source: "/host/cfg"},
		},
		{
			name: "a relative bind source is not an address",
			volume: Volume{
				Type: VolumeTypeBind,
				Bind: &VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "./cfg"},
			},
		},
		{
			name: "a bind record carrying no source is not an address",
			volume: Volume{
				Type: VolumeTypeBind,
				Bind: &VolumePropertiesBind{Service: "app", Target: "/etc/cfg"},
			},
		},
		{
			name: "an anonymous volume's docker name is created per container",
			volume: Volume{
				Type: VolumeTypeAnon,
				Anon: &VolumePropertiesAnon{Service: "db", Target: "/data"},
			},
		},
		{
			name:   "a volume with no addressing arm at all",
			volume: Volume{ObjectID: "volume/broken", Type: VolumeTypeNamed},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, ok := tt.volume.Address()

			// Assert
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.want, got)
				return
			}
			assert.Equal(t, VolumeAddress{}, got, "a refused address must carry nothing bindable")
		})
	}
}

// The fallback the daemon is asked through: only a volume with a mount of its
// own has one, and a named volume never does — it is addressed by name.
func TestVolume_LookupMount(t *testing.T) {
	tests := []struct {
		name       string
		volume     Volume
		wantOK     bool
		wantSvc    string
		wantTarget string
	}{
		{
			name: "an anonymous volume is reached through its one mount",
			volume: Volume{
				Type: VolumeTypeAnon,
				Anon: &VolumePropertiesAnon{Service: "db", Target: "/data"},
			},
			wantOK:     true,
			wantSvc:    "db",
			wantTarget: "/data",
		},
		{
			name: "a bind whose source did not resolve is reached through its mount",
			volume: Volume{
				Type: VolumeTypeBind,
				Bind: &VolumePropertiesBind{Service: "app", Target: "/etc/cfg"},
			},
			wantOK:     true,
			wantSvc:    "app",
			wantTarget: "/etc/cfg",
		},
		{
			name: "a named volume has no single mount to be looked up through",
			volume: Volume{
				Type: VolumeTypeNamed,
				Named: &VolumePropertiesNamed{
					VolumeName: "demo_pgdata",
					UsedBy:     []ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
				},
			},
		},
		{
			name:   "a volume with no addressing arm at all",
			volume: Volume{ObjectID: "volume/broken", Type: VolumeTypeNamed},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			service, target, ok := tt.volume.LookupMount()

			// Assert
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantSvc, service)
			assert.Equal(t, tt.wantTarget, target)
		})
	}
}
