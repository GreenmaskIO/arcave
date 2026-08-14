package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionCreateRequest_Validate(t *testing.T) {
	from := testSessionID

	tests := []struct {
		name    string
		req     SessionCreateRequest
		wantErr error
	}{
		{name: "from scratch", req: SessionCreateRequest{ProjectName: "myapp"}},
		{name: "inherited", req: SessionCreateRequest{ProjectName: "myapp", FromSessionID: &from}},
		{name: "missing project name", req: SessionCreateRequest{}, wantErr: ErrProjectNameRequired},
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
