package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The tool resolves the compose files it checks (explicit input, else the
// injected project config), passes the catalogs it was given to the factory, and
// reports back the files it actually used — which is what an agent needs to ask
// the user where to add a volume.
func TestMCPHandler_recommendMCP(t *testing.T) {
	recs := []model.Recommendation{{
		Service:   "db",
		Image:     "postgres:16",
		Volume:    "/var/lib/postgresql/data",
		Operation: "AddVolume",
		Hash:      "51d8076c46776093139962baadd5cb8ada04514f0d5d100ed44b6276b44a8184",
	}}
	svcErr := errors.New("bad compose file")

	// configuredFiles is what the composition root injects: the project's
	// project.compose_files, which every call falls back to.
	configuredFiles := []string{"docker-compose.yml", "docker-compose.override.yml"}

	tests := []struct {
		name                 string
		in                   recommendInput
		recommendations      []model.Recommendation // what the service returns
		recommendErr         error                  // the service's error, if any
		wantComposeFiles     []string               // files the service is called with, and echoed in the output
		wantRecommenderFiles []string               // catalogs the factory is built with
		wantOutput           recommendOutput
		wantErr              error
	}{
		{
			name:             "explicit compose files reach the service verbatim",
			in:               recommendInput{ComposeFiles: []string{"other.yml"}},
			recommendations:  recs,
			wantComposeFiles: []string{"other.yml"},
			wantOutput:       recommendOutput{ComposeFiles: []string{"other.yml"}, Recommendations: recs},
		},
		{
			name:             "omitted compose files fall back to the project's",
			recommendations:  recs,
			wantComposeFiles: configuredFiles,
			wantOutput:       recommendOutput{ComposeFiles: configuredFiles, Recommendations: recs},
		},
		{
			name:                 "recommender files reach the factory",
			in:                   recommendInput{RecommenderFiles: []string{"team.yaml", "mine.yaml"}},
			recommendations:      recs,
			wantComposeFiles:     configuredFiles,
			wantRecommenderFiles: []string{"team.yaml", "mine.yaml"},
			wantOutput:           recommendOutput{ComposeFiles: configuredFiles, Recommendations: recs},
		},
		{
			// A nil result must marshal as [] rather than null, so an agent never
			// has to tell "nothing to advise" from "no such field".
			name:             "a nil result is reported as an empty array",
			recommendations:  nil,
			wantComposeFiles: configuredFiles,
			wantOutput:       recommendOutput{ComposeFiles: configuredFiles, Recommendations: []model.Recommendation{}},
		},
		{
			name:             "service error surfaces",
			recommendErr:     svcErr,
			wantComposeFiles: configuredFiles,
			wantErr:          svcErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := &mockService{}
			svc.On("Recommend", mock.Anything, tt.wantComposeFiles).
				Return(tt.recommendations, tt.recommendErr).Once()
			var gotRecommenderFiles []string
			factory := func(recommenderFiles []string) RecommendService {
				gotRecommenderFiles = recommenderFiles
				return svc
			}
			h := NewMCPHandler(&mockService{}, factory, configuredFiles)

			// Act
			res, out, err := h.recommendMCP(context.Background(), nil, tt.in)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, recommendOutput{}, out)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantOutput, out)
			}
			// The SDK builds the unstructured half of the result from the typed
			// output, so the handler always leaves it nil.
			assert.Nil(t, res)
			assert.Equal(t, tt.wantRecommenderFiles, gotRecommenderFiles)
			svc.AssertExpectations(t)
		})
	}
}
