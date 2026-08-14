package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestRollbackHandler_Rollback(t *testing.T) {
	res := apimodel.SavepointRollbackResponse{Status: apimodel.SavepointRollbackStatusSuccess}
	base := apimodel.SavepointRollbackRequest{
		ProjectName:         "demo",
		SessionID:           testSessionID,
		ProviderID:          "my-linux-box",
		StackID:             "web",
		SavepointID:         testSavepointID,
		WithSafetySavepoint: true,
	}
	req := func(mut func(r *apimodel.SavepointRollbackRequest)) *apimodel.SavepointRollbackRequest {
		r := base
		mut(&r)
		return &r
	}
	params := RollbackParams{SavepointID: testSavepointID.String(), WithSafetySavepoint: true}

	tests := []struct {
		name        string
		addressing  Addressing
		env         string
		params      RollbackParams
		wantReq     *apimodel.SavepointRollbackRequest
		apiErr      error
		printerErr  error
		wantErr     string
		wantPrinted []any
	}{
		{
			name:        "the safety savepoint travels as the flag left it",
			addressing:  Addressing{Stack: "web"},
			env:         testSessionID.String(),
			params:      params,
			wantReq:     req(func(*apimodel.SavepointRollbackRequest) {}),
			wantPrinted: []any{res},
		},
		{
			name:        "opting out of the safety savepoint is carried through",
			addressing:  Addressing{Stack: "web"},
			env:         testSessionID.String(),
			params:      RollbackParams{SavepointID: testSavepointID.String()},
			wantReq:     req(func(r *apimodel.SavepointRollbackRequest) { r.WithSafetySavepoint = false }),
			wantPrinted: []any{res},
		},
		{
			name:       "an id that is not a uuid is refused before addressing",
			addressing: Addressing{},
			params:     RollbackParams{SavepointID: "latest"},
			wantErr:    `parse savepoint id "latest"`,
		},
		{
			name:       "an unnamed session reaches nothing",
			addressing: Addressing{Stack: "web"},
			params:     params,
			wantErr:    "arcave session create",
		},
		{
			name:       "api error passes through",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     params,
			wantReq:    req(func(*apimodel.SavepointRollbackRequest) {}),
			apiErr:     errAPI,
			wantErr:    errAPI.Error(),
		},
		{
			name:        "printer error surfaces",
			addressing:  Addressing{Stack: "web"},
			env:         testSessionID.String(),
			params:      params,
			wantReq:     req(func(*apimodel.SavepointRollbackRequest) {}),
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
				api.On("SavepointRollback", mock.Anything, *tt.wantReq).Return(res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewRollbackHandler(api, p)

			// Act
			err := h.Rollback(context.Background(), testConfig, tt.addressing, tt.params)

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
