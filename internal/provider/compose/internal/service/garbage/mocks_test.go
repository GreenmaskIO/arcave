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

package garbage

import (
	"context"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockSavepointReader struct{ mock.Mock }

func (m *mockSavepointReader) LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]model.Savepoint, error) {
	args := m.Called(ctx, ids)
	return args.Get(0).([]model.Savepoint), args.Error(1)
}

type mockCollector struct{ mock.Mock }

func (m *mockCollector) Kind() model.GarbageCollectorKind {
	return m.Called().Get(0).(model.GarbageCollectorKind)
}

func (m *mockCollector) DataKind() model.GarbageDataKind {
	return m.Called().Get(0).(model.GarbageDataKind)
}

type mockDaemonCollector struct{ mockCollector }

func (m *mockDaemonCollector) Find(ctx context.Context, scope DaemonScope) (model.GarbageStat, error) {
	args := m.Called(ctx, scope)
	stat, _ := args.Get(0).(model.GarbageStat)
	return stat, args.Error(1)
}

type mockComposeProjectCollector struct{ mockCollector }

func (m *mockComposeProjectCollector) Find(ctx context.Context, scope ComposeProjectScope) (model.GarbageStat, error) {
	args := m.Called(ctx, scope)
	stat, _ := args.Get(0).(model.GarbageStat)
	return stat, args.Error(1)
}

// mockProbeCollector is a daemon collector reading something besides the store.
type mockProbeCollector struct{ mockDaemonCollector }

func (m *mockProbeCollector) Probe(ctx context.Context) error { return m.Called(ctx).Error(0) }

// mockComposeProjectProbeCollector is a compose-project collector reading something besides the store.
type mockComposeProjectProbeCollector struct{ mockComposeProjectCollector }

func (m *mockComposeProjectProbeCollector) Probe(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// mockEraser doubles ComposeProjectEraser.
type mockEraser struct{ mock.Mock }

func (m *mockEraser) Action() model.GarbageAction {
	return m.Called().Get(0).(model.GarbageAction)
}

func (m *mockEraser) Erase(ctx context.Context, store storages.Storager, ops []model.GarbageOp) model.GarbageDeletion {
	return m.Called(ctx, store, ops).Get(0).(model.GarbageDeletion)
}

// mockDaemonEraser doubles DaemonEraser.
type mockDaemonEraser struct{ mock.Mock }

func (m *mockDaemonEraser) Action() model.GarbageAction {
	return m.Called().Get(0).(model.GarbageAction)
}

func (m *mockDaemonEraser) Erase(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion {
	return m.Called(ctx, ops).Get(0).(model.GarbageDeletion)
}
