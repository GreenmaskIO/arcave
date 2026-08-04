package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

type savepointListService interface {
	ListSavepoints(ctx context.Context, req model.ListRequest) ([]model.Savepoint, error)
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

// SavepointList prints the savepoints req asks for as a JSON array. It reads
// only the savepoint store, so it needs no config.
func (h *SavepointListHandler) SavepointList(ctx context.Context, req model.ListRequest) error {
	sps, err := h.svc.ListSavepoints(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(sps)
}
