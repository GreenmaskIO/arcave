package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestSessionListHandler_List(t *testing.T) {
	res := apimodel.SessionListResponse{
		ProjectName: "demo",
		Sessions:    []apimodel.SessionListItem{{SessionID: testSessionID}},
	}

	tests := []struct {
		name        string
		cfg         config.Config
		addressing  Addressing
		wantReq     *apimodel.SessionListRequest
		apiErr      error
		printerErr  error
		wantErr     string
		wantPrinted []any
	}{
		{
			name:        "the config answers the project",
			cfg:         testConfig,
			wantReq:     &apimodel.SessionListRequest{ProjectName: "demo"},
			wantPrinted: []any{res},
		},
		{
			name:        "--project beats the config",
			cfg:         testConfig,
			addressing:  Addressing{Project: "other"},
			wantReq:     &apimodel.SessionListRequest{ProjectName: "other"},
			wantPrinted: []any{res},
		},
		{
			name:    "no project anywhere reaches nothing",
			cfg:     config.Config{},
			wantErr: "no project",
		},
		{
			name:    "api error passes through",
			cfg:     testConfig,
			wantReq: &apimodel.SessionListRequest{ProjectName: "demo"},
			apiErr:  errAPI,
			wantErr: errAPI.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("SessionList", mock.Anything, *tt.wantReq).Return(res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewSessionListHandler(api, p)

			// Act
			err := h.List(context.Background(), tt.cfg, tt.addressing)

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
