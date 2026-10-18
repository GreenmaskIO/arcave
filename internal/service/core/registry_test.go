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

package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
)

func TestSnapshotBackendRegistry_New(t *testing.T) {
	store := newDirStore(t)
	// Carries a mount: fullcopy resolves the one it captures through at build time,
	// so a volume with no way in never gets as far as a backend instance.
	volume := model.Volume{
		ObjectID: "volume-named-demo_pgdata",
		Type:     model.VolumeTypeNamed,
		Backend:  model.SnapshotBackendFullCopy,
		Named: &model.VolumePropertiesNamed{
			VolumeName: "demo_pgdata",
			UsedBy:     []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		},
	}

	tests := []struct {
		name     string
		spec     model.BackendSpec
		wantType any
		wantErr  error
	}{
		{
			name:     "a full-copy volume builds the copy backend",
			spec:     model.BackendSpec{Store: store, Volume: volume},
			wantType: &fullcopy.Snapshotter{},
		},
		{
			name: "an unregistered backend is ErrUnknownBackend",
			spec: model.BackendSpec{
				Store:  store,
				Volume: model.Volume{ObjectID: "volume-named-demo_pgdata", Backend: model.SnapshotBackendUnknown},
			},
			wantErr: ErrUnknownBackend,
		},
		{
			name:    "a spec with no store is refused",
			spec:    model.BackendSpec{Volume: volume},
			wantErr: ErrNoStore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(&fakeVolumeStreamer{}))
			require.NoError(t, err)

			// Act
			snapshotter, err := registry.New(tt.spec)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.IsType(t, tt.wantType, snapshotter)
		})
	}
}

// A spec missing its root must not reach a factory: the double carries no New
// expectation, so a dispatch fails the run where it happened.
func TestSnapshotBackendRegistry_New_ValidatesBeforeTheFactory(t *testing.T) {
	// Arrange
	factory := &mockBackendFactory{kind: model.SnapshotBackendFullCopy}
	registry, err := NewSnapshotBackendRegistry(factory)
	require.NoError(t, err)

	// Act
	_, err = registry.New(model.BackendSpec{
		Volume: model.Volume{ObjectID: "volume-named-demo_pgdata", Backend: model.SnapshotBackendFullCopy},
	})

	// Assert
	require.ErrorIs(t, err, ErrNoStore)
	factory.AssertExpectations(t)
}

// The store reaches the factory exactly as it was handed in: narrowing it here
// would decide a layout this layer must not know.
func TestSnapshotBackendRegistry_New_PassesTheSpecThrough(t *testing.T) {
	// Arrange
	store := newDirStore(t)
	volume := model.Volume{ObjectID: "volume-named-demo_db", Backend: model.SnapshotBackendFullCopy}

	factory := &mockBackendFactory{kind: model.SnapshotBackendFullCopy}
	var got model.BackendSpec
	factory.On("New", model.BackendSpec{Store: store, Volume: volume}).
		Run(func(args mock.Arguments) {
			got = args.Get(0).(model.BackendSpec)
		}).Return(&mockBackend{volume: volume}, nil).Once()
	registry, err := NewSnapshotBackendRegistry(factory)
	require.NoError(t, err)

	// Act
	_, err = registry.New(model.BackendSpec{Store: store, Volume: volume})

	// Assert
	require.NoError(t, err)
	factory.AssertExpectations(t)
	assert.Same(t, store, got.Store)
	assert.Equal(t, volume, got.Volume)
}

func TestNewSnapshotBackendRegistry_DuplicateKind(t *testing.T) {
	// Arrange
	docker := &fakeVolumeStreamer{}

	// Act
	_, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(docker), fullcopy.NewFactory(docker))

	// Assert
	require.ErrorIs(t, err, ErrBackendAlreadyRegistered)
}

// mockBackendFactory doubles model.SnapshotBackendFactory; kind answers Kind()
// directly — the registry reads it at registration, not behavior to expect on.
type mockBackendFactory struct {
	mock.Mock
	kind model.SnapshotBackendKind
}

func (f *mockBackendFactory) Kind() model.SnapshotBackendKind { return f.kind }

func (f *mockBackendFactory) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	args := f.Called(spec)
	backend, _ := args.Get(0).(model.VolumeSnapshotBackend)
	return backend, args.Error(1)
}
