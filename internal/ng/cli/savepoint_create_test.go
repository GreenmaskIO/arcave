package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestSavepointCreateHandler_Create(t *testing.T) {
	res := apimodel.SavepointCreateResponse{ProviderID: "my-linux-box", SavepointID: testSavepointID}
	base := apimodel.SavepointCreateRequest{
		ProjectName: "demo",
		SessionID:   testSessionID,
		ProviderID:  "my-linux-box",
		StackID:     "web",
		Name:        "before-migration",
		Description: "schema v2",
		Labels:      map[string]string{"team": "core"},
	}
	// req returns base with mut applied.
	req := func(mut func(r *apimodel.SavepointCreateRequest)) *apimodel.SavepointCreateRequest {
		r := base
		mut(&r)
		return &r
	}
	params := SavepointCreateParams{
		Args:        []string{"before-migration"},
		Description: "schema v2",
		Labels:      []string{"team=core"},
	}

	tests := []struct {
		name        string
		addressing  Addressing
		env         string // $ARCAVE_SESSION_ID; empty is unset
		params      SavepointCreateParams
		wantReq     *apimodel.SavepointCreateRequest // nil: nothing may reach the api
		apiErr      error
		printerErr  error
		wantErr     string
		wantPrinted []any
	}{
		{
			name:        "the config answers the project and the provider",
			addressing:  Addressing{Stack: "web"},
			env:         testSessionID.String(),
			params:      params,
			wantReq:     req(func(*apimodel.SavepointCreateRequest) {}),
			wantPrinted: []any{res},
		},
		{
			name:        "every flag beats the config",
			addressing:  Addressing{Project: "other", SessionID: testSessionID.String(), Provider: "ci-2", Stack: "web"},
			params:      params,
			wantReq:     req(func(r *apimodel.SavepointCreateRequest) { r.ProjectName, r.ProviderID = "other", "ci-2" }),
			wantPrinted: []any{res},
		},
		{
			name:       "--name names the savepoint when no argument does",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params: SavepointCreateParams{
				Name:        "before-migration",
				Description: "schema v2",
				Labels:      []string{"team=core"},
			},
			wantReq:     req(func(*apimodel.SavepointCreateRequest) {}),
			wantPrinted: []any{res},
		},
		{
			name:       "a name given twice and disagreeing is refused",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     SavepointCreateParams{Args: []string{"before-migration"}, Name: "other"},
			wantErr:    "both as argument and --name",
		},
		{
			name:       "a label that is not key=value is refused",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     SavepointCreateParams{Args: []string{"before-migration"}, Labels: []string{"team"}},
			wantErr:    "is not key=value",
		},
		{
			name:       "an unnamed session reaches nothing",
			addressing: Addressing{Stack: "web"},
			params:     params,
			wantErr:    "arcave session create",
		},
		{
			name:       "an unnamed stack reaches nothing",
			addressing: Addressing{},
			env:        testSessionID.String(),
			params:     params,
			wantErr:    "no stack",
		},
		{
			name:       "api error passes through",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     params,
			wantReq:    req(func(*apimodel.SavepointCreateRequest) {}),
			apiErr:     errAPI,
			wantErr:    errAPI.Error(),
		},
		{
			name:        "printer error surfaces",
			addressing:  Addressing{Stack: "web"},
			env:         testSessionID.String(),
			params:      params,
			wantReq:     req(func(*apimodel.SavepointCreateRequest) {}),
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
				api.On("SavepointCreate", mock.Anything, *tt.wantReq).Return(res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewSavepointCreateHandler(api, p)

			// Act
			err := h.Create(context.Background(), testConfig, tt.addressing, tt.params)

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
