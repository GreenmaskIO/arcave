package cli

import (
	"context"

	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
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
	restored := make([]model.RestoredMount, 0, len(res.Restored))
	for _, m := range res.Restored {
		restored = append(restored, model.RestoredMount{Service: m.Service, MountPath: m.Target})
	}
	return h.printer.Print(model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored:     restored,
	})
}
