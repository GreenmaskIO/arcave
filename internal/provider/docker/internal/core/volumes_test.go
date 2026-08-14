package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
	stackmodel "github.com/greenmaskio/arcave/internal/provider/docker/stack/model"
)

func TestFindBackupVolumes(t *testing.T) {
	tests := []struct {
		name        string
		project     stackmodel.ComposeProject
		want        []dockermodel.Volume
		wantSkipped []dockermodel.SkippedVolume
	}{
		{
			name: "named comes from the project level, bind and anon from the service level",
			project: stackmodel.ComposeProject{
				Name: "demo",
				Services: []stackmodel.Service{
					{
						Name: "app",
						Volumes: []stackmodel.ServiceVolume{
							{Type: dockermodel.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg", ReadOnly: true},
							{Type: dockermodel.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
						},
					},
					{
						Name: "db",
						Volumes: []stackmodel.ServiceVolume{
							// A named mount is a reference to the unit taken below, never a
							// unit of its own — otherwise a shared volume is captured twice.
							{Type: dockermodel.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "demo_pgdata"},
							{Type: dockermodel.VolumeTypeAnon, RawType: "volume", Target: "/data", Volume: &stackmodel.VolumeMount{Subpath: "inner"}},
						},
					},
				},
				Volumes: []stackmodel.NamedVolume{
					{Key: "pgdata", Name: "demo_pgdata", UsedBy: []dockermodel.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}}},
				},
			},
			want: []dockermodel.Volume{
				{
					ObjectID:       "volume-named-demo_pgdata",
					ComposeProject: "demo",
					Type:           dockermodel.VolumeTypeNamed,
					Backend:        dockermodel.SnapshotBackendCopy,
					Named: &dockermodel.VolumePropertiesNamed{
						VolumeName: "demo_pgdata",
						UsedBy:     []dockermodel.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
					},
				},
				{
					ObjectID:       "volume-service-app-etc-cfg",
					ComposeProject: "demo",
					Type:           dockermodel.VolumeTypeBind,
					Backend:        dockermodel.SnapshotBackendCopy,
					Bind:           &dockermodel.VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "/host/cfg", ReadOnly: true},
				},
				{
					ObjectID:       "volume-service-db-data",
					ComposeProject: "demo",
					Type:           dockermodel.VolumeTypeAnon,
					Backend:        dockermodel.SnapshotBackendCopy,
					Anon:           &dockermodel.VolumePropertiesAnon{Service: "db", Target: "/data", Subpath: "inner"},
				},
			},
			wantSkipped: []dockermodel.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
			},
		},
		{
			name: "every mount of a shared volume travels with the one unit",
			project: stackmodel.ComposeProject{
				Name: "demo",
				Services: []stackmodel.Service{
					{Name: "api", Volumes: []stackmodel.ServiceVolume{
						{Type: dockermodel.VolumeTypeNamed, Source: "assets", Target: "/srv/assets", VolumeName: "demo_assets"},
					}},
					{Name: "web", Volumes: []stackmodel.ServiceVolume{
						{Type: dockermodel.VolumeTypeNamed, Source: "assets", Target: "/usr/share/nginx/html", VolumeName: "demo_assets"},
					}},
				},
				Volumes: []stackmodel.NamedVolume{{
					Key:  "assets",
					Name: "demo_assets",
					UsedBy: []dockermodel.ServiceReference{
						{Service: "api", Target: "/srv/assets"},
						{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true, Subpath: "public"},
					},
				}},
			},
			want: []dockermodel.Volume{{
				ObjectID:       "volume-named-demo_assets",
				ComposeProject: "demo",
				Type:           dockermodel.VolumeTypeNamed,
				Backend:        dockermodel.SnapshotBackendCopy,
				Named: &dockermodel.VolumePropertiesNamed{
					VolumeName: "demo_assets",
					UsedBy: []dockermodel.ServiceReference{
						{Service: "api", Target: "/srv/assets"},
						{Service: "web", Target: "/usr/share/nginx/html", ReadOnly: true, Subpath: "public"},
					},
				},
			}},
		},
		{
			name: "external and unmounted named volumes are captured too",
			project: stackmodel.ComposeProject{
				Name: "demo",
				Volumes: []stackmodel.NamedVolume{
					{Key: "orphan", Name: "demo_orphan"},
					{Key: "shared", Name: "shared_pgdata", External: true},
				},
			},
			want: []dockermodel.Volume{
				{ObjectID: "volume-named-demo_orphan", ComposeProject: "demo", Type: dockermodel.VolumeTypeNamed, Backend: dockermodel.SnapshotBackendCopy, Named: &dockermodel.VolumePropertiesNamed{VolumeName: "demo_orphan"}},
				{ObjectID: "volume-named-shared_pgdata", ComposeProject: "demo", Type: dockermodel.VolumeTypeNamed, Backend: dockermodel.SnapshotBackendCopy, Named: &dockermodel.VolumePropertiesNamed{VolumeName: "shared_pgdata"}},
			},
		},
		{
			// One case per skipped type, asserting the whole SkippedVolume.
			name: "every type that is not named, bind or anon is skipped with its reason",
			project: stackmodel.ComposeProject{
				Name: "demo",
				Services: []stackmodel.Service{{Name: "app", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
					{Type: dockermodel.VolumeTypeNPipe, RawType: "npipe", Target: `\\.\pipe\docker_engine`},
					{Type: dockermodel.VolumeTypeImage, RawType: "image", Source: "assets:1", Target: "/srv/assets"},
					{Type: dockermodel.VolumeTypeCluster, RawType: "cluster", Source: "csi-data", Target: "/data"},
					{Type: dockermodel.VolumeTypeUnknown, RawType: "something-new", Target: "/new"},
				}}},
			},
			want: nil,
			wantSkipped: []dockermodel.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
				{Service: "app", Target: `\\.\pipe\docker_engine`, RawType: "npipe", Reason: "a named pipe is a channel, not data"},
				{Service: "app", Target: "/srv/assets", RawType: "image", Reason: "read-only image content, reproducible from the image"},
				{Service: "app", Target: "/data", RawType: "cluster", Reason: "CSI volume: data lives outside the docker arcaved"},
				{Service: "app", Target: "/new", RawType: "something-new", Reason: "unknown mount type"},
			},
		},
		{
			name: "a skipped mount beside a captured one costs the unit nothing",
			project: stackmodel.ComposeProject{
				Name: "demo",
				Services: []stackmodel.Service{{Name: "app", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
					{Type: dockermodel.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg"},
				}}},
			},
			want: []dockermodel.Volume{{
				ObjectID:       "volume-service-app-etc-cfg",
				ComposeProject: "demo",
				Type:           dockermodel.VolumeTypeBind,
				Backend:        dockermodel.SnapshotBackendCopy,
				Bind:           &dockermodel.VolumePropertiesBind{Service: "app", Target: "/etc/cfg", Source: "/host/cfg"},
			}},
			wantSkipped: []dockermodel.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
			},
		},
		{
			// Units dedup by identity; skips do not.
			name: "two tmpfs mounts are two skips",
			project: stackmodel.ComposeProject{
				Name: "demo",
				Services: []stackmodel.Service{{Name: "app", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
					{Type: dockermodel.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/run"},
				}}},
			},
			want: nil,
			wantSkipped: []dockermodel.SkippedVolume{
				{Service: "app", Target: "/scratch", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
				{Service: "app", Target: "/run", RawType: "tmpfs", Reason: "non-persistent (tmpfs lives in RAM)"},
			},
		},
		{
			name: "two services binding the same host path stay two units",
			project: stackmodel.ComposeProject{
				Name: "demo",
				Services: []stackmodel.Service{
					{Name: "api", Volumes: []stackmodel.ServiceVolume{
						{Type: dockermodel.VolumeTypeBind, Source: "/host/cfg", Target: "/etc/cfg"},
					}},
					{Name: "web", Volumes: []stackmodel.ServiceVolume{
						{Type: dockermodel.VolumeTypeBind, Source: "/host/cfg", Target: "/etc/cfg"},
					}},
				},
			},
			want: []dockermodel.Volume{
				{
					ObjectID:       "volume-service-api-etc-cfg",
					ComposeProject: "demo",
					Type:           dockermodel.VolumeTypeBind,
					Backend:        dockermodel.SnapshotBackendCopy,
					Bind:           &dockermodel.VolumePropertiesBind{Service: "api", Target: "/etc/cfg", Source: "/host/cfg"},
				},
				{
					ObjectID:       "volume-service-web-etc-cfg",
					ComposeProject: "demo",
					Type:           dockermodel.VolumeTypeBind,
					Backend:        dockermodel.SnapshotBackendCopy,
					Bind:           &dockermodel.VolumePropertiesBind{Service: "web", Target: "/etc/cfg", Source: "/host/cfg"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, skipped, err := FindBackupVolumes(stackmodel.IntrospectionResult{Project: tt.project}, dockermodel.SnapshotBackendCopy)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantSkipped, skipped)
			assertOneArmPerType(t, got)
		})
	}
}

