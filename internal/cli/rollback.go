package cli

import (
	"context"
	"time"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

type rollbackService interface {
	Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error)
}

type RollbackHandler struct {
	svc     rollbackService
	printer printer
}

func NewRollbackHandler(svc rollbackService, printer printer) *RollbackHandler {
	return &RollbackHandler{
		svc:     svc,
		printer: printer,
	}
}

// Rollback restores a savepoint and prints what was restored. The bound it
// waits for before deciding the rollback failed and reverting comes from cfg
// (compose.wait_timeout, overridden by --wait-timeout), like the compose file
// list beside it.
func (h *RollbackHandler) Rollback(ctx context.Context, cfg config.Config, ref string) error {
	res, err := h.svc.Rollback(ctx, newRollbackRequest(ref, cfg.Project.ComposeFiles, cfg.Compose.WaitTimeout))
	if err != nil {
		return err
	}
	return h.printer.Print(model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored:     res.Restored,
	})
}

// newRollbackRequest builds the whole request both adapters send — the CLI from
// its positional argument and config, the MCP tool from its input fields — so
// neither can drift on what "1780335122" means, nor forget a field.
//
// "latest" is reserved; a run of decimal digits is an id, that being exactly what
// SavepointID.Validate admits; anything left is a name. An empty ref sets Name
// to "", which req.Validate refuses, so it needs no case of its own.
func newRollbackRequest(ref string, composeFiles []string, waitTimeout time.Duration) model.RollbackRequest {
	req := model.RollbackRequest{ComposeFiles: composeFiles, WaitTimeout: waitTimeout}
	switch {
	case ref == model.RefLatest:
		latest := true
		req.Latest = &latest
	case model.SavepointID(ref).Validate() == nil:
		id := model.SavepointID(ref)
		req.ID = &id
	default:
		req.Name = &ref
	}
	return req
}
