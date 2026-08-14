package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectRemoveRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     ProjectRemoveRequest
		wantErr error
	}{
		{name: "a named project", req: ProjectRemoveRequest{Name: "myapp"}},
		{name: "missing project name", req: ProjectRemoveRequest{}, wantErr: ErrProjectNameRequired},
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
