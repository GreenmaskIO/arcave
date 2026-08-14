package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestProjectApplyHandler_Apply(t *testing.T) {
	res := apimodel.ProjectApplyResponse{Name: "demo", Status: apimodel.ProjectApplyStatusCreated}
	wantReq := apimodel.ProjectApplyRequest{Project: model.Project{
		Name:      "demo",
		Providers: model.Providers{Stacks: []string{"my-linux-box"}},
	}}

	tests := []struct {
		name        string
		cfg         config.Config
		wantReq     *apimodel.ProjectApplyRequest
		apiErr      error
		printerErr  error
		wantErr     string
		wantPrinted []any
	}{
		{
			name:        "the project section travels whole",
			cfg:         testConfig,
			wantReq:     &wantReq,
			wantPrinted: []any{res},
		},
		{
			name:    "a project with no name is refused",
			cfg:     config.Config{},
			wantErr: apimodel.ErrProjectNameRequired.Error(),
		},
		{
			name:    "api error passes through",
			cfg:     testConfig,
			wantReq: &wantReq,
			apiErr:  errAPI,
			wantErr: errAPI.Error(),
		},
		{
			name:        "printer error surfaces",
			cfg:         testConfig,
			wantReq:     &wantReq,
			printerErr:  errPrint,
			wantErr:     errPrint.Error(),
			wantPrinted: []any{res},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("ProjectApply", mock.Anything, *tt.wantReq).Return(res, tt.apiErr)
			}
			p := &capturePrinter{}
			p.On("Print", res).Return(tt.printerErr).Maybe()
			h := NewProjectApplyHandler(api, p)

			// Act
			err := h.Apply(context.Background(), tt.cfg)

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

func TestProjectToModel(t *testing.T) {
	// Arrange
	cfgProject := config.Project{
		Name: "myapp",
		Providers: config.Providers{
			Sandbox: "my-linux-box",
			Stacks:  []string{"my-linux-box", "ci-2"},
		},
	}

	// Act
	got := projectToModel(cfgProject)

	// Assert
	assert.Equal(t, model.Project{
		Name: "myapp",
		Providers: model.Providers{
			Sandbox: "my-linux-box",
			Stacks:  []string{"my-linux-box", "ci-2"},
		},
	}, got)
}
