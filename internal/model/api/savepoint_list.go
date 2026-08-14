package api

import (
	"time"

	"github.com/google/uuid"
)

type SavepointListRequest struct {
	ProjectName string
	ProviderID  string
	StackID     string
	Limit       int
	Offset      int
	// IncludeSafetySavepoints also lists the savepoints a rollback takes of the
	// live state before it applies one.
	IncludeSafetySavepoints bool
}

func (r SavepointListRequest) Validate() error {
	return validateStackScope(r.ProjectName, r.ProviderID, r.StackID)
}

type SavepointRecord struct {
	SavepointID         uuid.UUID
	CreatedAt           time.Time
	CreatedAtTimelineID int
	Name                string
	Description         string
	Labels              map[string]string
}

type SavepointListResponse struct {
	SessionID    uuid.UUID
	ProviderID   string
	StackName    string
	ProviderType string
	CurrentPage  int
	TotalPages   int
	PageSize     int
	Records      []SavepointRecord
}
