package cli

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

var (
	testSessionID   = uuid.MustParse("6f8f2f0e-1a5c-4a2e-9d4b-0f1f2a3b4c5d")
	testSavepointID = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	errAPI          = errors.New("control plane unreachable")
	errPrint        = errors.New("stdout closed")
)

// mockAPI is a double for the control plane, wide enough to satisfy every
// handler's own interface.
type mockAPI struct{ mock.Mock }

func (m *mockAPI) ProjectApply(ctx context.Context, req apimodel.ProjectApplyRequest) (apimodel.ProjectApplyResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(apimodel.ProjectApplyResponse), args.Error(1)
}

func (m *mockAPI) ProjectList(ctx context.Context) (apimodel.ProjectListResponse, error) {
	args := m.Called(ctx)
	return args.Get(0).(apimodel.ProjectListResponse), args.Error(1)
}

func (m *mockAPI) ProjectRemove(ctx context.Context, req apimodel.ProjectRemoveRequest) error {
	return m.Called(ctx, req).Error(0)
}

func (m *mockAPI) SessionCreate(ctx context.Context, req apimodel.SessionCreateRequest) (apimodel.SessionCreateResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(apimodel.SessionCreateResponse), args.Error(1)
}

func (m *mockAPI) SessionList(ctx context.Context, req apimodel.SessionListRequest) (apimodel.SessionListResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(apimodel.SessionListResponse), args.Error(1)
}

func (m *mockAPI) SessionStatus(ctx context.Context, req apimodel.SessionStatusRequest) (any, error) {
	args := m.Called(ctx, req)
	return args.Get(0), args.Error(1)
}

func (m *mockAPI) SavepointCreate(ctx context.Context, req apimodel.SavepointCreateRequest) (apimodel.SavepointCreateResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(apimodel.SavepointCreateResponse), args.Error(1)
}

func (m *mockAPI) SavepointList(ctx context.Context, req apimodel.SavepointListRequest) (apimodel.SavepointListResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(apimodel.SavepointListResponse), args.Error(1)
}

func (m *mockAPI) SavepointRemove(ctx context.Context, req apimodel.SavepointRemoveRequest) error {
	return m.Called(ctx, req).Error(0)
}

func (m *mockAPI) SavepointRollback(ctx context.Context, req apimodel.SavepointRollbackRequest) (apimodel.SavepointRollbackResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(apimodel.SavepointRollbackResponse), args.Error(1)
}

func (m *mockAPI) Recommend(ctx context.Context, req apimodel.RecommendRequest) (apimodel.RecommendResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(apimodel.RecommendResponse), args.Error(1)
}

func (m *mockAPI) ProviderList(ctx context.Context) (apimodel.ProviderListResponse, error) {
	args := m.Called(ctx)
	return args.Get(0).(apimodel.ProviderListResponse), args.Error(1)
}

// capturePrinter is a double for printer that records what was rendered.
type capturePrinter struct{ mock.Mock }

func (p *capturePrinter) Print(v any) error {
	return p.Called(v).Error(0)
}

func (p *capturePrinter) printed() []any {
	var vs []any
	for _, c := range p.Calls {
		if c.Method == "Print" {
			vs = append(vs, c.Arguments[0])
		}
	}
	return vs
}
