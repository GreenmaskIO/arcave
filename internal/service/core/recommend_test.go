package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestRecommendService_Recommend(t *testing.T) {
	entries := []model.ARCEntry{
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

	tests := []struct {
		name     string
		services []model.ComposeService
		want     []model.Recommendation
	}{
		{
			name: "an uncovered volume is recommended",
			services: []model.ComposeService{
				{Name: "db", Image: "postgres:16", ImageRepo: "postgres"},
			},
			want: []model.Recommendation{
				newRecommendation("db", "postgres:16", "/var/lib/postgresql/data"),
			},
		},
		{
			name: "a named volume covers the path",
			services: []model.ComposeService{
				{Name: "db", Image: "postgres:16", ImageRepo: "postgres",
					MountTargets: []string{"/var/lib/postgresql/data"}},
			},
			want: []model.Recommendation{},
		},
		{
			name: "a bind mount covers the path just as well",
			services: []model.ComposeService{
				{Name: "db", Image: "postgres:16", ImageRepo: "postgres",
					MountTargets: []string{"/etc/pg", "/var/lib/postgresql/data"}},
			},
			want: []model.Recommendation{},
		},
		{
			name: "an alias matches the entry it belongs to",
			services: []model.ComposeService{
				{Name: "vec", Image: "pgvector/pgvector:pg16", ImageRepo: "pgvector/pgvector"},
			},
			want: []model.Recommendation{
				newRecommendation("vec", "pgvector/pgvector:pg16", "/var/lib/postgresql/data"),
			},
		},
		{
			name: "partial coverage recommends only what is missing",
			services: []model.ComposeService{
				{Name: "mongo", Image: "mongo:7", ImageRepo: "mongo",
					MountTargets: []string{"/data/db"}},
			},
			want: []model.Recommendation{
				newRecommendation("mongo", "mongo:7", "/data/configdb"),
			},
		},
		{
			name: "an unknown image is not advised on",
			services: []model.ComposeService{
				{Name: "app", Image: "ghcr.io/org/app:1", ImageRepo: "org/app"},
			},
			want: []model.Recommendation{},
		},
		{
			name: "a build-only service is skipped",
			services: []model.ComposeService{
				{Name: "builder"},
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
			services: []model.ComposeService{
				{Name: "db", Image: "postgres:16", ImageRepo: "postgres"},
				{Name: "mongo", Image: "mongo:7", ImageRepo: "mongo"},
			},
			want: []model.Recommendation{
				newRecommendation("db", "postgres:16", "/var/lib/postgresql/data"),
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
			inspector := &mockServiceInspector{}
			inspector.On("Services", mock.Anything, []string{"compose.yaml"}).Return(tt.services, nil).Once()

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

// A user catalog is merged after the built-in one, so the last entry claiming a
// key owns it — that is how a user reassigns an alias arcave ships with.
func TestCatalogIndex(t *testing.T) {
	// Arrange
	entries := []model.ARCEntry{
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
			catalog.On("List").Return([]model.ARCEntry{{Image: "postgres", Volumes: []string{"/data"}}}, tt.catalogErr).Once()
			inspector := &mockServiceInspector{}
			inspector.On("Services", mock.Anything, mock.Anything).
				Return([]model.ComposeService{}, tt.inspectErr).Maybe()

			// Act
			got, err := NewRecommendService(catalog, inspector).Recommend(context.Background())

			// Assert
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), tt.wantMsg)
			assert.Nil(t, got)
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
