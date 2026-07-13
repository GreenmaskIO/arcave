package cli

import (
	"context"

	"github.com/greenmaskio/gm-tools/internal/model"
)

type savepointListService interface {
	ListSavepoints(ctx context.Context) ([]model.Savepoint, error)
}

type SavepointListHandler struct {
	svc     savepointListService
	printer printer
}

func NewSavepointListHandler(svc savepointListService, printer printer) *SavepointListHandler {
	return &SavepointListHandler{
		svc:     svc,
		printer: printer,
	}
}

// SavepointList prints all savepoints as a JSON array. It reads only the
// savepoint store, so it needs no config.
func (h *SavepointListHandler) SavepointList(ctx context.Context) error {
	sps, err := h.svc.ListSavepoints(ctx)
	if err != nil {
		return err
	}
	return h.printer.Print(sps)
}
