package core

import (
	"errors"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/cdc"
	"github.com/greenmaskio/arcave/internal/service/backend/tarcopy"
)

var (
	ErrBackendAlreadyRegistered = errors.New("snapshot backend factory already registered")
	ErrUnknownBackend           = errors.New("unknown snapshot backend")
	ErrNoSessionStore           = errors.New("backend spec carries no session store")
	ErrNoSafetyStore            = errors.New("backend spec carries no safety-backup store")
	ErrNoBackendTier            = errors.New("backend spec carries no tier")
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

// DefaultRegistry is the registry arcave ships: every backend kind a volume can
// name. The concrete *docker.Engine arrives from the DI root.
func DefaultRegistry(docker tarcopy.MountStreamer) (*SnapshotBackendRegistry, error) {
	return NewSnapshotBackendRegistry(tarcopy.NewFactory(docker), cdc.NewFactory())
}

// New builds the backend spec.Volume.Backend names; an unregistered kind is
// ErrUnknownBackend. Both roots and the tier are required, checked before any
// factory runs. Each root reaches the factory as it was given: narrowing one
// here would decide a layout this layer must not know.
func (r *SnapshotBackendRegistry) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	if spec.SessionStore == nil {
		return nil, fmt.Errorf("%w: volume %s", ErrNoSessionStore, spec.Volume.ObjectID)
	}
	if spec.SafetyStore == nil {
		return nil, fmt.Errorf("%w: volume %s", ErrNoSafetyStore, spec.Volume.ObjectID)
	}
	// A backend branching on the zero value would read it as the savepoint tier.
	if spec.Tier == "" {
		return nil, fmt.Errorf("%w: volume %s", ErrNoBackendTier, spec.Volume.ObjectID)
	}

	factory, ok := r.factories[spec.Volume.Backend]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackend, spec.Volume.Backend)
	}
	return factory.New(spec)
}
