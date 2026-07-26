package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

type recommendService interface {
	Recommend(ctx context.Context, composeFiles ...string) ([]model.Recommendation, error)
}

type RecommendHandler struct {
	svc     recommendService
	printer printer
}

func NewRecommendHandler(svc recommendService, printer printer) *RecommendHandler {
	return &RecommendHandler{
		svc:     svc,
		printer: printer,
	}
}

// Recommend prints the volumes the project's services should mount, as a JSON
// array — empty when arcave has nothing to advise. It reads only compose files
// and the recommender catalog, so it touches neither storage nor the daemon.
//
// found reports whether the array carried anything, so the caller can turn "there
// is advice" into a non-zero exit status without parsing what was printed. It is
// a return value rather than an error because it is not one: the command did
// exactly what was asked, and cobra would print an error to stderr next to the
// JSON on stdout.
func (h *RecommendHandler) Recommend(ctx context.Context, composeFiles ...string) (found bool, err error) {
	recs, err := h.svc.Recommend(ctx, composeFiles...)
	if err != nil {
		return false, err
	}
	if err := h.printer.Print(recs); err != nil {
		return false, err
	}
	return len(recs) > 0, nil
}
