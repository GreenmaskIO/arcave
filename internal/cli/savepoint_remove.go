package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

type savepointRemoveService interface {
	RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error)
}

// SavepointRemoveHandler holds the services the CLI adapter delegates to and the printer that
// renders their results.
type SavepointRemoveHandler struct {
	svc     savepointRemoveService
	printer printer
}

func NewSavepointRemoveHandler(svc savepointRemoveService, printer printer) *SavepointRemoveHandler {
	return &SavepointRemoveHandler{
		svc:     svc,
		printer: printer,
	}
}

// SavepointRemove removes savepoints and prints what was (or would be) removed.
func (h *SavepointRemoveHandler) SavepointRemove(ctx context.Context, req model.RemoveRequest) error {
	res, err := h.svc.RemoveSavepoints(ctx, req)
	if err != nil {
		return removeUsageError(err)
	}
	return h.printer.Print(model.RemoveResponse(res))
}
