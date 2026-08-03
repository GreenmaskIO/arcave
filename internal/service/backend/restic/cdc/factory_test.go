package cdc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestFactory_Kind(t *testing.T) {
	assert.Equal(t, model.SnapshotBackendCDC, NewFactory(Dependencies{}).Kind())
}

// The repository is the volume's, under the shared root, and it is resolved at
// construction — unlike the volume's address, which needs a ctx the constructor
// does not have.
func TestFactory_New(t *testing.T) {
	tests := []struct {
		name       string
		volume     model.Volume
		tier       model.BackendTier
		wantRepo   string
		wantPrefix string
		wantErr    string
	}{
		{
			name:       "a named volume resolves its repository and prefix",
			volume:     demoVolume(),
			tier:       model.BackendTierSavepoint,
			wantRepo:   repoNamespace + "/" + demoObjectID,
			wantPrefix: "savepoint-volume-demo_pgdata",
		},
		{
			name: "a bind mount is its own mount, and flattens to its own prefix",
			volume: model.Volume{
				ObjectID:       "service/app/etc/cfg",
				ComposeProject: demoProject,
				Type:           model.VolumeTypeBind,
				Bind:           &model.VolumePropertiesBind{Service: "app", Target: "/etc/cfg"},
			},
			tier:       model.BackendTierSafety,
			wantRepo:   repoNamespace + "/service/app/etc/cfg",
			wantPrefix: "safety-service-app-etc-cfg",
		},
		{
			name: "a named volume no service mounts is built like any other",
			volume: model.Volume{
				ObjectID: "volume/orphan",
				Type:     model.VolumeTypeNamed,
				Named:    &model.VolumePropertiesNamed{VolumeName: "orphan"},
			},
			tier:       model.BackendTierSavepoint,
			wantRepo:   repoNamespace + "/volume/orphan",
			wantPrefix: "savepoint-volume-orphan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			shared := newStore(t)
			factory := NewFactory(Dependencies{SharedStore: shared, Options: defaultOptions()})

			// Act
			backend, err := factory.New(model.BackendSpec{Volume: tt.volume, Tier: tt.tier})

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, backend)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.volume, backend.Volume())
			snapshotter, ok := backend.(*Snapshotter)
			require.True(t, ok)
			// Both halves — what a container reaches over the protocol and what
			// arcave addresses as objects — are built over the one store.
			assert.Equal(t, shared.GetCwd()+"/"+tt.wantRepo, snapshotter.admin.store.GetCwd())
			assert.NotNil(t, snapshotter.repository)
			assert.Equal(t, tt.wantPrefix, snapshotter.prefix)
		})
	}
}

// A prefix is one path segment with no "/": that is the registry's contract, and
// flattening is the caller's job, not the registry's.
func TestRepositoryPrefix(t *testing.T) {
	tests := []struct {
		name     string
		tier     model.BackendTier
		objectID string
		want     string
	}{
		{name: "a named volume", tier: model.BackendTierSavepoint, objectID: "volume/pgdata", want: "savepoint-volume-pgdata"},
		{
			name: "a nested mount path", tier: model.BackendTierSafety,
			objectID: "service/app/var/lib/data", want: "safety-service-app-var-lib-data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := repositoryPrefix(tt.tier, tt.objectID)

			// Assert
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "/", "the registry takes one path segment")
		})
	}
}
