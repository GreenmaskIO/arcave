package cli

import (
	"context"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// projectListAPI is the control plane as this handler needs it.
type projectListAPI interface {
	ProjectList(ctx context.Context) (apimodel.ProjectListResponse, error)
}

type ProjectListHandler struct {
	api     projectListAPI
	printer printer
}

func NewProjectListHandler(api projectListAPI, printer printer) *ProjectListHandler {
	return &ProjectListHandler{api: api, printer: printer}
}

// List prints every project the control plane holds.
func (h *ProjectListHandler) List(ctx context.Context) error {
	res, err := h.api.ProjectList(ctx)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}