// An ObjectID is one segment, so a target is flattened into it and two
// identities can land on the same id. Sharing a unit of backup between them
// would have each overwrite the other, so the operation stops and names both.
func TestFindBackupVolumes_ObjectIDCollision(t *testing.T) {
	// Arrange
	res := stackmodel.IntrospectionResult{Project: stackmodel.ComposeProject{
		Name: "demo",
		Services: []stackmodel.Service{
			{Name: "app", Volumes: []stackmodel.ServiceVolume{
				{Type: dockermodel.VolumeTypeBind, RawType: "bind", Source: "/host/data", Target: "/var/lib/data"},
			}},
			{Name: "app-var", Volumes: []stackmodel.ServiceVolume{
				{Type: dockermodel.VolumeTypeBind, RawType: "bind", Source: "/host/other", Target: "/lib/data"},
			}},
		},
	}}

	// Act
	got, skipped, err := FindBackupVolumes(res, dockermodel.SnapshotBackendCopy)

	// Assert
	require.ErrorIs(t, err, errObjectIDCollision)
	assert.Contains(t, err.Error(), `service "app" at "/var/lib/data"`)
	assert.Contains(t, err.Error(), `service "app-var" at "/lib/data"`)
	assert.Contains(t, err.Error(), "volume-service-app-var-lib-data")
	assert.Nil(t, got)
	assert.Nil(t, skipped)
}

