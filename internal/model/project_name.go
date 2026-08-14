package model

import (
	"fmt"
	"strings"
)

// ProjectName names the storage tenant every store root is scoped by — the
// `project.name` of .arcave/config.yaml. It carries its own gate, so a value
// that reached a store was validated.
//
// ResourceName's twin, and deliberately a separate type: the tenant and the
// provider's own unit are different concepts that happen to share a charset, and
// one segment must never be passed where the other is expected.
type ProjectName string

// Validate accepts a non-empty name of the charset compose imposes on project
// names: it starts with [a-z0-9] and continues with [a-z0-9_-].
//
// An allowlist, like SessionID's and SavepointID's, and for the same reason: the
// name is rendered into a store root, and a list of what a path segment must not
// be is never provably complete.
func (m ProjectName) Validate() error {
	if err := validateNameSegment(string(m)); err != nil {
		return fmt.Errorf("invalid project name %q: %w", string(m), err)
	}
	return nil
}

// String renders the name as the path segment it becomes.
func (m ProjectName) String() string { return string(m) }

// NormalizeProjectName coerces s into the charset Validate accepts — the same
// normalization compose-go applies to a directory name: lower case, characters
// outside the charset dropped, leading "-"/"_" trimmed. It is spelled out here
// rather than borrowed, since model imports nothing.
//
// The result may be empty (a name of nothing but non-ASCII letters), which
// Validate is what reports.
func NormalizeProjectName(s string) ProjectName {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if isNameRune(r) {
			b.WriteRune(r)
		}
	}
	return ProjectName(strings.TrimLeft(b.String(), "-_"))
}

// validateNameSegment holds the charset rule ProjectName and ResourceName share.
// The message names the condition alone; each type wraps it with its own
// subject.
func validateNameSegment(s string) error {
	if s == "" {
		return fmt.Errorf("must not be empty")
	}
	for i, r := range s {
		if !isNameRune(r) || (i == 0 && (r == '-' || r == '_')) {
			return fmt.Errorf("must start with [a-z0-9] and continue with [a-z0-9_-]")
		}
	}
	return nil
}

// isNameRune reports whether r is in the [a-z0-9_-] charset.
func isNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}
