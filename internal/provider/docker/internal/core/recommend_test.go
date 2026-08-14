package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
	stackmodel "github.com/greenmaskio/arcave/internal/provider/docker/stack/model"
)

func TestRecommendService_Recommend(t *testing.T) {
	entries := []stackmodel.ARCEntry{
		{
			Image:   "postgres",
			Aliases: []string{"pgvector/pgvector"},
			Volumes: []string{"/var/lib/postgresql/data"},
		},
		{
			Image:   "mongo",
			Volumes: []string{"/data/db", "/data/configdb"},
		},
	}

	pgdata := "/var/lib/postgresql/data"

	tests := []struct {
		name     string
		services []stackmodel.Service
		want     []model.Recommendation
	}{
		{
			name: "an uncovered volume is recommended",
			services: []stackmodel.Service{
				{Name: "db", Image: "postgres:16"},
			},
			want: []model.Recommendation{
				newRecommendation("db", "postgres:16", pgdata),
			},
		},
		{
			name: "a named volume covers the path",
			services: []stackmodel.Service{
				{Name: "db", Image: "postgres:16", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeNamed, Source: "pgdata", Target: pgdata},
				}},
			},
			want: []model.Recommendation{},
		},
		{
			name: "a bind mount covers the path just as well",
			services: []stackmodel.Service{
				{Name: "db", Image: "postgres:16", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeBind, Source: "./pg", Target: "/etc/pg"},
					{Type: dockermodel.VolumeTypeBind, Source: "./data", Target: pgdata},
				}},
			},
			want: []model.Recommendation{},
		},
		// The compose mount type alone cannot decide these three: an anonymous
		// volume is a "volume" and persists, while tmpfs and an image mount sit
		// at a path without keeping anything there.
		{
			name: "an anonymous volume covers the path",
			services: []stackmodel.Service{
				{Name: "db", Image: "postgres:16", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeAnon, Target: pgdata},
				}},
			},
			want: []model.Recommendation{},
		},
		{
			name: "a tmpfs mount does not cover the path",
			services: []stackmodel.Service{
				{Name: "db", Image: "postgres:16", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeTmpfs, Target: pgdata},
				}},
			},
			want: []model.Recommendation{
				newRecommendation("db", "postgres:16", pgdata),
			},
		},
		{
			name: "an image mount does not cover the path",
			services: []stackmodel.Service{
				{Name: "db", Image: "postgres:16", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeImage, Source: "seed:1", Target: pgdata},
				}},
			},
			want: []model.Recommendation{
				newRecommendation("db", "postgres:16", pgdata),
			},
		},
		{
			name: "an alias matches the entry it belongs to",
			services: []stackmodel.Service{
				{Name: "vec", Image: "pgvector/pgvector:pg16"},
			},
			want: []model.Recommendation{
				newRecommendation("vec", "pgvector/pgvector:pg16", pgdata),
			},
		},
		{
			name: "a registry-qualified image matches the official entry",
			services: []stackmodel.Service{
				{Name: "db", Image: "registry.corp.local/postgres:16"},
			},
			want: []model.Recommendation{
				newRecommendation("db", "registry.corp.local/postgres:16", pgdata),
			},
		},
		{
			name: "partial coverage recommends only what is missing",
			services: []stackmodel.Service{
				{Name: "mongo", Image: "mongo:7", Volumes: []stackmodel.ServiceVolume{
					{Type: dockermodel.VolumeTypeNamed, Source: "mongodata", Target: "/data/db"},
				}},
			},
			want: []model.Recommendation{
				newRecommendation("mongo", "mongo:7", "/data/configdb"),
			},
		},
		{
			name: "an unknown image is not advised on",
			services: []stackmodel.Service{
				{Name: "app", Image: "ghcr.io/org/app:1"},
			},
			want: []model.Recommendation{},
		},
		{
			name: "a build-only service is skipped",
			services: []stackmodel.Service{
				{Name: "builder"},
			},
			want: []model.Recommendation{},
		},
		{
			name: "an unparsable image is skipped, not an error",
			services: []stackmodel.Service{
				{Name: "db", Image: "NOT A REFERENCE"},
			},
			want: []model.Recommendation{},
		},
		{
			name:     "no services at all",
			services: nil,
			want:     []model.Recommendation{},
		},
		{
			name: "volumes keep catalog order across services",
			services: []stackmodel.Service{
				{Name: "db", Image: "postgres:16"},
				{Name: "mongo", Image: "mongo:7"},
			},
			want: []model.Recommendation{
				newRecommendation("db", "postgres:16", pgdata),
				newRecommendation("mongo", "mongo:7", "/data/db"),
				newRecommendation("mongo", "mongo:7", "/data/configdb"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			catalog := &mockCatalog{}
			catalog.On("List").Return(entries, nil).Once()
			inspector := &mockInspector{}
			inspector.On("Introspect", mock.Anything, []string{"compose.yaml"}).
				Return(stackmodel.IntrospectionResult{Project: stackmodel.ComposeProject{Services: tt.services}}, nil).Once()

			// Act
			got, err := NewRecommendService(catalog, inspector).Recommend(context.Background(), "compose.yaml")

			// Assert
			require.NoError(t, err)
			assert.NotNil(t, got, "an empty result must marshal as [], never null")
			assert.Equal(t, tt.want, got)
			catalog.AssertExpectations(t)
			inspector.AssertExpectations(t)
		})
	}
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
		// The domain is dropped for every registry, so these four coexist in one
		// compose file and match the same catalog. Only a mirror that prefixes the
		// path yields a different key — a catalog alias's job, not this function's.
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

