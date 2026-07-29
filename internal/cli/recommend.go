package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

// RecommendService is this package's view of the recommend workflow. It is
// consumer-owned like every other seam here, and exported for the same reason
// snapshot.DockerVolume is: it is named in a constructor's signature — a
// RecommendServiceFactory is a parameter of NewMCPHandler — so godoc has to be
// able to say what the contract is.
type RecommendService interface {
	Recommend(ctx context.Context, composeFiles ...string) ([]model.Recommendation, error)
}

// RecommendServiceFactory builds a RecommendService over the recommender catalogs
// merged on top of the built-in one, in order (empty for the built-in alone).
//
// The seam is a factory rather than a service because the catalog is fixed at
// construction while the catalogs to merge arrive with each MCP tool call — the
// CLI takes them from a flag once per process, an agent names them per call.
// Constructing one is cheap (embedded bytes plus any files named), so building it
// per call costs nothing worth caching.
type RecommendServiceFactory func(recommenderFiles []string) RecommendService

type RecommendHandler struct {
	svc     RecommendService
	printer printer
}

func NewRecommendHandler(svc RecommendService, printer printer) *RecommendHandler {
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
