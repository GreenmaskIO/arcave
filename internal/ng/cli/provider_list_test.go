package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestProviderListHandler_List(t *testing.T) {
	res := apimodel.ProviderListResponse{
		Providers: []apimodel.ProviderListItem{{ID: "my-linux-box", Type: "linux", Status: "ready"}},
	}

	tests := []struct {
		name        string
		apiErr      error
		printerErr  error
		wantErr     string
		wantPrinted []any
	}{
		{name: "prints the response", wantPrinted: []any{res}},
		{name: "api error passes through", apiErr: errAPI, wantErr: errAPI.Error()},
		{
			name:        "printer error surfaces",
			printerErr:  errPrint,
			wantErr:     errPrint.Error(),
			wantPrinted: []any{res},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			api.On("ProviderList", mock.Anything).Return(res, tt.apiErr)
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewProviderListHandler(api, p)

			// Act
			err := h.List(context.Background())

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
