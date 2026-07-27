package model

import (
	"fmt"

	"github.com/google/uuid"
)

// ValidSessionID reports whether id names a session: DefaultSession, or a UUID
// in its canonical form.
//
// ValidSavepointID's twin one level up, and an allowlist for the same reason: the
// id is rendered into a store root, and a list of what a directory name must not
// be is never provably complete, while two known shapes are. The canonical
// spelling is required, since uuid.Parse also takes braces, a urn:uuid: prefix
// and upper case.
func ValidSessionID(id string) error {
	if id == DefaultSession {
		return nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("invalid session id %q: must be %q or a canonical UUID", id, DefaultSession)
	}
	return nil
}
