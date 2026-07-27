package cli

import (
	"context"

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
	res, err := h.svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: cfg.Project.ComposeFiles,
		Ref:          ref,
		WaitTimeout:  cfg.Compose.WaitTimeout,
	})
	if err != nil {
		return err
	}
	return h.printer.Print(model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored:     res.Restored,
	})
}
