// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

func (m *mockService) ListSavepoints(ctx context.Context, req model.ListRequest) ([]model.Savepoint, error) {
	args := m.Called(ctx, req)
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

func (m *mockService) ListSessions(ctx context.Context, req model.ListSessionsRequest) ([]model.SessionSummary, error) {
	args := m.Called(ctx, req)
	var sessions []model.SessionSummary
	if v := args.Get(0); v != nil {
		sessions = v.([]model.SessionSummary)
	}
	return sessions, args.Error(1)
}

func (m *mockService) CreateSession(ctx context.Context, req model.CreateSessionRequest) (model.Session, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(model.Session), args.Error(1)
}

func (m *mockService) DeleteSession(
	ctx context.Context, req model.DeleteSessionRequest,
) (model.DeleteSessionResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(model.DeleteSessionResponse), args.Error(1)
}