// The backend is the caller's, stamped on every unit whatever its addressing
// arm: it is project config, not a constant of this package.
func TestFindBackupVolumes_StampsTheGivenBackend(t *testing.T) {
	// Arrange
	res := stackmodel.IntrospectionResult{Project: stackmodel.ComposeProject{
		Name: "demo",
		Services: []stackmodel.Service{{
			Name:    "app",
			Volumes: []stackmodel.ServiceVolume{{Type: dockermodel.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg"}},
		}},
		Volumes: []stackmodel.NamedVolume{{Key: "db", Name: "demo_db",
			UsedBy: []dockermodel.ServiceReference{{Service: "db", Target: "/data"}}}},
	}}

	// Act
	got, _, err := FindBackupVolumes(res, dockermodel.SnapshotBackendCDC)

	// Assert
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, v := range got {
		assert.Equal(t, dockermodel.SnapshotBackendCDC, v.Backend, "volume %s", v.ObjectID)
	}
}

// assertOneArmPerType pins the invariant no type expresses: exactly one
// addressing arm is non-nil, and it is the one Type names.
func assertOneArmPerType(t *testing.T, volumes []dockermodel.Volume) {
	t.Helper()

	for _, v := range volumes {
		set := map[dockermodel.VolumeType]bool{
			dockermodel.VolumeTypeNamed: v.Named != nil,
			dockermodel.VolumeTypeBind:  v.Bind != nil,
			dockermodel.VolumeTypeAnon:  v.Anon != nil,
		}
		for volumeType, isSet := range set {
			assert.Equal(t, volumeType == v.Type, isSet, "volume %s of type %s: arm %s", v.ObjectID, v.Type, volumeType)
		}
	}
}
