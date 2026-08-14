package model

import "fmt"

// ResourceName names the unit a provider drives — the compose project for a
// stack provider, the sandbox for a sandbox provider — resolved at wiring time.
// It is the last segment of a store root, and it carries its own gate, so a
// value that reached a store was validated.
//
// A type of its own beside ProjectName: the storage tenant and the provider's
// own unit are different concepts, and conflating them is exactly what a shared
// string type would invite.
type ResourceName string

// Validate accepts a non-empty name of the charset compose imposes on project
// names: it starts with [a-z0-9] and continues with [a-z0-9_-]. Compose
// normalizes the name it resolves, so a value failing here names a stack arcave
// could not have addressed anyway.
func (m ResourceName) Validate() error {
	if err := validateNameSegment(string(m)); err != nil {
		return fmt.Errorf("invalid resource name %q: %w", string(m), err)
	}
	return nil
}

// String renders the name as the path segment it becomes.
func (m ResourceName) String() string { return string(m) }
