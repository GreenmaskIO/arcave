package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/greenmaskio/arcave/internal/model"
)

type SavepointRollbackRequest struct {
	ProjectName string
	SessionID   uuid.UUID
	ProviderID  string
	StackID     string
	// InfrastructureName is unused: StackID already names the spec.
	InfrastructureName  string
	SavepointID         uuid.UUID
	WithSafetySavepoint bool
}

func (r SavepointRollbackRequest) Validate() error {
	if err := validateScope(r.ProjectName, r.SessionID, r.ProviderID, r.StackID); err != nil {
		return err
	}
	if r.SavepointID == uuid.Nil {
		return ErrSavepointIDRequired
	}
	return nil
}

// ItemStatus is what became of one item the rollback worked on.
type ItemStatus struct {
	// Identity names the resource in normalized parts.
	Identity model.ObjectIdentity
	Status   string
	Error    *error
	// Meta carries any extra detail the provider reports.
	Meta map[string]string
}

type SavepointRollbackStatus string

const (
	SavepointRollbackStatusSuccess SavepointRollbackStatus = "success"
	SavepointRollbackStatusFailure SavepointRollbackStatus = "failure"
	SavepointRollbackStatusWarning SavepointRollbackStatus = "warning"
)

type SavepointRollbackResponse struct {
	StartedAt                  time.Time
	CompletedAt                time.Time
	RollbackItemStatues        []ItemStatus
	SafetySavepointItemStatues []ItemStatus
	SafetySavepointID          uuid.UUID
	Status                     SavepointRollbackStatus
}
