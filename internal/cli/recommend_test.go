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

// The handler prints whatever the service returns, verbatim, and reports whether
// there was anything — that flag is what the command turns into its exit status,
// so an empty result must report false even though it printed successfully.
func TestRecommendHandler_Recommend(t *testing.T) {
	// The handler forwards whatever the service produced, so the fixture is a
	// literal rather than a minted recommendation: how a hash is computed is the
	// service's business, and this test must not depend on it.
	recs := []model.Recommendation{{
		Service:   "db",
		Image:     "postgres:16",
		Volume:    "/var/lib/postgresql/data",
		Operation: "AddVolume",
		Hash:      "51d8076c46776093139962baadd5cb8ada04514f0d5d100ed44b6276b44a8184",
	}}
	svcErr := errors.New("bad compose file")
	printErr := errors.New("stdout closed")

	tests := []struct {
		name            string
		recommendations []model.Recommendation // what the service returns
		recommendErr    error                  // the service's error, if any
		printerErr      error                  // the printer's error, if any
		wantFound       bool
		wantErr         error
		wantPrinted     []any // every value handed to the printer, in order
	}{
		{
			name:            "recommendations are printed and reported",
			recommendations: recs,
			wantFound:       true,
			wantPrinted:     []any{recs},
		},
		{
			name:            "an empty result prints and reports nothing found",
			recommendations: []model.Recommendation{},
			wantFound:       false,
			wantPrinted:     []any{[]model.Recommendation{}},
		},
		{
			name:         "service error skips printing",
			recommendErr: svcErr,
			wantErr:      svcErr,
			wantPrinted:  nil,
		},
		{
			name:            "printer error surfaces and reports nothing found",
			recommendations: recs,
			printerErr:      printErr,
			wantErr:         printErr,
			wantPrinted:     []any{recs},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(tt.printerErr).Maybe()
			svc := &mockService{}
			svc.On("Recommend", mock.Anything, []string{"a.yml", "b.yml"}).
				Return(tt.recommendations, tt.recommendErr).Once()
			h := NewRecommendHandler(svc, p)

			// Act
			found, err := h.Recommend(context.Background(), "a.yml", "b.yml")

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
		})
	}
}
