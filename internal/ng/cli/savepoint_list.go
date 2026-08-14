package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// savepointListAPI is the control plane as this handler needs it.
type savepointListAPI interface {
	SavepointList(ctx context.Context, req apimodel.SavepointListRequest) (apimodel.SavepointListResponse, error)
}

// SavepointListParams is what the savepoint list command carries of its own.
type SavepointListParams struct {
	Limit                   int
	Offset                  int
	IncludeSafetySavepoints bool
}

type SavepointListHandler struct {
	api     savepointListAPI
	printer printer
}

func NewSavepointListHandler(api savepointListAPI, printer printer) *SavepointListHandler {
	return &SavepointListHandler{api: api, printer: printer}
}

// List prints the stack's savepoints.
func (h *SavepointListHandler) List(
	ctx context.Context,
	cfg config.Config,
	a Addressing,
	params SavepointListParams,
) error {
	s, err := resolveStackScope(cfg, a)
	if err != nil {
		return err
	}

	req := apimodel.SavepointListRequest{
		ProjectName:             s.ProjectName,
		ProviderID:              s.ProviderID,
		StackID:                 s.StackID,
		Limit:                   params.Limit,
		Offset:                  params.Offset,
		IncludeSafetySavepoints: params.IncludeSafetySavepoints,
	}
	if err := req.Validate(); err != nil {
		return err
	}
	res, err := h.api.SavepointList(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}
