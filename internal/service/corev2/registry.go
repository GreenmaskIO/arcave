package corev2

import (
	"context"
	"errors"
	"fmt"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	ErrBackendAlreadyRegistered = errors.New("snapshot backend factory already registered")
	ErrUnknownBackend           = errors.New("unknown snapshot backend")
	ErrNoSessionStore           = errors.New("backend spec carries no session store")
)

// BackendSpec is everything a backend needs to bind itself: which volume, which
// savepoint, and the store it renders its own layout into. It is exported because
// factories outside this package name it in their signatures.
//
// It carries no kind of its own — Volume.Backend is what the registry dispatches
// on, and a second copy would be a second source of truth. A backend needing a
// store this one cannot reach gets its own field here.
type BackendSpec struct {
	// SessionStore is rooted at the session, *not* at the savepoint: the layout
	// below it is the backend's own policy.
	SessionStore storages.Storager
	SavepointID  string
	Volume       model.Volume
}

// VolumeSnapshotBackend is bound to one (volume, savepoint) pair: an instance is
// never reused across savepoints.
type VolumeSnapshotBackend interface {
	Snapshot(ctx context.Context) (model.SnapshotStat, error)
	// Restore/Missing/Remove — next steps, on this same interface.
}

// SnapshotBackendFactory mints a backend bound to one spec for the Kind it names.
type SnapshotBackendFactory interface {
	Kind() model.SnapshotBackend
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}

// SnapshotBackendRegistry resolves a volume's Backend to its factory.
type SnapshotBackendRegistry struct {
	factories map[model.SnapshotBackend]SnapshotBackendFactory
}

// NewSnapshotBackendRegistry indexes the factories by Kind; a duplicate is
// ErrBackendAlreadyRegistered.
func NewSnapshotBackendRegistry(factories ...SnapshotBackendFactory) (*SnapshotBackendRegistry, error) {
	indexed := make(map[model.SnapshotBackend]SnapshotBackendFactory, len(factories))
	for _, factory := range factories {
		if _, ok := indexed[factory.Kind()]; ok {
			return nil, fmt.Errorf("%w: %s", ErrBackendAlreadyRegistered, factory.Kind())
		}
		indexed[factory.Kind()] = factory
	}
	return &SnapshotBackendRegistry{factories: indexed}, nil
}

// New mints the backend spec.Volume.Backend names, bound to spec. An
// unregistered kind is ErrUnknownBackend.
//
// The spec is validated here, before any factory runs, so no backend
// re-implements the gate. The store reaches the factory rooted exactly as it was
// given: narrowing it here would decide the layout in a layer that must not know
// it.
func (r *SnapshotBackendRegistry) New(spec BackendSpec) (VolumeSnapshotBackend, error) {
	if err := model.ValidSavepointID(spec.SavepointID); err != nil {
		return nil, err
	}
	if spec.SessionStore == nil {
		return nil, fmt.Errorf("%w: volume %s", ErrNoSessionStore, spec.Volume.ObjectID)
	}

	factory, ok := r.factories[spec.Volume.Backend]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackend, spec.Volume.Backend)
	}
	return factory.New(spec)
}
