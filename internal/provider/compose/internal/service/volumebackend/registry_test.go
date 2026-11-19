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

package volumebackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/volumebackend/fullcopy"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

func TestRegistry_New(t *testing.T) {
	store := composetest.NewDirStore(t)
	// Carries a mount: fullcopy resolves the one it captures through at build time,
	// so a volume with no way in never gets as far as a backend instance.
	volume := composemodel.Volume{
		ObjectID: "volume-named-demo_pgdata",
		Type:     composemodel.VolumeTypeNamed,
		Backend:  composeconfig.SnapshotBackendFullCopy,
		Named: &composemodel.VolumePropertiesNamed{
			VolumeName: "demo_pgdata",
			UsedBy:     []composemodel.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		},
	}

	tests := []struct {
		name     string
		spec     composemodel.BackendSpec
		wantType any
		wantErr  error
	}{
		{
			name:     "a full-copy volume builds the copy backend",
			spec:     composemodel.BackendSpec{Store: store, Volume: volume},
			wantType: &fullcopy.Snapshotter{},
		},
		{
			name: "an unregistered backend is ErrUnknownBackend",
			spec: composemodel.BackendSpec{
				Store:  store,
				Volume: composemodel.Volume{ObjectID: "volume-named-demo_pgdata", Backend: composeconfig.SnapshotBackendUnknown},
			},
			wantErr: ErrUnknownBackend,
		},
		{
			name:    "a spec with no store is refused",
			spec:    composemodel.BackendSpec{Volume: volume},
			wantErr: ErrNoStore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			registry, err := NewRegistry(fullcopy.NewFactory(composetest.FakeVolumeStreamer{}, fullcopy.CompressionNone))
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
func TestRegistry_New_ValidatesBeforeTheFactory(t *testing.T) {
	// Arrange
	factory := &mockBackendFactory{kind: composeconfig.SnapshotBackendFullCopy}
	registry, err := NewRegistry(factory)
	require.NoError(t, err)

	// Act
	_, err = registry.New(composemodel.BackendSpec{
		Volume: composemodel.Volume{ObjectID: "volume-named-demo_pgdata", Backend: composeconfig.SnapshotBackendFullCopy},
	})

	// Assert
	require.ErrorIs(t, err, ErrNoStore)
	factory.AssertExpectations(t)
}

// The store reaches the factory exactly as it was handed in: narrowing it here
// would decide a layout this layer must not know.
func TestRegistry_New_PassesTheSpecThrough(t *testing.T) {
	// Arrange
	store := composetest.NewDirStore(t)
	volume := composemodel.Volume{ObjectID: "volume-named-demo_db", Backend: composeconfig.SnapshotBackendFullCopy}

	factory := &mockBackendFactory{kind: composeconfig.SnapshotBackendFullCopy}
	var got composemodel.BackendSpec
	factory.On("New", composemodel.BackendSpec{Store: store, Volume: volume}).
		Run(func(args mock.Arguments) {
			got = args.Get(0).(composemodel.BackendSpec)
		}).Return(composetest.NewMockBackend(volume), nil).Once()
	registry, err := NewRegistry(factory)
	require.NoError(t, err)

	// Act
	_, err = registry.New(composemodel.BackendSpec{Store: store, Volume: volume})

	// Assert
	require.NoError(t, err)
	factory.AssertExpectations(t)
	assert.Same(t, store, got.Store)
	assert.Equal(t, volume, got.Volume)
}

func TestNew_DuplicateKind(t *testing.T) {
	// Arrange
	docker := composetest.FakeVolumeStreamer{}

	// Act
	_, err := NewRegistry(fullcopy.NewFactory(docker, fullcopy.CompressionNone), fullcopy.NewFactory(docker, fullcopy.CompressionNone))

	// Assert
	require.ErrorIs(t, err, ErrBackendAlreadyRegistered)
}

// mockBackendFactory doubles composemodel.SnapshotBackendFactory; kind answers Kind()
// directly — the registry reads it at registration, not behavior to expect on.
type mockBackendFactory struct {
	mock.Mock
	kind composeconfig.SnapshotBackendKind
}

func (f *mockBackendFactory) Kind() composeconfig.SnapshotBackendKind { return f.kind }

func (f *mockBackendFactory) New(spec composemodel.BackendSpec) (composemodel.VolumeSnapshotBackend, error) {
	args := f.Called(spec)
	backend, _ := args.Get(0).(composemodel.VolumeSnapshotBackend)
	return backend, args.Error(1)
}
