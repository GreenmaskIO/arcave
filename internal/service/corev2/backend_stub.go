package corev2

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

// Stub backends prove the registry → backend integration until the real
// full-copy and cdc implementations land. Each renders its own layout below the
// root the spec hands it, which is the property under test.

// stubArchiveName stands in for the tar stream the real backend writes.
const stubArchiveName = "data.tar"

// StubFullCopyPayload is the stub full-copy backend's SnapshotStat payload. It
// reports the root verbatim so a test can see which store reached the backend.
type StubFullCopyPayload struct {
	ObjectID    string
	SavepointID string
	SessionRoot string
}

// StubCDCPayload is the stub cdc backend's SnapshotStat payload.
type StubCDCPayload struct {
	ObjectID    string
	SavepointID string
	SessionRoot string
}

// StubFullCopyFactory mints stub full-copy backends.
type StubFullCopyFactory struct{}

func (StubFullCopyFactory) Kind() model.SnapshotBackend { return model.SnapshotBackendFullCopy }

// New narrows the session store to <savepointID>/<objectID>/, which is the whole
// of full-copy's layout policy.
func (StubFullCopyFactory) New(spec BackendSpec) (VolumeSnapshotBackend, error) {
	store, err := spec.SessionStore.SubStorage(path.Join(spec.SavepointID, spec.Volume.ObjectID), true)
	if err != nil {
		return nil, fmt.Errorf("re-root session store at savepoint %s: %w", spec.SavepointID, err)
	}
	return stubFullCopyBackend{spec: spec, store: store}, nil
}

type stubFullCopyBackend struct {
	spec  BackendSpec
	store storages.Storager
}

func (b stubFullCopyBackend) Snapshot(ctx context.Context) (model.SnapshotStat, error) {
	if err := b.store.PutObject(ctx, stubArchiveName, strings.NewReader(b.spec.Volume.ObjectID)); err != nil {
		return model.SnapshotStat{}, fmt.Errorf("write stub archive for volume %s: %w", b.spec.Volume.ObjectID, err)
	}
	return model.SnapshotStat{
		Volume: b.spec.Volume,
		Type:   model.SnapshotBackendFullCopy,
		Payload: StubFullCopyPayload{
			ObjectID:    b.spec.Volume.ObjectID,
			SavepointID: b.spec.SavepointID,
			SessionRoot: b.spec.SessionStore.GetCwd(),
		},
	}, nil
}

// StubCDCFactory mints stub cdc backends.
type StubCDCFactory struct{}

func (StubCDCFactory) Kind() model.SnapshotBackend { return model.SnapshotBackendCDC }

// New binds the spec and narrows nothing: the real cdc backend keeps its
// repository in the tree that outlives the session, and that store is not a spec
// field until it has a reader.
func (StubCDCFactory) New(spec BackendSpec) (VolumeSnapshotBackend, error) {
	return stubCDCBackend{spec: spec}, nil
}

type stubCDCBackend struct {
	spec BackendSpec
}

func (b stubCDCBackend) Snapshot(context.Context) (model.SnapshotStat, error) {
	return model.SnapshotStat{
		Volume: b.spec.Volume,
		Type:   model.SnapshotBackendCDC,
		Payload: StubCDCPayload{
			ObjectID:    b.spec.Volume.ObjectID,
			SavepointID: b.spec.SavepointID,
			SessionRoot: b.spec.SessionStore.GetCwd(),
		},
	}, nil
}
