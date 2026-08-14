package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// projectApplyAPI is the control plane as this handler needs it.
type projectApplyAPI interface {
	ProjectApply(ctx context.Context, req apimodel.ProjectApplyRequest) (apimodel.ProjectApplyResponse, error)
}

type ProjectApplyHandler struct {
	api     projectApplyAPI
	printer printer
}

func NewProjectApplyHandler(api projectApplyAPI, printer printer) *ProjectApplyHandler {
	return &ProjectApplyHandler{api: api, printer: printer}
}

// Apply creates or updates the project cfg declares.
func (h *ProjectApplyHandler) Apply(ctx context.Context, cfg config.Config) error {
	req := apimodel.ProjectApplyRequest{Project: projectToModel(cfg.Project)}
	if err := req.Validate(); err != nil {
		return err
	}
	res, err := h.api.ProjectApply(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}

// projectToModel copies the project section into the model type the request carries.
func projectToModel(p config.Project) model.Project {
	return model.Project{
		Name: p.Name,
		Providers: model.Providers{
			Sandbox: p.Providers.Sandbox,
			Stacks:  p.Providers.Stacks,
		},
	}
}
