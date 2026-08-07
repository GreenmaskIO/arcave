package model

import "fmt"

// StoreNamespace names one session-scoped store a provider keeps beside the
// savepoint trees — `restic` for the per-volume repositories, `oci` for image
// layers. It is the argument of provider.Stores.Session, and it carries its own
// gate for the same reason every other segment type does: the name becomes a
// path segment, and the caller is a provider arcave does not control.
//
// ProjectName's and StackName's third sibling rather than an alias of either: a
// namespace is not a tenant and not a stack, and one must never be passed where
// another is expected.
type StoreNamespace string

// Validate accepts a non-empty namespace of the [a-z0-9_-] charset, starting
// with [a-z0-9]. An allowlist, like every other segment gate — a list of what a
// segment must not be is never provably complete.
//
// It deliberately admits no "/": a namespace is one segment. A provider that
// wants a tree below it takes a SubStorage of what it was handed, which is the
// store library's own guarded operation.
func (m StoreNamespace) Validate() error {
	if err := validateNameSegment(string(m)); err != nil {
		return fmt.Errorf("invalid store namespace %q: %w", string(m), err)
	}
	return nil
}

// String renders the namespace as the path segment it becomes.
func (m StoreNamespace) String() string { return string(m) }
