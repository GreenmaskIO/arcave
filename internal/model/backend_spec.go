package model

import (
	"fmt"

	"github.com/greenmaskio/storages"
)

// BackendSpec binds a snapshot backend to one volume, one tier, and the roots it
// may write under. It carries no snapshot id — that is the request's, so one
// instance serves a whole workflow — and no kind: Volume.Backend is what the
// registry dispatches on.
type BackendSpec struct {
	// SessionStore is rooted at the session, not at a savepoint: the layout below
	// it is the backend's own policy.
	SessionStore storages.Storager
	// SafetyStore is rooted at the safety-backup tree. A backend may assume only
	// that it is a distinct root.
	SafetyStore storages.Storager
	Volume      Volume
	// Tier is what the instance is bound to. Both roots are handed over whatever
	// it says: what a tier means is the backend's policy.
	Tier BackendTier
}

// TierStore reads the tier as naming one of the two roots — the policy of every
// backend that keeps its captures in the tree it was handed. It is a method
// rather than a switch in each backend because it reads nothing but this spec's
// own fields. A backend whose tier decides something other than a root (cdc,
// whose repository lives outside both trees) never calls it.
func (s BackendSpec) TierStore() (storages.Storager, error) {
	switch s.Tier {
	case BackendTierSavepoint:
		return s.SessionStore, nil
	case BackendTierSafety:
		return s.SafetyStore, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownBackendTier, s.Tier)
	}
}
