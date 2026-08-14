package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/fullcopy"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

func TestSnapshotBackendRegistry_New(t *testing.T) {
	store := newDirStore(t)
	// Carries a mount: fullcopy resolves the one it captures through at build time,
	// so a volume with no way in never gets as far as a backend instance.
	volume := dockermodel.Volume{
		ObjectID: "volume-named-demo_pgdata",
		Type:     dockermodel.VolumeTypeNamed,
		Backend:  dockermodel.SnapshotBackendCopy,
		Named: &dockermodel.VolumePropertiesNamed{
			VolumeName: "demo_pgdata",
			UsedBy:     []dockermodel.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		},
	}

	tests := []struct {
		name     string
		spec     dockermodel.BackendSpec
		wantType any
		wantErr  error
	}{
		{
			name:     "a full-copy volume builds the copy backend",
			spec:     dockermodel.BackendSpec{Store: store, Volume: volume},
			wantType: &fullcopy.Snapshotter{},
		},
		{
			name: "an unregistered backend is ErrUnknownBackend",
			spec: dockermodel.BackendSpec{
				Store:  store,
				Volume: dockermodel.Volume{ObjectID: "volume-named-demo_pgdata", Backend: dockermodel.SnapshotBackendUnknown},
			},
			wantErr: ErrUnknownBackend,
		},
		{
			name:    "a spec with no store is refused",
			spec:    dockermodel.BackendSpec{Volume: volume},
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
	factory := &mockBackendFactory{kind: dockermodel.SnapshotBackendCopy}
	registry, err := NewSnapshotBackendRegistry(factory)
	require.NoError(t, err)

	// Act
	_, err = registry.New(dockermodel.BackendSpec{
		Volume: dockermodel.Volume{ObjectID: "volume-named-demo_pgdata", Backend: dockermodel.SnapshotBackendCopy},
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
	volume := dockermodel.Volume{ObjectID: "volume-named-demo_db", Backend: dockermodel.SnapshotBackendCopy}

	factory := &mockBackendFactory{kind: dockermodel.SnapshotBackendCopy}
	var got dockermodel.BackendSpec
	factory.On("New", dockermodel.BackendSpec{Store: store, Volume: volume}).
		Run(func(args mock.Arguments) {
			got = args.Get(0).(dockermodel.BackendSpec)
		}).Return(&mockBackend{volume: volume}, nil).Once()
	registry, err := NewSnapshotBackendRegistry(factory)
	require.NoError(t, err)

	// Act
	_, err = registry.New(dockermodel.BackendSpec{Store: store, Volume: volume})

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

// mockBackendFactory doubles dockermodel.SnapshotBackendFactory; kind answers Kind()
// directly — the registry reads it at registration, not behavior to expect on.
type mockBackendFactory struct {
	mock.Mock
	kind dockermodel.SnapshotBackendKind
}

func (f *mockBackendFactory) Kind() dockermodel.SnapshotBackendKind { return f.kind }

func (f *mockBackendFactory) New(spec dockermodel.BackendSpec) (dockermodel.VolumeSnapshotBackend, error) {
	args := f.Called(spec)
	backend, _ := args.Get(0).(dockermodel.VolumeSnapshotBackend)
	return backend, args.Error(1)
}
