package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/tarcopy"
)

func TestSnapshotBackendRegistry_New(t *testing.T) {
	session, safety := newDirStore(t), newDirStore(t)
	// Carries a mount: tarcopy resolves the one it captures through at build time,
	// so a volume with no way in never gets as far as a backend instance.
	volume := model.Volume{
		ObjectID: "volume/demo_pgdata",
		Type:     model.VolumeTypeNamed,
		Backend:  model.SnapshotBackendCopy,
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
			name: "a full-copy volume builds the copy backend",
			spec: model.BackendSpec{
				SessionStore: session,
				SafetyStore:  safety,
				Volume:       volume,
				Tier:         model.BackendTierSavepoint,
			},
			wantType: tarcopy.Snapshotter{},
		},
		{
			name: "an unregistered backend is ErrUnknownBackend",
			spec: model.BackendSpec{
				SessionStore: session,
				SafetyStore:  safety,
				Volume:       model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendUnknown},
				Tier:         model.BackendTierSavepoint,
			},
			wantErr: ErrUnknownBackend,
		},
		{
			name:    "a spec with no session store is refused",
			spec:    model.BackendSpec{SafetyStore: safety, Volume: volume, Tier: model.BackendTierSavepoint},
			wantErr: ErrNoSessionStore,
		},
		{
			name:    "a spec with no safety store is refused",
			spec:    model.BackendSpec{SessionStore: session, Volume: volume, Tier: model.BackendTierSavepoint},
			wantErr: ErrNoSafetyStore,
		},
		{
			name:    "a spec naming no tier is refused",
			spec:    model.BackendSpec{SessionStore: session, SafetyStore: safety, Volume: volume},
			wantErr: ErrNoBackendTier,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			registry, err := NewSnapshotBackendRegistry(tarcopy.NewFactory(&fakeMountStreamer{}))
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

// A spec missing a root or a tier must not reach a factory: the double carries
// no New expectation, so a dispatch fails the run where it happened.
func TestSnapshotBackendRegistry_New_ValidatesBeforeTheFactory(t *testing.T) {
	session, safety := newDirStore(t), newDirStore(t)
	volume := model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendCopy}

	tests := []struct {
		name string
		spec model.BackendSpec
	}{
		{
			name: "no session store",
			spec: model.BackendSpec{SafetyStore: safety, Volume: volume, Tier: model.BackendTierSavepoint},
		},
		{
			name: "no safety store",
			spec: model.BackendSpec{SessionStore: session, Volume: volume, Tier: model.BackendTierSavepoint},
		},
		{
			name: "no tier",
			spec: model.BackendSpec{SessionStore: session, SafetyStore: safety, Volume: volume},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			factory := &mockBackendFactory{kind: model.SnapshotBackendCopy}
			registry, err := NewSnapshotBackendRegistry(factory)
			require.NoError(t, err)

			// Act
			_, err = registry.New(tt.spec)

			// Assert
			require.Error(t, err)
			factory.AssertExpectations(t)
		})
	}
}

// Both stores and the tier reach the factory exactly as they were handed in.
func TestSnapshotBackendRegistry_New_PassesTheSpecThrough(t *testing.T) {
	// Arrange
	session, safety := newDirStore(t), newDirStore(t)
	volume := model.Volume{ObjectID: "volume/demo_db", Backend: model.SnapshotBackendCopy}

	factory := &mockBackendFactory{kind: model.SnapshotBackendCopy}
	var got model.BackendSpec
	factory.On("New", model.BackendSpec{
		SessionStore: session,
		SafetyStore:  safety,
		Volume:       volume,
		Tier:         model.BackendTierSafety,
	}).Run(func(args mock.Arguments) {
		got = args.Get(0).(model.BackendSpec)
	}).Return(&mockBackend{volume: volume}, nil).Once()
	registry, err := NewSnapshotBackendRegistry(factory)
	require.NoError(t, err)

	// Act
	_, err = registry.New(model.BackendSpec{
		SessionStore: session,
		SafetyStore:  safety,
		Volume:       volume,
		Tier:         model.BackendTierSafety,
	})

	// Assert
	require.NoError(t, err)
	factory.AssertExpectations(t)
	assert.Same(t, session, got.SessionStore)
	assert.Same(t, safety, got.SafetyStore)
	assert.Equal(t, volume, got.Volume)
	assert.Equal(t, model.BackendTierSafety, got.Tier)
}

func TestNewSnapshotBackendRegistry_DuplicateKind(t *testing.T) {
	// Arrange
	docker := &fakeMountStreamer{}

	// Act
	_, err := NewSnapshotBackendRegistry(tarcopy.NewFactory(docker), tarcopy.NewFactory(docker))

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
