package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// sessionListAPI is the control plane as this handler needs it.
type sessionListAPI interface {
	SessionList(ctx context.Context, req apimodel.SessionListRequest) (apimodel.SessionListResponse, error)
}

type SessionListHandler struct {
	api     sessionListAPI
	printer printer
}

func NewSessionListHandler(api sessionListAPI, printer printer) *SessionListHandler {
	return &SessionListHandler{api: api, printer: printer}
}

// List prints the project's sessions.
func (h *SessionListHandler) List(ctx context.Context, cfg config.Config, a Addressing) error {
	projectName, err := resolveProjectName(a.Project, cfg.Project.Name)
	if err != nil {
		return err
	}

	req := apimodel.SessionListRequest{ProjectName: projectName}
	if err := req.Validate(); err != nil {
		return err
	}
	res, err := h.api.SessionList(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}
