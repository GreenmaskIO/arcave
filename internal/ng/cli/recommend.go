package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// recommendAPI is the control plane as this handler needs it.
type recommendAPI interface {
	Recommend(ctx context.Context, req apimodel.RecommendRequest) (apimodel.RecommendResponse, error)
}

type RecommendHandler struct {
	api     recommendAPI
	printer printer
}

func NewRecommendHandler(api recommendAPI, printer printer) *RecommendHandler {
	return &RecommendHandler{api: api, printer: printer}
}

// Recommend prints what a savepoint of this stack would fail to capture, and
// reports whether there was a finding.
func (h *RecommendHandler) Recommend(ctx context.Context, cfg config.Config, a Addressing) (bool, error) {
	s, err := resolveStackScope(cfg, a)
	if err != nil {
		return false, err
	}

	req := apimodel.RecommendRequest{
		ProjectName: s.ProjectName,
		ProviderID:  s.ProviderID,
		StackID:     s.StackID,
	}
	if err := req.Validate(); err != nil {
		return false, err
	}
	res, err := h.api.Recommend(ctx, req)
	if err != nil {
		return false, err
	}
	if err := h.printer.Print(res); err != nil {
		return false, err
	}
	return len(res.Recommendations) > 0, nil
}
