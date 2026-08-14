package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSavepointCreateRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     SavepointCreateRequest
		wantErr error
	}{
		{
			name: "full scope",
			req: SavepointCreateRequest{
				ProjectName: "myapp",
				SessionID:   testSessionID,
				ProviderID:  "compose",
				StackID:     "demo",
			},
		},
		{
			name:    "missing project name",
			req:     SavepointCreateRequest{SessionID: testSessionID, ProviderID: "compose", StackID: "demo"},
			wantErr: ErrProjectNameRequired,
		},
		{
			name:    "missing session id",
			req:     SavepointCreateRequest{ProjectName: "myapp", ProviderID: "compose", StackID: "demo"},
			wantErr: ErrSessionIDRequired,
		},
		{
			name:    "missing provider id",
			req:     SavepointCreateRequest{ProjectName: "myapp", SessionID: testSessionID, StackID: "demo"},
			wantErr: ErrProviderIDRequired,
		},
		{
			name:    "missing stack id",
			req:     SavepointCreateRequest{ProjectName: "myapp", SessionID: testSessionID, ProviderID: "compose"},
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
