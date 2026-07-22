package model

import "fmt"

// ValidSavepointID reports whether id is a savepoint id: a non-empty run of
// decimal digits, since an id is the Unix second the savepoint was created.
//
// This is arcave's first path-safety gate. A savepoint id doubles as the directory
// its data lives in, so every layer that renders one into a storage key calls
// this before it does: the savepoint repository before it mints a record key,
// and the snapshot backend on its write and destructive paths. Digits alone
// cannot name a parent (".."), an absolute path, or the store root itself, so a
// hand-planted id can never reach a syscall — the directory backend's own
// resolve gate stands behind this one, not in place of it.
func ValidSavepointID(id string) error {
	if id == "" {
		return fmt.Errorf("invalid savepoint id: must not be empty")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("invalid savepoint id %q: must be a Unix timestamp (digits only)", id)
		}
	}
	return nil
}
