package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

type savepointCreateService interface {
	CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error)
}

type SavepointCreateHandler struct {
	svc     savepointCreateService
	printer printer
}

func NewSavepointCreateHandler(svc savepointCreateService, printer printer) *SavepointCreateHandler {
	return &SavepointCreateHandler{
		svc:     svc,
		printer: printer,
	}
}

// SavepointCreate creates a savepoint and prints its metadata as JSON.
//
// The health-check bound comes from cfg rather than from a parameter: it is a
// project setting (compose.wait_timeout, which the --wait-timeout flag
// overrides), resolved once by cmd/arcave exactly like the compose file list
// beside it.
func (h *SavepointCreateHandler) SavepointCreate(ctx context.Context, cfg config.Config, name, description string) error {
	sp, err := h.svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		ComposeFiles: cfg.Project.ComposeFiles,
		Name:         name,
		Description:  description,
		WaitTimeout:  cfg.Compose.WaitTimeout,
	})
	if err != nil {
		return err
	}
	return h.printer.Print(sp)
}
