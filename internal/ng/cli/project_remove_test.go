package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestProjectRemoveHandler_Remove(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		wantReq     *apimodel.ProjectRemoveRequest
		apiErr      error
		wantErr     string
	}{
		{
			name:        "removes the named project",
			projectName: "demo",
			wantReq:     &apimodel.ProjectRemoveRequest{Name: "demo"},
		},
		{
			name:    "an unnamed project is refused",
			wantErr: apimodel.ErrProjectNameRequired.Error(),
		},
		{
			name:        "api error passes through",
			projectName: "demo",
			wantReq:     &apimodel.ProjectRemoveRequest{Name: "demo"},
			apiErr:      errAPI,
			wantErr:     errAPI.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("ProjectRemove", mock.Anything, *tt.wantReq).Return(tt.apiErr)
			}
			h := NewProjectRemoveHandler(api)

			// Act
			err := h.Remove(context.Background(), tt.projectName)

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			api.AssertExpectations(t)
		})
	}
}
