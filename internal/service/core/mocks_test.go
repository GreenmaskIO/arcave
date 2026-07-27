package core

import (
	"context"
	"io"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// The doubles below satisfy the service package's private consumer interfaces
// (composeInspector, savepointRepository, projectLocker, composeRunner, configWriter)
// plus the snapshot backend's DockerVolume. They live in the service test package
// because those interfaces are unexported; mockDockerVolume drives a real
// snapshot.CopyBackend over a real directory Storager so orchestration is
// exercised end to end without a daemon.

type mockSavepointRepo struct{ mock.Mock }

// The repository mock drops ctx from its expectations: no test asserts on it, and
// carrying it would force a mock.Anything into every On("List") call.
func (m *mockSavepointRepo) List(context.Context) ([]model.Savepoint, error) {
	args := m.Called()
	var recs []model.Savepoint
	if v := args.Get(0); v != nil {
		recs = v.([]model.Savepoint)
	}
	return recs, args.Error(1)
}

func (m *mockSavepointRepo) Save(_ context.Context, record model.Savepoint) error {
	return m.Called(record).Error(0)
}

func (m *mockSavepointRepo) Delete(_ context.Context, id string) error {
	return m.Called(id).Error(0)
}

// mockLocker is a projectLocker double. It counts releases rather than expecting
// them, because release is a closure Acquire returns rather than a method the
// mock can match on — and "the lock was given back" is what a test asserts.
type mockLocker struct {
	mock.Mock
	released int
}

func (m *mockLocker) Acquire(context.Context) (func() error, error) {
	if err := m.Called().Error(0); err != nil {
		return nil, err
	}
	return func() error {
		m.released++
		return nil
	}, nil
}

// freeLocker returns a locker that always grants the project, for the tests whose
// subject is not the lock.
func freeLocker() *mockLocker {
	l := &mockLocker{}
	l.On("Acquire").Return(nil)
	return l
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

type mockDockerVolume struct{ mock.Mock }

// OpenMount returns the tar stream configured for the call. The Return value is
// an io.ReadCloser (or nil alongside an error).
func (m *mockDockerVolume) OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error) {
	args := m.Called(ctx, project, service, target)
	var rc io.ReadCloser
	if v := args.Get(0); v != nil {
		rc = v.(io.ReadCloser)
	}
	return rc, args.Error(1)
}

// WriteMount drains r and forwards its content as the fifth expectation argument,
// so tests distinguish a savepoint restore from a backup revert by the bytes that
// flow in (which store minted the source stream) rather than by a file path.
func (m *mockDockerVolume) WriteMount(ctx context.Context, project, service, target string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return m.Called(ctx, project, service, target, string(data)).Error(0)
}

// mockBackend is a snapshotBackend double for the flows that only need the
// backend's contract, not a real archive on disk (removal, pre-flight). The
// data-moving flows drive a real snapshot.CopyBackend over mockDockerVolume
// instead, so the tar round-trip stays covered.
type mockBackend struct{ mock.Mock }

func (m *mockBackend) Snapshot(
	ctx context.Context, savepointID, composeProject string, volumes []model.Volume,
) ([]model.SnapshotStat, error) {
	args := m.Called(ctx, savepointID, composeProject, volumes)
	stats, _ := args.Get(0).([]model.SnapshotStat)
	return stats, args.Error(1)
}

func (m *mockBackend) Restore(ctx context.Context, savepointID, composeProject string, volumes []model.Volume) error {
	return m.Called(ctx, savepointID, composeProject, volumes).Error(0)
}

func (m *mockBackend) Missing(ctx context.Context, savepointID string, volumes []model.Volume) ([]model.Volume, error) {
	args := m.Called(ctx, savepointID, volumes)
	missing, _ := args.Get(0).([]model.Volume)
	return missing, args.Error(1)
}

func (m *mockBackend) Remove(ctx context.Context, savepointID string) error {
	return m.Called(ctx, savepointID).Error(0)
}

type mockConfig struct{ mock.Mock }

func (m *mockConfig) Exists() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

func (m *mockConfig) Save(cfg config.Config) error { return m.Called(cfg).Error(0) }

func (m *mockConfig) Path() string { return m.Called().String(0) }

// mockCatalog is the recommendationCatalog double. It drops ctx from its
// expectations because the seam takes none — the catalog is local bytes.
type mockCatalog struct{ mock.Mock }

func (m *mockCatalog) List() ([]model.ARCEntry, error) {
	args := m.Called()
	entries, _ := args.Get(0).([]model.ARCEntry)
	return entries, args.Error(1)
}

// mockIntrospector is the composeIntrospector double. It takes the variadic
// files as a single []string so an expectation can name them.
type mockIntrospector struct{ mock.Mock }

func (m *mockIntrospector) Introspect(ctx context.Context, files ...string) (model.IntrospectionResult, error) {
	args := m.Called(ctx, files)
	result, _ := args.Get(0).(model.IntrospectionResult)
	return result, args.Error(1)
}
