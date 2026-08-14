package model

import (
	"github.com/greenmaskio/storages"
)

// BackendSpec binds a snapshot backend to one volume and the root it may write
// under. It carries no snapshot id — that is the request's, so one instance
// serves a whole workflow — and no kind: Volume.Backend is what the registry
// dispatches on.
type BackendSpec struct {
	// Store is the root the caller hands over: a savepoint's tree for a savepoint,
	// a rollback's undo tree for a safety snapshot. Which of them it is, is the
	// caller's call — the backend only knows it is a root, and what it lays out
	// below it is its own policy. A backend whose data lives elsewhere (cdc, whose
	// repository outlives every session) may ignore it.
	Store  storages.Storager
	Volume Volume
}
