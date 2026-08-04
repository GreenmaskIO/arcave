package core

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockSavepointRepo struct{ mock.Mock }

func (m *mockSavepointRepo) List(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	records, _ := args.Get(0).([]model.Savepoint)
	return records, args.Error(1)
}

func (m *mockSavepointRepo) Save(ctx context.Context, record model.Savepoint) error {
	return m.Called(ctx, record).Error(0)
}

func (m *mockSavepointRepo) Delete(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}

// mockLocker doubles projectLocker. Release is a closure Acquire returns rather
// than a method of the seam, so it reports itself as one — MethodCalled puts it
// where testify counts it and mock.InOrder can place it.
type mockLocker struct{ mock.Mock }

func (m *mockLocker) Acquire(ctx context.Context) (func() error, error) {
	if err := m.Called(ctx).Error(0); err != nil {
		return nil, err
	}
	return func() error { return m.MethodCalled("Release").Error(0) }, nil
}

type mockInspector struct{ mock.Mock }

func (m *mockInspector) Introspect(ctx context.Context, files ...string) (model.IntrospectionResult, error) {
	args := m.Called(ctx, files)
	return args.Get(0).(model.IntrospectionResult), args.Error(1)
}

type mockComposeRunner struct{ mock.Mock }

func (m *mockComposeRunner) Stop(ctx context.Context, files ...string) error {
	return m.Called(ctx, files).Error(0)
}

func (m *mockComposeRunner) Up(ctx context.Context, waitTimeout time.Duration, files ...string) error {
	return m.Called(ctx, waitTimeout, files).Error(0)
}

type mockRegistry struct{ mock.Mock }

func (m *mockRegistry) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	args := m.Called(spec)
	backend, _ := args.Get(0).(model.VolumeSnapshotBackend)
	return backend, args.Error(1)
}

// mockBackend doubles model.VolumeSnapshotBackend; volume answers the Volume()
// accessor directly — it is identity, not behavior a test expects on.
type mockBackend struct {
	mock.Mock
	volume model.Volume
}

func (m *mockBackend) Volume() model.Volume { return m.volume }

func (m *mockBackend) Snapshot(ctx context.Context, req model.BackendSnapshotRequest) (model.SnapshotStat, error) {
	args := m.Called(ctx, req)
	stat, _ := args.Get(0).(model.SnapshotStat)
	return stat, args.Error(1)
}

func (m *mockBackend) Restore(ctx context.Context, req model.BackendRestoreRequest) error {
	return m.Called(ctx, req).Error(0)
}

func (m *mockBackend) Exists(ctx context.Context, req model.BackendExistsRequest) (bool, error) {
	args := m.Called(ctx, req)
	return args.Bool(0), args.Error(1)
}

func (m *mockBackend) Remove(ctx context.Context, req model.BackendRemoveRequest) error {
	return m.Called(ctx, req).Error(0)
}

// matchSpec matches a registry New's BackendSpec argument by the root it names
// and the volume it is for. There is one root now, so the store no longer tells
// a savepoint's instances from a rollback's undo — it is asserted because every
// backend must be bound to the store the DI root handed down, and to no other.
func matchSpec(store storages.Storager, objectID string) any {
	return mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Store == store && spec.Volume.ObjectID == objectID
	})
}

// matchValidSnapshotID matches a Snapshot request whose id passes the gate; the
// id comes off the clock, so no literal can name it.
func matchValidSnapshotID() any {
	return mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil
	})
}

// matchSafetySnapshot matches a safety snapshot's capture request: an id off the
// clock that passes the gate, carrying the label a rollback stamps. That label
// is the only thing marking the capture as an undo — the record it commits is an
// ordinary savepoint's — so it is matched rather than widened away.
func matchSafetySnapshot() any {
	return mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil &&
			req.Labels[model.SafetySnapshotLabel] == model.SafetySnapshotLabelValue
	})
}

// syncBuffer is the log sink workflow tests read back; guarded, because the
// fan-out logs from one goroutine per volume.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
