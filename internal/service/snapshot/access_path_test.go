package snapshot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// TestAccessPath pins which mount the copy backend reaches each unit through. The
// named cases are the whole policy: subpath disqualifies, writable wins among
// what is left, UsedBy order breaks the tie.
func TestAccessPath(t *testing.T) {
	tests := []struct {
		name        string
		volume      model.Volume
		wantService string
		wantTarget  string
		wantErr     bool
	}{
		{
			name:        "a bind mount is its own mount",
			volume:      bindVolume("app", "/etc/cfg"),
			wantService: "app",
			wantTarget:  "/etc/cfg",
		},
		{
			name:        "an anonymous volume is its own mount",
			volume:      anonVolume("db", "/data"),
			wantService: "db",
			wantTarget:  "/data",
		},
		{
			name:        "a named volume with one mount uses it",
			volume:      namedVolume("demo_pgdata", model.ServiceReference{Service: "db", Target: "/var/lib/postgresql/data"}),
			wantService: "db",
			wantTarget:  "/var/lib/postgresql/data",
		},
		{
			name: "several usable mounts pick the first, since UsedBy is sorted",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets"},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html"},
			),
			wantService: "api",
			wantTarget:  "/srv/assets",
		},
		{
			name: "a writable mount beats an earlier read-only one",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", ReadOnly: true},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html"},
			),
			wantService: "web",
			wantTarget:  "/usr/share/nginx/html",
		},
		{
			// Reading works through a read-only mount; only the restore fails, and
			// that is a separate decision from whether to capture at all.
			name: "read-only everywhere is still taken",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", ReadOnly: true},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true},
			),
			wantService: "api",
			wantTarget:  "/srv/assets",
		},
		{
			// Disqualifying, not deprioritised: passed over though it comes first
			// and is the writable one.
			name: "a subpath mount is passed over for a later full one",
			volume: namedVolume("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", Subpath: "public"},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true},
			),
			wantService: "web",
			wantTarget:  "/usr/share/nginx/html",
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
			service, target, err := accessPath(tt.volume)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantService, service)
			assert.Equal(t, tt.wantTarget, target)
		})
	}
}
