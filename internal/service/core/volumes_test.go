package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestFindBackupVolumes(t *testing.T) {
	tests := []struct {
		name        string
		project     model.Project
		want        []model.Volume
		wantSkipped []model.SkippedVolume
	}{
		{
			name: "named comes from the project level, bind and anon from the service level",
			project: model.Project{
				Name: "demo",
				Services: []model.Service{
					{
						Name: "app",
						Volumes: []model.ServiceVolume{
							{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg", ReadOnly: true},
							{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
						},
					},
					{
						Name: "db",
						Volumes: []model.ServiceVolume{
							// A named mount is a reference to the unit taken below, never a
							// unit of its own — otherwise a shared volume is captured twice.
							{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "demo_pgdata"},
							{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/data", Volume: &model.VolumeMount{Subpath: "inner"}},
						},
					},
				},
				Volumes: []model.NamedVolume{
					{Key: "pgdata", Name: "demo_pgdata", UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}}},
				},
			},
			want: []model.Volume{
				{
					ObjectID:       "volume/demo_pgdata",
					ComposeProject: "demo",
					Type:           model.VolumeTypeNamed,
					Backend:        model.SnapshotBackendCopy,
					Named: &model.VolumePropertiesNamed{
						VolumeName: "demo_pgdata",
						UsedBy:     []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
					},
				},
				{
					ObjectID:       "service/app/etc/cfg",
					ComposeProject: "demo",
					Type:           model.VolumeTypeBind,
					Backend:        model.SnapshotBackendCopy,
					Bind:           &model.VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "/host/cfg", ReadOnly: true},
				},
				{
					ObjectID:       "service/db/data",
					ComposeProject: "demo",
					Type:           model.VolumeTypeAnon,
					Backend:        model.SnapshotBackendCopy,
					Anon:           &model.VolumePropertiesAnon{Service: "db", Target: "/data", Subpath: "inner"},
				},
			},
			wantSkipped: []model.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
			},
		},
		{
			name: "every mount of a shared volume travels with the one unit",
			project: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "api", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, Source: "assets", Target: "/srv/assets", VolumeName: "demo_assets"},
					}},
					{Name: "web", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeNamed, Source: "assets", Target: "/usr/share/nginx/html", VolumeName: "demo_assets"},
					}},
				},
				Volumes: []model.NamedVolume{{
					Key:  "assets",
					Name: "demo_assets",
					UsedBy: []model.ServiceReference{
						{Service: "api", Target: "/srv/assets"},
						{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true, Subpath: "public"},
					},
				}},
			},
			want: []model.Volume{{
				ObjectID:       "volume/demo_assets",
				ComposeProject: "demo",
				Type:           model.VolumeTypeNamed,
				Backend:        model.SnapshotBackendCopy,
				Named: &model.VolumePropertiesNamed{
					VolumeName: "demo_assets",
					UsedBy: []model.ServiceReference{
						{Service: "api", Target: "/srv/assets"},
						{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true, Subpath: "public"},
					},
				},
			}},
		},
		{
			name: "external and unmounted named volumes are captured too",
			project: model.Project{
				Name: "demo",
				Volumes: []model.NamedVolume{
					{Key: "orphan", Name: "demo_orphan"},
					{Key: "shared", Name: "shared_pgdata", External: true},
				},
			},
			want: []model.Volume{
				{ObjectID: "volume/demo_orphan", ComposeProject: "demo", Type: model.VolumeTypeNamed, Backend: model.SnapshotBackendCopy, Named: &model.VolumePropertiesNamed{VolumeName: "demo_orphan"}},
				{ObjectID: "volume/shared_pgdata", ComposeProject: "demo", Type: model.VolumeTypeNamed, Backend: model.SnapshotBackendCopy, Named: &model.VolumePropertiesNamed{VolumeName: "shared_pgdata"}},
			},
		},
		{
			// One case per skipped type, asserting the whole SkippedVolume.
			name: "every type that is not named, bind or anon is skipped with its reason",
			project: model.Project{
				Name: "demo",
				Services: []model.Service{{Name: "app", Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
					{Type: model.VolumeTypeNPipe, RawType: "npipe", Target: `\\.\pipe\docker_engine`},
					{Type: model.VolumeTypeImage, RawType: "image", Source: "assets:1", Target: "/srv/assets"},
					{Type: model.VolumeTypeCluster, RawType: "cluster", Source: "csi-data", Target: "/data"},
					{Type: model.VolumeTypeUnknown, RawType: "something-new", Target: "/new"},
				}}},
			},
			want: nil,
			wantSkipped: []model.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
				{Service: "app", Target: `\\.\pipe\docker_engine`, RawType: "npipe", Reason: "a named pipe is a channel, not data"},
				{Service: "app", Target: "/srv/assets", RawType: "image", Reason: "read-only image content, reproducible from the image"},
				{Service: "app", Target: "/data", RawType: "cluster", Reason: "CSI volume: data lives outside the docker daemon"},
				{Service: "app", Target: "/new", RawType: "something-new", Reason: "unknown mount type"},
			},
		},
		{
			name: "a skipped mount beside a captured one costs the unit nothing",
			project: model.Project{
				Name: "demo",
				Services: []model.Service{{Name: "app", Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
					{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg"},
				}}},
			},
			want: []model.Volume{{
				ObjectID:       "service/app/etc/cfg",
				ComposeProject: "demo",
				Type:           model.VolumeTypeBind,
				Backend:        model.SnapshotBackendCopy,
				Bind:           &model.VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "/host/cfg"},
			}},
			wantSkipped: []model.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
			},
		},
		{
			// Units dedup by identity; skips do not.
			name: "two tmpfs mounts are two skips",
			project: model.Project{
				Name: "demo",
				Services: []model.Service{{Name: "app", Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
					{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/run"},
				}}},
			},
			want: nil,
			wantSkipped: []model.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
				{Service: "app", Target: "/run", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
			},
		},
		{
			name: "two services binding the same host path stay two units",
			project: model.Project{
				Name: "demo",
				Services: []model.Service{
					{Name: "api", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeBind, Source: "/host/cfg", Target: "/etc/cfg"},
					}},
					{Name: "web", Volumes: []model.ServiceVolume{
						{Type: model.VolumeTypeBind, Source: "/host/cfg", Target: "/etc/cfg"},
					}},
				},
			},
			want: []model.Volume{
				{
					ObjectID:       "service/api/etc/cfg",
					ComposeProject: "demo",
					Type:           model.VolumeTypeBind,
					Backend:        model.SnapshotBackendCopy,
					Bind:           &model.VolumePropertiesBind{Service: "api", Target: "/etc/cfg", Source: "/host/cfg"},
				},
				{
					ObjectID:       "service/web/etc/cfg",
					ComposeProject: "demo",
					Type:           model.VolumeTypeBind,
					Backend:        model.SnapshotBackendCopy,
					Bind:           &model.VolumePropertiesBind{Service: "web", Target: "/etc/cfg", Source: "/host/cfg"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, skipped, err := FindBackupVolumes(model.IntrospectionResult{Project: tt.project}, model.SnapshotBackendCopy)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantSkipped, skipped)
			assertOneArmPerType(t, got)
		})
	}
}

