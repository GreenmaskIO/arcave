package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestRecommendHandler_Recommend(t *testing.T) {
	advice := apimodel.RecommendResponse{
		ProviderID:      "my-linux-box",
		Recommendations: []model.Recommendation{{Service: "db", Volume: "/var/lib/postgresql/data"}},
	}
	empty := apimodel.RecommendResponse{ProviderID: "my-linux-box"}
	base := apimodel.RecommendRequest{ProjectName: "demo", ProviderID: "my-linux-box", StackID: "web"}

	tests := []struct {
		name        string
		addressing  Addressing
		res         apimodel.RecommendResponse
		wantReq     *apimodel.RecommendRequest
		apiErr      error
		printerErr  error
		wantFound   bool
		wantErr     string
		wantPrinted []any
	}{
		{
			name:        "advice is printed and found",
			addressing:  Addressing{Stack: "web"},
			res:         advice,
			wantReq:     &base,
			wantFound:   true,
			wantPrinted: []any{advice},
		},
		{
			name:        "nothing to recommend is not a finding",
			addressing:  Addressing{Stack: "web"},
			res:         empty,
			wantReq:     &base,
			wantPrinted: []any{empty},
		},
		{
			name:       "an unnamed stack reaches nothing",
			addressing: Addressing{},
			wantErr:    "no stack",
		},
		{
			name:       "api error passes through",
			addressing: Addressing{Stack: "web"},
			res:        advice,
			wantReq:    &base,
			apiErr:     errAPI,
			wantErr:    errAPI.Error(),
		},
		{
			name:        "printer error is not a finding",
			addressing:  Addressing{Stack: "web"},
			res:         advice,
			wantReq:     &base,
			printerErr:  errPrint,
			wantErr:     errPrint.Error(),
			wantPrinted: []any{advice},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("Recommend", mock.Anything, *tt.wantReq).Return(tt.res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", tt.res).Return(tt.printerErr).Maybe()
			h := NewRecommendHandler(api, p)

			// Act
			found, err := h.Recommend(context.Background(), testConfig, tt.addressing)

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantPrinted, p.printed())
			api.AssertExpectations(t)
		})
	}
}
