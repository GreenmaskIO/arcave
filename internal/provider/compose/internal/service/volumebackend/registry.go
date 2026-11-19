// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package volumebackend resolves a volume's backend to the factory that builds it; the backends
// live beneath it, one package per kind.
package volumebackend

import (
	"errors"
	"fmt"

	"github.com/greenmaskio/storages"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

var (
	ErrBackendAlreadyRegistered = errors.New("snapshot backend factory already registered")
	ErrUnknownBackend           = errors.New("unknown snapshot backend")
	ErrNoStore                  = errors.New("backend spec carries no store")
)

// Registry resolves a volume's Backend to its factory. It
// implements composemodel.SnapshotBackendRegistry.
type Registry struct {
	factories map[composeconfig.SnapshotBackendKind]composemodel.SnapshotBackendFactory
}

// NewRegistry indexes the factories by Kind; a duplicate is
// ErrBackendAlreadyRegistered.
func NewRegistry(factories ...composemodel.SnapshotBackendFactory) (*Registry, error) {
	indexed := make(map[composeconfig.SnapshotBackendKind]composemodel.SnapshotBackendFactory, len(factories))
	for _, factory := range factories {
		if _, ok := indexed[factory.Kind()]; ok {
			return nil, fmt.Errorf("%w: %s", ErrBackendAlreadyRegistered, factory.Kind())
		}
		indexed[factory.Kind()] = factory
	}
	return &Registry{factories: indexed}, nil
}

// New builds the backend spec.Volume.Backend names; an unregistered kind is
// ErrUnknownBackend. The root is required, checked before any factory runs, and
// reaches the factory as it was given: narrowing it here would decide a layout
// this layer must not know.
func (s *Registry) New(spec composemodel.BackendSpec) (composemodel.VolumeSnapshotBackend, error) {
	if spec.Store == nil {
		return nil, fmt.Errorf("%w: volume %s", ErrNoStore, spec.Volume.ObjectID)
	}

	factory, ok := s.factories[spec.Volume.Backend]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackend, spec.Volume.Backend)
	}
	return factory.New(spec)
}

// NewBackends builds one backend per volume through registry, all rooted at store, in
// input order. All up front: an unregistered backend fails before any byte moves.
func NewBackends(
	registry composemodel.SnapshotBackendRegistry, store storages.Storager, volumes []composemodel.Volume,
) ([]composemodel.VolumeSnapshotBackend, error) {
	backends := make([]composemodel.VolumeSnapshotBackend, len(volumes))
	for i, volume := range volumes {
		backend, err := registry.New(composemodel.BackendSpec{
			Store:  store,
			Volume: volume,
		})
		if err != nil {
			return nil, fmt.Errorf("building %s backend for volume %s: %w", volume.Backend, volume.ObjectID, err)
		}
		backends[i] = backend
	}
	return backends, nil
}
