package cli

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// sessionCreateAPI is the control plane as this handler needs it.
type sessionCreateAPI interface {
	SessionCreate(ctx context.Context, req apimodel.SessionCreateRequest) (apimodel.SessionCreateResponse, error)
}

type SessionCreateHandler struct {
	api     sessionCreateAPI
	printer printer
}

func NewSessionCreateHandler(api sessionCreateAPI, printer printer) *SessionCreateHandler {
	return &SessionCreateHandler{api: api, printer: printer}
}

// Create opens a session in the addressed project, inheriting from
// fromSessionID when it is given.
func (h *SessionCreateHandler) Create(
	ctx context.Context,
	cfg config.Config,
	a Addressing,
	fromSessionID string,
) error {
	projectName, err := resolveProjectName(a.Project, cfg.Project.Name)
	if err != nil {
		return err
	}

	req := apimodel.SessionCreateRequest{ProjectName: projectName}
	if fromSessionID != "" {
		from, err := uuid.Parse(fromSessionID)
		if err != nil {
			return fmt.Errorf("parse --from-session-id %q: %w", fromSessionID, err)
		}
		req.FromSessionID = &from
	}
	if err := req.Validate(); err != nil {
		return err
	}
	res, err := h.api.SessionCreate(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}
