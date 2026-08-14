package api

import (
	"time"

	"github.com/google/uuid"
)

// SessionCreateRequest opens a session inside a project, either from nothing or
// inherited from an existing one.
type SessionCreateRequest struct {
	ProjectName string
	// FromSessionID names the session the new one inherits from; nil starts fresh.
	FromSessionID *uuid.UUID
}

func (r SessionCreateRequest) Validate() error {
	if r.ProjectName == "" {
		return ErrProjectNameRequired
	}
	return nil
}

type SessionCreateResponse struct {
	ProjectName string
	SessionID   uuid.UUID
	CreatedAt   time.Time
	From        *uuid.UUID
}
