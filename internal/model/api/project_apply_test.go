package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestProjectApplyRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     ProjectApplyRequest
		wantErr error
	}{
		{
			name: "a named project",
			req:  ProjectApplyRequest{Project: model.Project{Name: "myapp"}},
		},
		{
			name: "providers are not judged",
			req: ProjectApplyRequest{Project: model.Project{
				Name:      "myapp",
				Providers: model.Providers{Sandbox: "Not-A-Type_42", Stacks: []string{"", "ci-2"}},
			}},
		},
		{
			name:    "missing project name",
			req:     ProjectApplyRequest{},
			wantErr: ErrProjectNameRequired,
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
