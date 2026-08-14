package api

import (
	"github.com/greenmaskio/arcave/internal/model"
)

// RecommendRequest asks what state of one stack a savepoint would fail to
// capture.
type RecommendRequest struct {
	ProjectName string
	ProviderID  string
	StackID     string
}

func (r RecommendRequest) Validate() error {
	return validateStackScope(r.ProjectName, r.ProviderID, r.StackID)
}

type RecommendResponse struct {
	ProviderID string
	StackName  string
	// Recommendations is empty when there is nothing to advise, never nil on the wire.
	Recommendations []model.Recommendation
}
