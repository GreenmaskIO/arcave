package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecommendRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     RecommendRequest
		wantErr error
	}{
		{name: "full scope", req: RecommendRequest{ProjectName: "myapp", ProviderID: "compose", StackID: "demo"}},
		{
			name:    "missing project name",
			req:     RecommendRequest{ProviderID: "compose", StackID: "demo"},
			wantErr: ErrProjectNameRequired,
		},
		{
			name:    "missing provider id",
			req:     RecommendRequest{ProjectName: "myapp", StackID: "demo"},
			wantErr: ErrProviderIDRequired,
		},
		{
			name:    "missing stack id",
			req:     RecommendRequest{ProjectName: "myapp", ProviderID: "compose"},
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
