package api

import (
	"time"

	"github.com/google/uuid"
)

// SavepointRemoveRequest selects savepoints of one stack to remove. At least one
// selector is required, and ids never combine with retention rules.
type SavepointRemoveRequest struct {
	ProjectName string
	SessionID   uuid.UUID
	ProviderID  string
	StackID     string

	SavepointIDs []uuid.UUID
	RetainRecent *int
	RetainFor    *time.Duration
	BeforeDate   *time.Time
	DryRun       bool
}

func (r SavepointRemoveRequest) Validate() error {
	if err := validateScope(r.ProjectName, r.SessionID, r.ProviderID, r.StackID); err != nil {
		return err
	}

	retention := r.RetainRecent != nil || r.RetainFor != nil || r.BeforeDate != nil
	switch {
	case len(r.SavepointIDs) > 0 && retention:
		return ErrRemoveSelectorConflict
	case len(r.SavepointIDs) == 0 && !retention:
		return ErrRemoveNoSelector
	case r.RetainRecent != nil && *r.RetainRecent < 0:
		return ErrRetainRecentNegative
	}
	return nil
}
