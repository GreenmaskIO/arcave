package api

import (
	"github.com/google/uuid"
)

// SessionStatusRequest addresses one session.
type SessionStatusRequest struct {
	ProjectName string
	SessionID   uuid.UUID
}

func (r SessionStatusRequest) Validate() error {
	if r.ProjectName == "" {
		return ErrProjectNameRequired
	}
	if r.SessionID == uuid.Nil {
		return ErrSessionIDRequired
	}
	return nil
}
