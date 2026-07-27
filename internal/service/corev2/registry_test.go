package corev2

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// sessionStore returns a store rooted at a session directory — the shape
// storefactory hands the DI root.
func sessionStore(t *testing.T) storages.Storager {
	t.Helper()
	root := filepath.Join(t.TempDir(), model.DefaultSession)
	require.NoError(t, os.MkdirAll(root, 0o755))

	store, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })

	return store
}

func TestSnapshotBackendRegistry_New(t *testing.T) {
	session := sessionStore(t)

	tests := []struct {
		name     string
		spec     BackendSpec
		wantType any
		wantErr  error
		wantMsg  string
	}{
		{
			name: "a full-copy volume mints the full-copy backend",
			spec: BackendSpec{
				SessionStore: session,
				SavepointID:  "1780335122",
				Volume:       model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendFullCopy},
			},
			wantType: stubFullCopyBackend{},
		},
		{
			name: "a cdc volume mints the cdc backend",
			spec: BackendSpec{
				SessionStore: session,
				SavepointID:  "1780335122",
				Volume:       model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendCDC},
			},
			wantType: stubCDCBackend{},
		},
		{
			name: "an unregistered backend is ErrUnknownBackend",
			spec: BackendSpec{
				SessionStore: session,
				SavepointID:  "1780335122",
				Volume:       model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendUnknown},
			},
			wantErr: ErrUnknownBackend,
		},
		{
			name: "an empty savepoint id is refused",
			spec: BackendSpec{
				SessionStore: session,
				Volume:       model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendFullCopy},
			},
			wantMsg: "invalid savepoint id",
		},
		{
			name: "a non-numeric savepoint id is refused",
			spec: BackendSpec{
				SessionStore: session,
				SavepointID:  "../1780335122",
				Volume:       model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendFullCopy},
			},
			wantMsg: "invalid savepoint id",
		},
		{
			name: "a spec with no session store is refused",
			spec: BackendSpec{
				SavepointID: "1780335122",
				Volume:      model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendFullCopy},
			},
			wantErr: ErrNoSessionStore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			registry, err := NewSnapshotBackendRegistry(StubFullCopyFactory{}, StubCDCFactory{})
			require.NoError(t, err)

			// Act
			backend, err := registry.New(tt.spec)

			// Assert
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantMsg != "":
				require.ErrorContains(t, err, tt.wantMsg)
			default:
				require.NoError(t, err)
				assert.IsType(t, tt.wantType, backend)
			}
		})
	}
}

// A spec whose savepoint id or session store is missing must not reach a factory:
// the gate stands once, in the registry, so no backend re-implements it.
func TestSnapshotBackendRegistry_New_ValidatesBeforeTheFactory(t *testing.T) {
	session := sessionStore(t)
	volume := model.Volume{ObjectID: "volume/demo_pgdata", Backend: model.SnapshotBackendFullCopy}

	tests := []struct {
		name string
		spec BackendSpec
	}{
		{name: "no savepoint id", spec: BackendSpec{SessionStore: session, Volume: volume}},
		{name: "no session store", spec: BackendSpec{SavepointID: "1780335122", Volume: volume}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			recorder := &snapshotRecorder{}
			registry, err := NewSnapshotBackendRegistry(recordingFactory{kind: model.SnapshotBackendFullCopy, recorder: recorder})
			require.NoError(t, err)

			// Act
			_, err = registry.New(tt.spec)

			// Assert
			require.Error(t, err)
			assert.Empty(t, recorder.specs(), "no factory may run on a spec the registry refuses")
		})
	}
}

// The store reaches the product rooted exactly as it was handed in: where a
// backend writes below it is the backend's own policy.
func TestSnapshotBackendRegistry_New_PassesTheStoreThrough(t *testing.T) {
	// Arrange
	session := sessionStore(t)
	registry, err := NewSnapshotBackendRegistry(StubCDCFactory{})
	require.NoError(t, err)
	backend, err := registry.New(BackendSpec{
		SessionStore: session,
		SavepointID:  "1780335122",
		Volume:       model.Volume{ObjectID: "service/app/etc/cfg", Backend: model.SnapshotBackendCDC},
	})
	require.NoError(t, err)

	// Act
	stat, err := backend.Snapshot(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.SnapshotBackendCDC, stat.Type)
	assert.Equal(t, StubCDCPayload{
		ObjectID:    "service/app/etc/cfg",
		SavepointID: "1780335122",
		SessionRoot: session.GetCwd(),
	}, stat.Payload)
}

// A full-copy backend writes under its own savepoint's directory and nowhere
// else — never at the savepoint root, where savepoint.json is reserved. Nothing
// in the type system says so, so it is pinned here over a real store.
func TestStubFullCopyFactory_New_WritesOnlyUnderItsOwnSavepoint(t *testing.T) {
	// Arrange
	session := sessionStore(t)
	registry, err := NewSnapshotBackendRegistry(StubFullCopyFactory{})
	require.NoError(t, err)
	backend, err := registry.New(BackendSpec{
		SessionStore: session,
		SavepointID:  "1780335122",
		Volume:       model.Volume{ObjectID: "service/app/etc/cfg", Backend: model.SnapshotBackendFullCopy},
	})
	require.NoError(t, err)

	// Act
	_, err = backend.Snapshot(context.Background())

	// Assert
	require.NoError(t, err)
	written, err := session.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(written))
	for _, obj := range written {
		names = append(names, obj.Name)
	}
	assert.Equal(t, []string{"1780335122/service/app/etc/cfg/data.tar"}, names)
}

func TestNewSnapshotBackendRegistry_DuplicateKind(t *testing.T) {
	// Act
	_, err := NewSnapshotBackendRegistry(StubFullCopyFactory{}, StubCDCFactory{}, StubFullCopyFactory{})

	// Assert
	require.ErrorIs(t, err, ErrBackendAlreadyRegistered)
}
