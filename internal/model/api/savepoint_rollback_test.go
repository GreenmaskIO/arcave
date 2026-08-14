package api

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSavepointRollbackRequest_Validate(t *testing.T) {
	req := SavepointRollbackRequest{
		ProjectName: "myapp",
		SessionID:   testSessionID,
		ProviderID:  "compose",
		StackID:     "demo",
		SavepointID: testSavepointID,
	}

	withoutSavepoint := req
	withoutSavepoint.SavepointID = uuid.Nil

	tests := []struct {
		name    string
		req     SavepointRollbackRequest
		wantErr error
	}{
		{name: "full request", req: req},
		{name: "missing savepoint id", req: withoutSavepoint, wantErr: ErrSavepointIDRequired},
		{
			name: "missing project name",
			req: SavepointRollbackRequest{
				SessionID:   testSessionID,
				ProviderID:  "compose",
				StackID:     "demo",
				SavepointID: testSavepointID,
			},
			wantErr: ErrProjectNameRequired,
		},
		{
			name: "missing session id",
			req: SavepointRollbackRequest{
				ProjectName: "myapp",
				ProviderID:  "compose",
				StackID:     "demo",
				SavepointID: testSavepointID,
			},
			wantErr: ErrSessionIDRequired,
		},
		{
			name: "missing provider id",
			req: SavepointRollbackRequest{
				ProjectName: "myapp",
				SessionID:   testSessionID,
				StackID:     "demo",
				SavepointID: testSavepointID,
			},
			wantErr: ErrProviderIDRequired,
		},
		{
			name: "missing stack id",
			req: SavepointRollbackRequest{
				ProjectName: "myapp",
				SessionID:   testSessionID,
				ProviderID:  "compose",
				SavepointID: testSavepointID,
			},
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
