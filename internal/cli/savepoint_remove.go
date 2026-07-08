package cli

import (
	"context"
	"errors"

	"github.com/greenmaskio/gm-tools/internal/model"
)

type savepointRemoveService interface {
	RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error)
}

// SavepointRemoveHandler holds the services the CLI adapter delegates to and the printer that
// renders their results.
type SavepointRemoveHandler struct {
	svc     savepointRemoveService
	printer model.Printer
}

func NewSavepointRemoveHandler(svc savepointRemoveService, printer model.Printer) *SavepointRemoveHandler {
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

// removeUsageError rephrases the service's flag-agnostic selector-validation
// errors in terms of the CLI flags the user typed. Only this layer knows the
// flag names, so the guidance lives here rather than in the service.
func removeUsageError(err error) error {
	switch {
	case errors.Is(err, model.ErrRemoveSelectorConflict):
		return errors.New("cannot combine savepoint IDs with --retain-recent/--retain-for/--before-date")
	case errors.Is(err, model.ErrRemoveNoSelector):
		return errors.New("specify savepoint IDs, or at least one of --retain-recent, --retain-for or --before-date")
	case errors.Is(err, model.ErrRetainRecentNegative):
		return errors.New("--retain-recent must be zero or positive")
	default:
		return err
	}
}
