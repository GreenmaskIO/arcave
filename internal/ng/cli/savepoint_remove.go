package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	str2duration "github.com/xhit/go-str2duration/v2"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// savepointRemoveAPI is the control plane as this handler needs it.
type savepointRemoveAPI interface {
	SavepointRemove(ctx context.Context, req apimodel.SavepointRemoveRequest) error
}

// SavepointRemoveParams is the selector the command carries, still as text.
type SavepointRemoveParams struct {
	IDs []string
	// RetainRecent is a pointer because zero is a count the user may mean.
	RetainRecent *int
	RetainFor    string
	BeforeDate   string
	DryRun       bool
}

type SavepointRemoveHandler struct {
	api savepointRemoveAPI
}

func NewSavepointRemoveHandler(api savepointRemoveAPI) *SavepointRemoveHandler {
	return &SavepointRemoveHandler{api: api}
}

// Remove deletes the savepoints the selector names, or reports what it would
// delete when the params say dry run.
func (h *SavepointRemoveHandler) Remove(
	ctx context.Context,
	cfg config.Config,
	a Addressing,
	params SavepointRemoveParams,
) error {
	req, err := newSavepointRemoveRequest(params)
	if err != nil {
		return err
	}
	s, err := resolveScope(cfg, a)
	if err != nil {
		return err
	}
	req.ProjectName, req.SessionID, req.ProviderID, req.StackID = s.ProjectName, s.SessionID, s.ProviderID, s.StackID

	if err := req.Validate(); err != nil {
		return removeFlagUsageError(err)
	}
	return h.api.SavepointRemove(ctx, req)
}

// removeFlagUsageError restates a selector sentinel as the remedy in the flags
// the user typed — the tool-field wording of mcp.go's removeUsageError is no
// help to someone at a shell.
func removeFlagUsageError(err error) error {
	switch {
	case errors.Is(err, apimodel.ErrRemoveSelectorConflict):
		return fmt.Errorf("%w: pass either savepoint ids, or --retain-recent/--retain-for/--before-date", err)
	case errors.Is(err, apimodel.ErrRemoveNoSelector):
		return fmt.Errorf("%w: pass savepoint ids, or one of --retain-recent, --retain-for, --before-date", err)
	case errors.Is(err, apimodel.ErrRetainRecentNegative):
		return fmt.Errorf("%w: --retain-recent must be zero or positive", err)
	default:
		return err
	}
}

// newSavepointRemoveRequest parses the selectors the flags carry as text.
func newSavepointRemoveRequest(params SavepointRemoveParams) (apimodel.SavepointRemoveRequest, error) {
	req := apimodel.SavepointRemoveRequest{RetainRecent: params.RetainRecent, DryRun: params.DryRun}

	for _, raw := range params.IDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return apimodel.SavepointRemoveRequest{}, fmt.Errorf("parse savepoint id %q: %w", raw, err)
		}
		req.SavepointIDs = append(req.SavepointIDs, id)
	}
	if params.RetainFor != "" {
		d, err := str2duration.ParseDuration(params.RetainFor)
		if err != nil {
			return apimodel.SavepointRemoveRequest{}, fmt.Errorf("parse --retain-for %q: %w", params.RetainFor, err)
		}
		req.RetainFor = &d
	}
	if params.BeforeDate != "" {
		ts, err := time.Parse(time.RFC3339Nano, params.BeforeDate)
		if err != nil {
			return apimodel.SavepointRemoveRequest{}, fmt.Errorf("parse --before-date %q: %w", params.BeforeDate, err)
		}
		req.BeforeDate = &ts
	}
	return req, nil
}
