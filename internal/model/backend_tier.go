package model

// BackendTier is which of a workflow's two capture sets a backend instance
// serves. It is picked once, when the instance is built, so no call site can
// mix them. The zero value names no tier and is refused by the registry.
type BackendTier string

const (
	// BackendTierSavepoint is the tier a savepoint's captures live in.
	BackendTierSavepoint BackendTier = "savepoint"
	// BackendTierSafety is the tier a rollback's undo lives in. What that means is
	// the backend's own policy: a distinct root for one, a parent-referencing
	// snapshot in the same repository for another.
	BackendTierSafety BackendTier = "safety"
)
