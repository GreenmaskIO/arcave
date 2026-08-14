package cli

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestSessionCreateHandler_Create(t *testing.T) {
	from := uuid.MustParse("22222222-3333-4444-5555-666666666666")
	res := apimodel.SessionCreateResponse{ProjectName: "demo", SessionID: testSessionID}

	tests := []struct {
		name          string
		addressing    Addressing
		fromSessionID string
		wantReq       *apimodel.SessionCreateRequest
		apiErr        error
		printerErr    error
		wantErr       string
		wantPrinted   []any
	}{
		{
			name:        "the config answers the project",
			wantReq:     &apimodel.SessionCreateRequest{ProjectName: "demo"},
			wantPrinted: []any{res},
		},
		{
			name:        "--project beats the config",
			addressing:  Addressing{Project: "other"},
			wantReq:     &apimodel.SessionCreateRequest{ProjectName: "other"},
			wantPrinted: []any{res},
		},
		{
			name:          "the session inherited from travels",
			fromSessionID: from.String(),
			wantReq:       &apimodel.SessionCreateRequest{ProjectName: "demo", FromSessionID: &from},
			wantPrinted:   []any{res},
		},
		{
			name:          "a from-session-id that is not a uuid is refused",
			fromSessionID: "latest",
			wantErr:       `parse --from-session-id "latest"`,
		},
		{
			name:    "api error passes through",
			wantReq: &apimodel.SessionCreateRequest{ProjectName: "demo"},
			apiErr:  errAPI,
			wantErr: errAPI.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("SessionCreate", mock.Anything, *tt.wantReq).Return(res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewSessionCreateHandler(api, p)

			// Act
			err := h.Create(context.Background(), testConfig, tt.addressing, tt.fromSessionID)

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
