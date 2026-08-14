package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestSavepointListHandler_List(t *testing.T) {
	res := apimodel.SavepointListResponse{
		ProviderID: "my-linux-box",
		Records:    []apimodel.SavepointRecord{{SavepointID: testSavepointID}},
	}
	base := apimodel.SavepointListRequest{
		ProjectName:             "demo",
		ProviderID:              "my-linux-box",
		StackID:                 "web",
		Limit:                   10,
		Offset:                  20,
		IncludeSafetySavepoints: true,
	}
	params := SavepointListParams{Limit: 10, Offset: 20, IncludeSafetySavepoints: true}

	tests := []struct {
		name        string
		addressing  Addressing
		env         string
		params      SavepointListParams
		wantReq     *apimodel.SavepointListRequest
		apiErr      error
		printerErr  error
		wantErr     string
		wantPrinted []any
	}{
		{
			name:        "the paging travels and the session does not",
			addressing:  Addressing{Stack: "web"},
			env:         testSessionID.String(),
			params:      params,
			wantReq:     &base,
			wantPrinted: []any{res},
		},
		{
			name:        "no session anywhere is still a listing",
			addressing:  Addressing{Stack: "web"},
			params:      params,
			wantReq:     &base,
			wantPrinted: []any{res},
		},
		{
			name:       "an unnamed stack reaches nothing",
			addressing: Addressing{},
			params:     params,
			wantErr:    "no stack",
		},
		{
			name:       "api error passes through",
			addressing: Addressing{Stack: "web"},
			params:     params,
			wantReq:    &base,
			apiErr:     errAPI,
			wantErr:    errAPI.Error(),
		},
		{
			name:        "printer error surfaces",
			addressing:  Addressing{Stack: "web"},
			params:      params,
			wantReq:     &base,
			printerErr:  errPrint,
			wantErr:     errPrint.Error(),
			wantPrinted: []any{res},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv(EnvSessionID, tt.env)
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("SavepointList", mock.Anything, *tt.wantReq).Return(res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewSavepointListHandler(api, p)

			// Act
			err := h.List(context.Background(), testConfig, tt.addressing, tt.params)

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantPrinted, p.printed())
			api.AssertExpectations(t)
		})
	}
}
