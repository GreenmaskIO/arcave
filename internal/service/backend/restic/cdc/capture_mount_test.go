package cdc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The mount a volume is reached through: its own for a bind or anon volume, and
// for a named one the first mount that exposes the whole of it, preferring a
// writable one so a restore has somewhere to write.
func TestCaptureMount(t *testing.T) {
	tests := []struct {
		name    string
		volume  model.Volume
		want    captureMountRef
		wantErr string
	}{
		{
			name: "a bind mount is its own mount",
			volume: model.Volume{
				ObjectID: "service/app/etc/cfg", Type: model.VolumeTypeBind,
				Bind: &model.VolumePropertiesBind{Service: "app", Target: "/etc/cfg"},
			},
			want: captureMountRef{service: "app", target: "/etc/cfg"},
		},
		{
			name: "an anonymous volume is reachable only through its one mount",
			volume: model.Volume{
				ObjectID: "service/db/data", Type: model.VolumeTypeAnon,
				Anon: &model.VolumePropertiesAnon{Service: "db", Target: "/data"},
			},
			want: captureMountRef{service: "db", target: "/data"},
		},
		{
			name: "a named volume takes its first whole mount",
			volume: model.Volume{
				ObjectID: "volume/pgdata", Type: model.VolumeTypeNamed,
				Named: &model.VolumePropertiesNamed{VolumeName: "pgdata", UsedBy: []model.ServiceReference{
					{Service: "db", Target: "/data"},
					{Service: "api", Target: "/mirror"},
				}},
			},
			want: captureMountRef{service: "db", target: "/data"},
		},
		{
			name: "a subpath mount is skipped: it exposes a fraction of the volume",
			volume: model.Volume{
				ObjectID: "volume/pgdata", Type: model.VolumeTypeNamed,
				Named: &model.VolumePropertiesNamed{VolumeName: "pgdata", UsedBy: []model.ServiceReference{
					{Service: "api", Target: "/part", Subpath: "sub"},
					{Service: "db", Target: "/data"},
				}},
			},
			want: captureMountRef{service: "db", target: "/data"},
		},
		{
			name: "a writable mount wins over a read-only one",
			volume: model.Volume{
				ObjectID: "volume/pgdata", Type: model.VolumeTypeNamed,
				Named: &model.VolumePropertiesNamed{VolumeName: "pgdata", UsedBy: []model.ServiceReference{
					{Service: "api", Target: "/data", ReadOnly: true},
					{Service: "db", Target: "/data"},
				}},
			},
			want: captureMountRef{service: "db", target: "/data"},
		},
		{
			name: "read-only is a fallback, not a disqualification",
			volume: model.Volume{
				ObjectID: "volume/pgdata", Type: model.VolumeTypeNamed,
				Named: &model.VolumePropertiesNamed{VolumeName: "pgdata", UsedBy: []model.ServiceReference{
					{Service: "api", Target: "/data", ReadOnly: true},
				}},
			},
			want: captureMountRef{service: "api", target: "/data"},
		},
		{
			name: "a named volume nothing mounts in full has no way in",
			volume: model.Volume{
				ObjectID: "volume/pgdata", Type: model.VolumeTypeNamed,
				Named: &model.VolumePropertiesNamed{VolumeName: "pgdata", UsedBy: []model.ServiceReference{
					{Service: "api", Target: "/part", Subpath: "sub"},
				}},
			},
			wantErr: "no mount to reach its data through",
		},
		{
			name:    "a volume carrying no addressing arm is refused",
			volume:  model.Volume{ObjectID: "volume/pgdata", Type: model.VolumeTypeNamed},
			wantErr: "carries no addressing properties",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := captureMount(tt.volume)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
