package introspect

import (
	"context"
	"errors"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// Services is the recommend command's view of the same loaded project Inspect
// works from, so it runs against the same in-memory loader seam.
func TestInspector_Services(t *testing.T) {
	t.Run("projects images and covered targets", func(t *testing.T) {
		// Arrange
		project := &types.Project{
			Name: "fixture",
			Services: types.Services{
				"db": {Image: "postgres:16", Volumes: []types.ServiceVolumeConfig{
					{Type: "volume", Source: "pgdata", Target: "/var/lib/postgresql/data"},
					{Type: "tmpfs", Target: "/scratch"},
				}},
				"app": {Image: "ghcr.io/org/app:1.2", Volumes: []types.ServiceVolumeConfig{
					{Type: "bind", Source: "./cfg", Target: "/etc/cfg"},
				}},
				"builder": {},
			},
		}
		loader := &mockLoader{}
		loader.On("Load", mock.Anything, []string{"a.yml"}).Return(project, nil).Once()

		// Act
		got, err := NewInspector(loader).Services(context.Background(), "a.yml")

		// Assert
		require.NoError(t, err)
		loader.AssertExpectations(t)
		assert.Equal(t, []model.ComposeService{
			{Name: "app", Image: "ghcr.io/org/app:1.2", ImageRepo: "org/app", MountTargets: []string{"/etc/cfg"}},
			{Name: "builder"},
			{Name: "db", Image: "postgres:16", ImageRepo: "postgres", MountTargets: []string{"/var/lib/postgresql/data"}},
		}, got)
	})

	t.Run("an unparsable image is reported as empty, not as a failure", func(t *testing.T) {
		// Arrange
		project := &types.Project{
			Name:     "fixture",
			Services: types.Services{"db": {Image: "NOT A REFERENCE"}},
		}
		loader := &mockLoader{}
		loader.On("Load", mock.Anything, mock.Anything).Return(project, nil).Once()

		// Act
		got, err := NewInspector(loader).Services(context.Background())

		// Assert
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "NOT A REFERENCE", got[0].Image)
		assert.Empty(t, got[0].ImageRepo)
	})

	t.Run("wraps a loader failure", func(t *testing.T) {
		// Arrange
		loader := &mockLoader{}
		loader.On("Load", mock.Anything, mock.Anything).Return(nil, errors.New("bad compose file")).Once()

		// Act
		_, err := NewInspector(loader).Services(context.Background())

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading docker compose file(s)")
		assert.Contains(t, err.Error(), "bad compose file")
	})
}

func TestImageRepo(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		want    string
		wantErr bool
	}{
		{name: "official image loses its tag", image: "postgres:16", want: "postgres"},
		{name: "bare official image", image: "postgres", want: "postgres"},
		{name: "explicit library namespace is stripped", image: "docker.io/library/redis:7", want: "redis"},
		{name: "registry is stripped, namespace kept", image: "ghcr.io/org/pg:1", want: "org/pg"},
		{name: "elastic registry", image: "docker.elastic.co/elasticsearch/elasticsearch:8.13.0", want: "elasticsearch/elasticsearch"},
		{name: "digest is stripped", image: "redis@sha256:0000000000000000000000000000000000000000000000000000000000000000", want: "redis"},
		{name: "hub namespace is kept", image: "bitnami/postgresql:16", want: "bitnami/postgresql"},
		// arcave assumes no single registry: the domain is dropped for all of
		// them, so these four coexist in one compose file and match the same
		// catalog. Only a mirror that prefixes the path yields a different key,
		// which is a catalog alias's job, not this function's.
		{name: "corporate registry matches the official entry", image: "registry.corp.local/postgres:16", want: "postgres"},
		{name: "registry with a port", image: "localhost:5000/redis:7", want: "redis"},
		{name: "private ghcr image keeps its namespace", image: "ghcr.io/myorg/mydb:1", want: "myorg/mydb"},
		{name: "a mirror prefix stays part of the key", image: "mycorp.jfrog.io/docker-remote/library/postgres:16", want: "docker-remote/library/postgres"},
		{name: "no image at all", image: "", want: ""},
		{name: "unparsable reference", image: "Postgres:16", want: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := imageRepo(tt.image)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
