package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// sessionStatusAPI returns any: the status payload has no shape yet.
type sessionStatusAPI interface {
	SessionStatus(ctx context.Context, req apimodel.SessionStatusRequest) (any, error)
}

type SessionStatusHandler struct {
	api     sessionStatusAPI
	printer printer
}

func NewSessionStatusHandler(api sessionStatusAPI, printer printer) *SessionStatusHandler {
	return &SessionStatusHandler{api: api, printer: printer}
}

// Status prints whatever the control plane answers with for the session.
func (h *SessionStatusHandler) Status(ctx context.Context, cfg config.Config, a Addressing) error {
	projectName, err := resolveProjectName(a.Project, cfg.Project.Name)
	if err != nil {
		return err
	}
	sessionID, err := resolveSessionID(a.SessionID)
	if err != nil {
		return err
	}

	req := apimodel.SessionStatusRequest{ProjectName: projectName, SessionID: sessionID}
	if err := req.Validate(); err != nil {
		return err
	}
	res, err := h.api.SessionStatus(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}
