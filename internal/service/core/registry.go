package core

import (
	"errors"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	ErrBackendAlreadyRegistered = errors.New("snapshot backend factory already registered")
	ErrUnknownBackend           = errors.New("unknown snapshot backend")
	ErrNoStore                  = errors.New("backend spec carries no store")
)

// SnapshotBackendRegistry resolves a volume's Backend to its factory. It
// implements model.SnapshotBackendRegistry.
type SnapshotBackendRegistry struct {
	factories map[model.SnapshotBackendKind]model.SnapshotBackendFactory
}

// NewSnapshotBackendRegistry indexes the factories by Kind; a duplicate is
// ErrBackendAlreadyRegistered.
func NewSnapshotBackendRegistry(factories ...model.SnapshotBackendFactory) (*SnapshotBackendRegistry, error) {
	indexed := make(map[model.SnapshotBackendKind]model.SnapshotBackendFactory, len(factories))
	for _, factory := range factories {
		if _, ok := indexed[factory.Kind()]; ok {
			return nil, fmt.Errorf("%w: %s", ErrBackendAlreadyRegistered, factory.Kind())
		}
		indexed[factory.Kind()] = factory
	}
	return &SnapshotBackendRegistry{factories: indexed}, nil
}

// New builds the backend spec.Volume.Backend names; an unregistered kind is
// ErrUnknownBackend. The root is required, checked before any factory runs, and
// reaches the factory as it was given: narrowing it here would decide a layout
// this layer must not know.
func (r *SnapshotBackendRegistry) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	if spec.Store == nil {
		return nil, fmt.Errorf("%w: volume %s", ErrNoStore, spec.Volume.ObjectID)
	}

	factory, ok := r.factories[spec.Volume.Backend]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackend, spec.Volume.Backend)
	}
	return factory.New(spec)
}
