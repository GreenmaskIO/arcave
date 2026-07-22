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

// Rollback restores a savepoint and prints what was restored.
func (h *RollbackHandler) Rollback(ctx context.Context, cfg config.Config, ref string, waitTimeout int) error {
	res, err := h.svc.Rollback(ctx, model.RollbackRequest{
		ComposeFiles: cfg.Project.ComposeFiles,
		Ref:          ref,
		WaitTimeout:  waitTimeout,
	})
	if err != nil {
		return err
	}
	return h.printer.Print(model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored:     restoredMounts(res.Restored),
	})
}
