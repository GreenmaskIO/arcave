package service

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
)

// The doubles below satisfy the service package's private consumer interfaces
// (composeInspector, manifestStore, composeRunner, dockerVolumeClient, configWriter).
// They live in the service test package because those interfaces are unexported.

type mockManifest struct{ mock.Mock }

func (m *mockManifest) Load() ([]model.SavepointRecord, error) {
	args := m.Called()
	var recs []model.SavepointRecord
	if v := args.Get(0); v != nil {
		recs = v.([]model.SavepointRecord)
	}
	return recs, args.Error(1)
}

func (m *mockManifest) Save(records []model.SavepointRecord) error {
	return m.Called(records).Error(0)
}

type mockInspector struct{ mock.Mock }

func (m *mockInspector) Inspect(ctx context.Context, files ...string) (compose.Inspection, error) {
	args := m.Called(ctx, files)
	return args.Get(0).(compose.Inspection), args.Error(1)
}

type mockCompose struct{ mock.Mock }

func (m *mockCompose) Stop(ctx context.Context) error { return m.Called(ctx).Error(0) }

func (m *mockCompose) Up(ctx context.Context, waitTimeoutSec int) error {
	return m.Called(ctx, waitTimeoutSec).Error(0)
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

func (m *mockDockerVolume) Close() error { return m.Called().Error(0) }

type mockConfig struct{ mock.Mock }

func (m *mockConfig) Exists() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

func (m *mockConfig) Save(cfg config.Config) error { return m.Called(cfg).Error(0) }

func (m *mockConfig) Path() string { return m.Called().String(0) }
