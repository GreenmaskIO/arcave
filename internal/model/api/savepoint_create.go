package api

import (
	"github.com/google/uuid"
)

type SavepointCreateRequest struct {
	ProjectName string
	SessionID   uuid.UUID
	// ProviderID is a provider registered in the control plane.
	ProviderID string
	// StackID names the spec to capture.
	StackID string

	// Name is the savepoint's optional human name.
	Name        string
	Description string
	// Labels are free-form; no provider reads a particular key.
	Labels map[string]string
}

func (r SavepointCreateRequest) Validate() error {
	return validateScope(r.ProjectName, r.SessionID, r.ProviderID, r.StackID)
}

type SavepointCreateResponse struct {
	ProviderID   string
	StackName    string
	SavepointID  uuid.UUID
	ProviderType string
}
