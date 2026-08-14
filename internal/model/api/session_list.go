package api

import (
	"time"

	"github.com/google/uuid"
)

type SessionListRequest struct {
	ProjectName string
}

func (r SessionListRequest) Validate() error {
	if r.ProjectName == "" {
		return ErrProjectNameRequired
	}
	return nil
}

type SessionListItem struct {
	SessionID uuid.UUID
	CreatedAt time.Time
	From      *uuid.UUID
}

type SessionListResponse struct {
	ProjectName string
	Sessions    []SessionListItem
}
