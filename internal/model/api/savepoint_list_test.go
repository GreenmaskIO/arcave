package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSavepointListRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     SavepointListRequest
		wantErr error
	}{
		{
			name: "full scope",
			req:  SavepointListRequest{ProjectName: "myapp", ProviderID: "compose", StackID: "demo"},
		},
		{
			name:    "missing project name",
			req:     SavepointListRequest{ProviderID: "compose", StackID: "demo"},
			wantErr: ErrProjectNameRequired,
		},
		{
			name:    "missing provider id",
			req:     SavepointListRequest{ProjectName: "myapp", StackID: "demo"},
			wantErr: ErrProviderIDRequired,
		},
		{
			name:    "missing stack id",
			req:     SavepointListRequest{ProjectName: "myapp", ProviderID: "compose"},
			wantErr: ErrStackIDRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.req.Validate()

			// Assert
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}
