package cli

import (
	"context"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// projectRemoveAPI is the control plane as this handler needs it.
type projectRemoveAPI interface {
	ProjectRemove(ctx context.Context, req apimodel.ProjectRemoveRequest) error
}

type ProjectRemoveHandler struct {
	api projectRemoveAPI
}

func NewProjectRemoveHandler(api projectRemoveAPI) *ProjectRemoveHandler {
	return &ProjectRemoveHandler{api: api}
}

// Remove deletes the named project.
func (h *ProjectRemoveHandler) Remove(ctx context.Context, name string) error {
	req := apimodel.ProjectRemoveRequest{Name: name}
	if err := req.Validate(); err != nil {
		return err
	}
	return h.api.ProjectRemove(ctx, req)
}
