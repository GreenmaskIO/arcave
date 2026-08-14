package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestSessionStatusHandler_Status(t *testing.T) {
	res := any(map[string]any{"status": "active"})
	wantReq := apimodel.SessionStatusRequest{ProjectName: "demo", SessionID: testSessionID}

	tests := []struct {
		name        string
		addressing  Addressing
		env         string
		wantReq     *apimodel.SessionStatusRequest
		apiErr      error
		printerErr  error
		wantErr     string
		wantPrinted []any
	}{
		{
			name:        "the environment answers the session",
			env:         testSessionID.String(),
			wantReq:     &wantReq,
			wantPrinted: []any{res},
		},
		{
			name:        "--session-id beats the environment",
			addressing:  Addressing{SessionID: testSessionID.String()},
			env:         "11111111-1111-1111-1111-111111111111",
			wantReq:     &wantReq,
			wantPrinted: []any{res},
		},
		{
			name:    "no session anywhere reaches nothing",
			wantErr: "arcave session create",
		},
		{
			name:    "api error passes through",
			env:     testSessionID.String(),
			wantReq: &wantReq,
			apiErr:  errAPI,
			wantErr: errAPI.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv(EnvSessionID, tt.env)
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("SessionStatus", mock.Anything, *tt.wantReq).Return(res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewSessionStatusHandler(api, p)

			// Act
			err := h.Status(context.Background(), testConfig, tt.addressing)

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
