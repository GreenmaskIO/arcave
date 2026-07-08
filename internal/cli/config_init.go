// Package cli adapts GM's services to Cobra commands: each handler invokes a
// service method and renders the result through a Printer — the model types go
// out as JSON (Savepoint for single-savepoint payloads, the response envelopes
// for multi-part responses). The Printer is injected, so handlers are unit-
// testable without touching stdout. Command definitions and flags live in
// cmd/gm, which resolves the config and logger before RunE and passes typed
// arguments here — the handlers know nothing of cobra or viper.
package cli

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
)

type configInitService interface {
	Init(ctx context.Context, cfg config.Config, override bool) (resolvedName, path string, err error)
}

type ConfigInitHandler struct {
	svc     configInitService
	printer model.Printer
}

func NewConfigInitHandler(svc configInitService, printer model.Printer) *ConfigInitHandler {
	return &ConfigInitHandler{svc: svc, printer: printer}
}

func (h *ConfigInitHandler) Init(ctx context.Context, cfg config.Config, override bool) error {
	resolvedName, path, err := h.svc.Init(ctx, cfg, override)
	if err != nil {
		return err
	}
	log.Ctx(ctx).Info().Str("name", resolvedName).
		Str("path", path).
		Msg("initialized GM project")
	return nil
}
