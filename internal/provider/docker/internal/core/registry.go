package core

import (
	"errors"
	"fmt"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

var (
	ErrBackendAlreadyRegistered = errors.New("snapshot backend factory already registered")
	ErrUnknownBackend           = errors.New("unknown snapshot backend")
	ErrNoStore                  = errors.New("backend spec carries no store")
)

// SnapshotBackendRegistry resolves a volume's Backend to its factory. It
// implements dockermodel.SnapshotBackendRegistry.
type SnapshotBackendRegistry struct {
	factories map[dockermodel.SnapshotBackendKind]dockermodel.SnapshotBackendFactory
}

// NewSnapshotBackendRegistry indexes the factories by Kind; a duplicate is
// ErrBackendAlreadyRegistered.
func NewSnapshotBackendRegistry(factories ...dockermodel.SnapshotBackendFactory) (*SnapshotBackendRegistry, error) {
	indexed := make(map[dockermodel.SnapshotBackendKind]dockermodel.SnapshotBackendFactory, len(factories))
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
func (r *SnapshotBackendRegistry) New(spec dockermodel.BackendSpec) (dockermodel.VolumeSnapshotBackend, error) {
	if spec.Store == nil {
		return nil, fmt.Errorf("%w: volume %s", ErrNoStore, spec.Volume.ObjectID)
	}

	factory, ok := r.factories[spec.Volume.Backend]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackend, spec.Volume.Backend)
	}
	return factory.New(spec)
}
