package cli

import (
	"context"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// providerListAPI is the control plane as this handler needs it.
type providerListAPI interface {
	ProviderList(ctx context.Context) (apimodel.ProviderListResponse, error)
}

type ProviderListHandler struct {
	api     providerListAPI
	printer printer
}

func NewProviderListHandler(api providerListAPI, printer printer) *ProviderListHandler {
	return &ProviderListHandler{api: api, printer: printer}
}

// List prints the providers registered with the control plane, by id and type.
func (h *ProviderListHandler) List(ctx context.Context) error {
	res, err := h.api.ProviderList(ctx)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}
