package cli

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// rollbackAPI is the control plane as this handler needs it.
type rollbackAPI interface {
	SavepointRollback(ctx context.Context, req apimodel.SavepointRollbackRequest) (apimodel.SavepointRollbackResponse, error)
}

// RollbackParams is what the rollback command carries of its own.
type RollbackParams struct {
	SavepointID string
	// WithSafetySavepoint arrives already defaulted by cobra.
	WithSafetySavepoint bool
}

type RollbackHandler struct {
	api     rollbackAPI
	printer printer
}

func NewRollbackHandler(api rollbackAPI, printer printer) *RollbackHandler {
	return &RollbackHandler{api: api, printer: printer}
}

// Rollback restores the stack to the named savepoint.
func (h *RollbackHandler) Rollback(
	ctx context.Context,
	cfg config.Config,
	a Addressing,
	params RollbackParams,
) error {
	savepointID, err := uuid.Parse(params.SavepointID)
	if err != nil {
		return fmt.Errorf("parse savepoint id %q: %w", params.SavepointID, err)
	}
	s, err := resolveScope(cfg, a)
	if err != nil {
		return err
	}

	req := apimodel.SavepointRollbackRequest{
		ProjectName:         s.ProjectName,
		SessionID:           s.SessionID,
		ProviderID:          s.ProviderID,
		StackID:             s.StackID,
		SavepointID:         savepointID,
		WithSafetySavepoint: params.WithSafetySavepoint,
	}
	if err := req.Validate(); err != nil {
		return err
	}
	res, err := h.api.SavepointRollback(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}
