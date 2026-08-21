package model

import "fmt"

// StackName names the docker compose project a store root is scoped by, given per
// invocation (-p/--project-name). Distinct from ProjectName, the storage tenant.
type StackName string

// Validate accepts a non-empty name of the charset compose imposes on project
// names: it starts with [a-z0-9] and continues with [a-z0-9_-]. Compose
// normalizes the name it resolves, so a value failing here names a stack arcave
// could not have addressed anyway.
func (m StackName) Validate() error {
	if err := validateNameSegment(string(m)); err != nil {
		return fmt.Errorf("invalid stack name %q: %w", string(m), err)
	}
	return nil
}

// String renders the name as the path segment it becomes.
func (m StackName) String() string { return string(m) }
