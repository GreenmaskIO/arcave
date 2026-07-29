package tarcopy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// TestCaptureMount pins the mount choice: subpath disqualifies, writable wins,
// UsedBy order breaks the tie.
func TestCaptureMount(t *testing.T) {
	tests := []struct {
		name    string
		volume  model.Volume
		want    captureMountRef
		wantErr bool
	}{
		{
			name:   "a bind mount is its own mount",
			volume: bindVolume("app", "/etc/cfg"),
			want:   captureMountRef{service: "app", target: "/etc/cfg"},
		},
		{
			name:   "an anonymous volume is its own mount",
			volume: anonVolume("db", "/data"),
			want:   captureMountRef{service: "db", target: "/data"},
		},
		{
			name:   "a named volume with one mount uses it",
			volume: namedVolume("demo_pgdata", model.ServiceReference{Service: "db", Target: "/var/lib/postgresql/data"}),
			want:   captureMountRef{service: "db", target: "/var/lib/postgresql/data"},
		},
		{
			name: "several usable mounts pick the first, since UsedBy is sorted",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets"},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html"},
			),
			want: captureMountRef{service: "api", target: "/srv/assets"},
		},
		{
			name: "a writable mount beats an earlier read-only one",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", ReadOnly: true},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html"},
			),
			want: captureMountRef{service: "web", target: "/usr/share/nginx/html"},
		},
		{
			name: "read-only everywhere is still taken",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", ReadOnly: true},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true},
			),
			want: captureMountRef{service: "api", target: "/srv/assets"},
		},
		{
			name: "a subpath mount is passed over for a later full one",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", Subpath: "public"},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true},
			),
			want: captureMountRef{service: "web", target: "/usr/share/nginx/html"},
		},
		{
			name: "only subpath mounts is an error, never a partial capture",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", Subpath: "public"},
			),
			wantErr: true,
		},
		{
			name:    "a named volume nobody mounts has no way in",
			volume:  namedVolume("demo_orphan"),
			wantErr: true,
		},
		{
			name:    "a volume with no addressing arm is an error, not a panic",
			volume:  model.Volume{ObjectID: "volume/broken", Type: model.VolumeTypeNamed},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			mount, err := captureMount(tt.volume)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, mount)
		})
	}
}
