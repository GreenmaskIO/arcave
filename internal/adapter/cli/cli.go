// Package cli adapts GM's services to Cobra commands: each handler invokes a
// service method and renders the result through a Printer — the model types go
// out as JSON (Savepoint for single-savepoint payloads, the response envelopes
// for multi-part responses). The Printer is injected, so handlers are unit-
// testable without touching stdout. Command definitions and flags live in
// internal/cmd, which resolves the config and logger in RunE and passes typed
// arguments here — the handlers know nothing of cobra or viper.
package cli

import (
	"context"
	"encoding/json"
	"io"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/gm-tools/internal/client/docker"
	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/service"
)

// DefaultWaitTimeout is the default bound (seconds) for the rollback health
// check, surfaced here so command definitions need not import the docker client.
const DefaultWaitTimeout = docker.DefaultUpWaitTimeout

// gmService is the set of service workflows the CLI adapter delegates to. It is
// declared here, on the consumer side, so the handlers depend on behaviour
// rather than the concrete *service.Service — the real type satisfies it
// implicitly and tests can substitute a fake.
type gmService interface {
	Init(ctx context.Context, cfg config.Config, override bool) (resolvedName, path string, err error)
	CreateSavepoint(ctx context.Context, cfg config.Config, name, description string) (model.Savepoint, error)
	ListSavepoints() ([]model.Savepoint, error)
	RemoveSavepoints(cfg config.Config, req service.RemoveRequest) (service.RemoveResult, error)
	Rollback(ctx context.Context, cfg config.Config, ref string, waitTimeout int) (service.RollbackResult, error)
}

// Printer renders a command result. The production printer marshals v to
// indented JSON on stdout (NewJSONPrinter); unit tests substitute a fake that
// captures v, so a handler can be exercised without touching stdout.
type Printer interface {
	Print(v any) error
}

// Handlers holds the services the CLI adapter delegates to and the printer that
// renders their results.
type Handlers struct {
	svc     gmService
	printer Printer
}

// New returns CLI handlers backed by svc, printing results through printer.
func New(svc gmService, printer Printer) *Handlers {
	return &Handlers{svc: svc, printer: printer}
}

// jsonPrinter renders values as indented JSON to w.
type jsonPrinter struct {
	w io.Writer
}

// NewJSONPrinter returns a Printer that writes indented JSON to w. All gm
// commands report their results as JSON; logs go to stderr via the context
// logger.
func NewJSONPrinter(w io.Writer) Printer {
	return jsonPrinter{w: w}
}

func (p jsonPrinter) Print(v any) error {
	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Init initialises the project from the resolved config and logs a confirmation
// via the context logger.
func (h *Handlers) Init(ctx context.Context, cfg config.Config, override bool) error {
	resolvedName, path, err := h.svc.Init(ctx, cfg, override)
	if err != nil {
		return err
	}
	log.Ctx(ctx).Info().Str("name", resolvedName).Str("path", path).Msg("initialized GM project")
	return nil
}

// SavepointCreate creates a savepoint and prints its metadata as JSON.
func (h *Handlers) SavepointCreate(ctx context.Context, cfg config.Config, name, description string) error {
	sp, err := h.svc.CreateSavepoint(ctx, cfg, name, description)
	if err != nil {
		return err
	}
	return h.printer.Print(sp)
}

// SavepointList prints all savepoints as a JSON array. It reads only the
// manifest, so it needs no config.
func (h *Handlers) SavepointList() error {
	sps, err := h.svc.ListSavepoints()
	if err != nil {
		return err
	}
	return h.printer.Print(sps)
}

// SavepointRemove removes savepoints and prints what was (or would be) removed.
func (h *Handlers) SavepointRemove(cfg config.Config, req service.RemoveRequest) error {
	res, err := h.svc.RemoveSavepoints(cfg, req)
	if err != nil {
		return err
	}
	return h.printer.Print(model.RemoveResponse{
		DryRun:  res.DryRun,
		Removed: res.Removed,
	})
}

// Rollback restores a savepoint and prints what was restored.
func (h *Handlers) Rollback(ctx context.Context, cfg config.Config, ref string, waitTimeout int) error {
	res, err := h.svc.Rollback(ctx, cfg, ref, waitTimeout)
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
