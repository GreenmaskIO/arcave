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

// Rollback restores a savepoint and prints what was restored. The wait bound
// comes from cfg (compose.wait_timeout, overridden by --wait-timeout).
func (h *RollbackHandler) Rollback(ctx context.Context, cfg config.Config, ref string) error {
	res, err := h.svc.Rollback(ctx, newRollbackRequest(ref, cfg.Compose.WaitTimeout))
	if err != nil {
		return err
	}
	return h.printer.Print(model.RollbackResponse{
		RolledBackTo: res.Savepoint,
		Restored:     res.Restored,
	})
}

// newRollbackRequest builds the request both adapters send, so neither drifts on
// what a ref means: "latest" is reserved, digits are an id, the rest is a name.
func newRollbackRequest(ref string, waitTimeout time.Duration) model.RollbackRequest {
	req := model.RollbackRequest{WaitTimeout: waitTimeout}
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
