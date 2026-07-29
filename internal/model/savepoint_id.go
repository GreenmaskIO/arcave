package model

import "fmt"

// SavepointID addresses one capture — a savepoint's or a safety backup's — and
// doubles as the directory it lives in. It carries its own gate, so a value that
// reached a storage key was validated.
type SavepointID string

// Validate accepts a non-empty run of decimal digits, since an id is the Unix
// second the savepoint was created.
//
// This is arcave's first path-safety gate: digits alone cannot name a parent, an
// absolute path or the store root. Every layer that renders an id into a storage
// key calls it first.
func (m SavepointID) Validate() error {
	if m == "" {
		return fmt.Errorf("%w: must not be empty", ErrInvalidSavepointID)
	}
	for _, r := range m {
		if r < '0' || r > '9' {
			return fmt.Errorf("%w %q: must be a Unix timestamp (digits only)", ErrInvalidSavepointID, string(m))
		}
	}
	return nil
}

// String renders the id as the path segment it becomes.
func (m SavepointID) String() string { return string(m) }

// SavepointIDs converts adapter input — CLI args, a JSON array — into ids. It
// validates nothing: the layer that renders one into a storage key gates it.
func SavepointIDs(ids []string) []SavepointID {
	out := make([]SavepointID, len(ids))
	for i, id := range ids {
		out[i] = SavepointID(id)
	}
	return out
}
