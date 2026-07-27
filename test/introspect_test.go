// Package test holds arcave's cross-package integration tests. It exists so a
// test may wire two internal packages together — here the real compose loader
// into the introspector — without either package importing the other.
package test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/corev2"
	introspect "github.com/greenmaskio/arcave/internal/service/introspectv2"
)

// The unit tests drive the introspector off compose-go directly; this one goes
// through arcave's own loader, so the file resolution, project naming and volume
// name resolution a user actually gets are what is asserted.
func TestInspector_Introspect_RealLoader(t *testing.T) {
	// Arrange
	// The loader honours COMPOSE_FILE, and a value inherited from the developer's
	// shell would silently add files to the project.
	t.Setenv("COMPOSE_FILE", "")
	inspector := introspect.NewInspector(compose.NewLoader())
	bindCfg, err := filepath.Abs(filepath.Join("testdata", "cfg"))
	require.NoError(t, err)

	// Act
	got, err := inspector.Introspect(context.Background(), filepath.Join("testdata", "stack.yml"))

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.Project{
		Name: "introspect-integration",
		Services: []model.Service{
			{Name: "api", Image: "alpine:3", Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets", Target: "/srv/assets", VolumeName: "shared_assets", Volume: &model.VolumeMount{}},
			}},
			{Name: "db", Image: "postgres:16", Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data", VolumeName: "introspect-integration_pgdata", Volume: &model.VolumeMount{}},
				{Type: model.VolumeTypeBind, RawType: "bind", Source: bindCfg, Target: "/etc/postgresql/conf.d", Bind: &model.BindMount{CreateHostPath: true}},
				{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/var/lib/postgresql/scratch", Volume: &model.VolumeMount{}},
				{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/run/postgresql"},
			}},
			{Name: "web", Image: "nginx:1", Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets", Target: "/usr/share/nginx/html", VolumeName: "shared_assets", Volume: &model.VolumeMount{}},
			}},
		},
		Volumes: []model.NamedVolume{
			{Key: "assets", Name: "shared_assets", UsedBy: []model.ServiceReference{
				{Service: "api", Target: "/srv/assets"},
				{Service: "web", Target: "/usr/share/nginx/html"},
			}},
			{Key: "orphan", Name: "introspect-integration_orphan"},
			{Key: "pgdata", Name: "introspect-integration_pgdata", UsedBy: []model.ServiceReference{
				{Service: "db", Target: "/var/lib/postgresql/data"},
			}},
		},
	}, got.Project)
}

// The introspector guards against a mount whose volume is undeclared; through
// the real loader that project never gets that far. Both layers are load-bearing
// — this pins which one speaks first, so the guard is not mistaken for the only
// defence.
func TestInspector_Introspect_RealLoader_UndeclaredVolume(t *testing.T) {
	// Arrange
	t.Setenv("COMPOSE_FILE", "")
	inspector := introspect.NewInspector(compose.NewLoader())

	// Act
	_, err := inspector.Introspect(context.Background(), filepath.Join("testdata", "undeclared.yml"))

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading docker compose file(s)")
	assert.Contains(t, err.Error(), `service "db" refers to undefined volume nowhere`)
}

// The backup units the service layer would snapshot: one per named volume and
// one per bind/anon mount, deduplicated by identity — the shared volume appears
// once for two services. The stack's tmpfs is the one skip.
func TestFindBackupVolumes_RealLoader(t *testing.T) {
	// Arrange
	t.Setenv("COMPOSE_FILE", "")
	got, err := introspect.NewInspector(compose.NewLoader()).
		Introspect(context.Background(), filepath.Join("testdata", "stack.yml"))
	require.NoError(t, err)
	bindCfg, err := filepath.Abs(filepath.Join("testdata", "cfg"))
	require.NoError(t, err)

	// Act
	volumes, skipped, err := corev2.FindBackupVolumes(got)

	// Assert
	require.NoError(t, err)
	ids := make([]string, 0, len(volumes))
	for _, v := range volumes {
		ids = append(ids, v.ObjectID)
	}
	assert.Equal(t, []string{
		"volume/shared_assets",
		"volume/introspect-integration_orphan",
		"volume/introspect-integration_pgdata",
		"service/db/etc/postgresql/conf.d",
		"service/db/var/lib/postgresql/scratch",
	}, ids)

	assert.Equal(t, []model.SkippedVolume{{
		Service: "db",
		Target:  "/run/postgresql",
		RawType: "tmpfs",
		Reason:  "non-persistent (tmpfs lives in RAM)",
	}}, skipped)

	// Each unit is addressed through the arm its type names.
	byID := make(map[string]model.Volume, len(volumes))
	for _, v := range volumes {
		byID[v.ObjectID] = v
	}
	require.NotNil(t, byID["volume/shared_assets"].Named)
	assert.Equal(t, "shared_assets", byID["volume/shared_assets"].Named.VolumeName)
	assert.Equal(t, []model.ServiceReference{
		{Service: "api", Target: "/srv/assets"},
		{Service: "web", Target: "/usr/share/nginx/html"},
	}, byID["volume/shared_assets"].Named.UsedBy)

	require.NotNil(t, byID["service/db/etc/postgresql/conf.d"].Bind)
	assert.Equal(t, bindCfg, byID["service/db/etc/postgresql/conf.d"].Bind.Source)

	require.NotNil(t, byID["service/db/var/lib/postgresql/scratch"].Anon)
	assert.Equal(t, "db", byID["service/db/var/lib/postgresql/scratch"].Anon.Service)
}