// The backend is the caller's, stamped on every unit whatever its addressing
// arm: it is project config, not a constant of this package.
func TestFindBackupVolumes_StampsTheGivenBackend(t *testing.T) {
	// Arrange
	res := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{{
			Name:    "app",
			Volumes: []model.ServiceVolume{{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg"}},
		}},
		Volumes: []model.NamedVolume{{Key: "db", Name: "demo_db",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}}},
	}}

	// Act
	got, _, err := FindBackupVolumes(res, model.SnapshotBackendCDC)

	// Assert
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, v := range got {
		assert.Equal(t, model.SnapshotBackendCDC, v.Backend, "volume %s", v.ObjectID)
	}
}

// TestReachableVolumes covers the one thing today's copy backend cannot do: reach
// a named volume no container exposes in full. Everything else passes through.
func TestReachableVolumes(t *testing.T) {
	var (
		bind = model.Volume{
			ObjectID: "service/app/etc/cfg",
			Type:     model.VolumeTypeBind,
			Bind:     &model.VolumePropertiesBind{Service: "app", Target: "/etc/cfg"},
		}
		anon = model.Volume{
			ObjectID: "service/db/data",
			Type:     model.VolumeTypeAnon,
			// A subpath does not make an anonymous volume unreachable: its one mount
			// is the only address either way, so there is no better one to pass over.
			Anon: &model.VolumePropertiesAnon{Service: "db", Target: "/data", Subpath: "inner"},
		}
	)
	named := func(dockerName string, usedBy ...model.ServiceReference) model.Volume {
		return model.Volume{
			ObjectID: model.NamedVolume{Name: dockerName}.ObjectID(),
			Type:     model.VolumeTypeNamed,
			Named:    &model.VolumePropertiesNamed{VolumeName: dockerName, UsedBy: usedBy},
		}
	}

	tests := []struct {
		name        string
		volumes     []model.Volume
		wantKept    []model.Volume
		wantSkipped []string // the volume name each skip's reason must carry
	}{
		{
			name:     "a mounted named volume, a bind and an anon all pass",
			volumes:  []model.Volume{named("demo_pgdata", model.ServiceReference{Service: "db", Target: "/var/lib/postgresql/data"}), bind, anon},
			wantKept: []model.Volume{named("demo_pgdata", model.ServiceReference{Service: "db", Target: "/var/lib/postgresql/data"}), bind, anon},
		},
		{
			name:        "a named volume nobody mounts has no container to read it through",
			volumes:     []model.Volume{named("demo_orphan")},
			wantSkipped: []string{"demo_orphan"},
		},
		{
			name: "a named volume mounted only at subpaths would be captured in part",
			volumes: []model.Volume{named("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", Subpath: "public"},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html", Subpath: "static"},
			)},
			wantSkipped: []string{"demo_assets"},
		},
		{
			name: "one full mount among subpath ones is enough",
			volumes: []model.Volume{named("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", Subpath: "public"},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html"},
			)},
			wantKept: []model.Volume{named("demo_assets",
				model.ServiceReference{Service: "api", Target: "/srv/assets", Subpath: "public"},
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html"},
			)},
		},
		{
			name: "a read-only mount is still a way in",
			volumes: []model.Volume{named("demo_assets",
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true},
			)},
			wantKept: []model.Volume{named("demo_assets",
				model.ServiceReference{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true},
			)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			kept, skipped := reachableVolumes(tt.volumes)

			// Assert
			assert.Equal(t, tt.wantKept, kept)
			require.Len(t, skipped, len(tt.wantSkipped))
			for i, volumeName := range tt.wantSkipped {
				// No service or target to name, so the reason must carry the volume's.
				assert.Contains(t, skipped[i].Reason, volumeName)
				assert.Equal(t, "volume", skipped[i].RawType)
			}
		})
	}
}

// assertOneArmPerType pins the invariant no type expresses: exactly one
// addressing arm is non-nil, and it is the one Type names.
func assertOneArmPerType(t *testing.T, volumes []model.Volume) {
	t.Helper()

	for _, v := range volumes {
		set := map[model.VolumeType]bool{
			model.VolumeTypeNamed: v.Named != nil,
			model.VolumeTypeBind:  v.Bind != nil,
			model.VolumeTypeAnon:  v.Anon != nil,
		}
		for volumeType, isSet := range set {
			assert.Equal(t, volumeType == v.Type, isSet, "volume %s of type %s: arm %s", v.ObjectID, v.Type, volumeType)
		}
	}
}
