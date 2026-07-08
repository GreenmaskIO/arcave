package cli

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
)

// capturePrinter records everything a handler prints, so a test can assert on
// the exact value marshalled without inspecting stdout. Its err lets a test
// simulate a printer failure.
type capturePrinter struct {
	got []any
	err error
}

func (p *capturePrinter) Print(v any) error {
	p.got = append(p.got, v)
	return p.err
}

// mockService is a testify double satisfying every per-handler service
// interface in this package; each test sets expectations only on the method the
// handler under test calls.
type mockService struct{ mock.Mock }

func (m *mockService) Init(ctx context.Context, cfg config.Config, override bool) (string, string, error) {
	args := m.Called(ctx, cfg, override)
	return args.String(0), args.String(1), args.Error(2)
}

func (m *mockService) CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(model.Savepoint), args.Error(1)
}

func (m *mockService) ListSavepoints(context.Context) ([]model.Savepoint, error) {
	args := m.Called()
	var sps []model.Savepoint
	if v := args.Get(0); v != nil {
		sps = v.([]model.Savepoint)
	}
	return sps, args.Error(1)
}

func (m *mockService) RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(model.RemoveResult), args.Error(1)
}

func (m *mockService) Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(model.RollbackResult), args.Error(1)
}