// A user catalog is merged after the built-in one, so the last entry claiming a
// key owns it — that is how a user reassigns an alias arcave ships with.
func TestCatalogIndex(t *testing.T) {
	// Arrange
	entries := []stackmodel.ARCEntry{
		{Image: "postgres", Aliases: []string{"timescale/timescaledb"}, Volumes: []string{"/var/lib/postgresql/data"}},
		{Image: "timescale/timescaledb", Volumes: []string{"/home/postgres/pgdata"}},
	}

	// Act
	index := catalogIndex(entries)

	// Assert
	assert.Equal(t, []string{"/var/lib/postgresql/data"}, index["postgres"].Volumes)
	assert.Equal(t, []string{"/home/postgres/pgdata"}, index["timescale/timescaledb"].Volumes)
}

// Neither collaborator's failure is recoverable here: without a catalog or
// without the project there is nothing to advise on, so both surface.
func TestRecommendService_Recommend_Errors(t *testing.T) {
	catalogErr := errors.New("catalog unreadable")
	inspectErr := errors.New("bad compose file")

	tests := []struct {
		name       string
		catalogErr error
		inspectErr error
		wantErr    error
		wantMsg    string
	}{
		{
			name:       "catalog failure surfaces",
			catalogErr: catalogErr,
			wantErr:    catalogErr,
			wantMsg:    "reading recommender catalog",
		},
		{
			name:       "inspector failure surfaces",
			inspectErr: inspectErr,
			wantErr:    inspectErr,
			wantMsg:    "inspecting compose services",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			catalog := &mockCatalog{}
			catalog.On("List").Return([]stackmodel.ARCEntry{{Image: "postgres", Volumes: []string{"/data"}}}, tt.catalogErr).Once()
			inspector := &mockInspector{}
			// A call naming no files passes the nil slice through.
			inspector.On("Introspect", mock.Anything, []string(nil)).
				Return(stackmodel.IntrospectionResult{}, tt.inspectErr).Maybe()

			// Act
			got, err := NewRecommendService(catalog, inspector).Recommend(context.Background())

			// Assert
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), tt.wantMsg)
			assert.Nil(t, got)
			catalog.AssertExpectations(t)
			inspector.AssertExpectations(t)
		})
	}
}

// The hash is a published contract: a caller stores one to suppress advice it has
// already acted on, so the canonical form must not drift. The literals below pin
// it — change them and every hash a user holds stops matching.
func TestNewRecommendation(t *testing.T) {
	tests := []struct {
		name    string
		service string
		image   string
		volume  string
		want    model.Recommendation
	}{
		{
			name:    "canonical form is sha256 of service|image|volume|operation",
			service: "db",
			image:   "postgres:16",
			volume:  "/var/lib/postgresql/data",
			want: model.Recommendation{
				Service:   "db",
				Image:     "postgres:16",
				Volume:    "/var/lib/postgresql/data",
				Operation: model.RecommendationOperationAddVolume,
				Hash:      "68cf83ec338db99fb410469421d5623bc116ea567d598e65e0e6d76603566892",
			},
		},
		{
			name:    "a different tag hashes differently",
			service: "db",
			image:   "postgres:17",
			volume:  "/var/lib/postgresql/data",
			want: model.Recommendation{
				Service:   "db",
				Image:     "postgres:17",
				Volume:    "/var/lib/postgresql/data",
				Operation: model.RecommendationOperationAddVolume,
				Hash:      "f7d55a55cdd152f429c2701d1f239b50a132673c14806ea25d74fc67576e771b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newRecommendation(tt.service, tt.image, tt.volume)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// The same inputs always hash the same, and each field participates: changing any
// one of them must move the hash, or two distinct findings would collide.
func TestNewRecommendation_Distinct(t *testing.T) {
	// Arrange
	base := newRecommendation("db", "postgres:16", "/var/lib/postgresql/data")

	// Act
	again := newRecommendation("db", "postgres:16", "/var/lib/postgresql/data")
	otherService := newRecommendation("db2", "postgres:16", "/var/lib/postgresql/data")
	otherImage := newRecommendation("db", "postgres:15", "/var/lib/postgresql/data")
	otherVolume := newRecommendation("db", "postgres:16", "/var/lib/postgresql")

	// Assert
	assert.Equal(t, base.Hash, again.Hash)
	assert.NotEqual(t, base.Hash, otherService.Hash)
	assert.NotEqual(t, base.Hash, otherImage.Hash)
	assert.NotEqual(t, base.Hash, otherVolume.Hash)
}

// mockCatalog doubles recommendationCatalog; the seam takes no ctx — the
// catalog is local bytes.
type mockCatalog struct{ mock.Mock }

func (m *mockCatalog) List() ([]stackmodel.ARCEntry, error) {
	args := m.Called()
	entries, _ := args.Get(0).([]stackmodel.ARCEntry)
	return entries, args.Error(1)
}
