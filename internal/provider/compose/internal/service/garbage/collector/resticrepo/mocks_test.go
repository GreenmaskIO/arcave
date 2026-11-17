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

package resticrepo

import (
	"context"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
)

type mockPinger struct{ mock.Mock }

func (m *mockPinger) Ping(ctx context.Context) error { return m.Called(ctx).Error(0) }

type mockOpener struct{ mock.Mock }

func (m *mockOpener) Open(ctx context.Context, store storages.Navigator) (Inspector, error) {
	args := m.Called(ctx, store)
	inspector, _ := args.Get(0).(Inspector)
	return inspector, args.Error(1)
}

type mockInspector struct{ mock.Mock }

func (m *mockInspector) ReadSnapshots(ctx context.Context, objectID string) ([]cdc.Snapshot, error) {
	args := m.Called(ctx, objectID)
	snapshots, _ := args.Get(0).([]cdc.Snapshot)
	return snapshots, args.Error(1)
}

func (m *mockInspector) EstimateForget(ctx context.Context, objectID string, ids []string) (*int64, error) {
	args := m.Called(ctx, objectID, ids)
	freed, _ := args.Get(0).(*int64)
	return freed, args.Error(1)
}

func (m *mockInspector) Close(ctx context.Context) { m.Called(ctx) }
