package model

import "fmt"

// StackName names the docker compose project a store root is scoped by — the
// name compose itself resolves from the project's files, resolved at wiring
// time. It carries its own gate, so a value that reached a store was validated.
//
// A type of its own beside ProjectName: the storage tenant and the compose stack
// are different concepts, and conflating them is exactly what a shared string
// type would invite.
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
