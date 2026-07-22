package cli

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// capturePrinter is a testify double for the printer seam: a test sets a Print
// expectation to simulate a printer failure, and reads printed() to assert on
// the exact value marshalled without inspecting stdout.
type capturePrinter struct {
	mock.Mock
}

func (p *capturePrinter) Print(v any) error {
	args := p.Called(v)
	return args.Error(0)
}

// printed returns every value handed to Print, in call order, so a test can
// assert on the exact payload marshalled. It is nil when nothing was printed.
func (p *capturePrinter) printed() []any {
	var vs []any
	for _, c := range p.Calls {
		if c.Method == "Print" {
			vs = append(vs, c.Arguments[0])
		}
	}
	return vs
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
