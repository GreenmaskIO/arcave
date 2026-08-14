// Package api holds the request and response types the CLI and the control
// plane speak, one file per operation.
package api

import (
	"github.com/google/uuid"
)

// validateScope refuses a request that does not name the whole chain it
// addresses: project → session → (provider, stack).
func validateScope(projectName string, sessionID uuid.UUID, providerID, stackID string) error {
	if projectName == "" {
		return ErrProjectNameRequired
	}
	if sessionID == uuid.Nil {
		return ErrSessionIDRequired
	}
	return validateStackScope(projectName, providerID, stackID)
}

// validateStackScope is the same chain without the session.
func validateStackScope(projectName, providerID, stackID string) error {
	switch {
	case projectName == "":
		return ErrProjectNameRequired
	case providerID == "":
		return ErrProviderIDRequired
	case stackID == "":
		return ErrStackIDRequired
	}
	return nil
}
