package api

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSessionStatusRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     SessionStatusRequest
		wantErr error
	}{
		{name: "full request", req: SessionStatusRequest{ProjectName: "myapp", SessionID: testSessionID}},
		{
			name:    "missing project name",
			req:     SessionStatusRequest{SessionID: testSessionID},
			wantErr: ErrProjectNameRequired,
		},
		{
			name:    "missing session id",
			req:     SessionStatusRequest{ProjectName: "myapp", SessionID: uuid.Nil},
			wantErr: ErrSessionIDRequired,
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
