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

package eraser

import (
	"context"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

type mockPrunerOpener struct{ mock.Mock }

func (m *mockPrunerOpener) Open(ctx context.Context, store storages.Navigator) (Pruner, error) {
	args := m.Called(ctx, store)
	pruner, _ := args.Get(0).(Pruner)
	return pruner, args.Error(1)
}

type mockPruner struct{ mock.Mock }

func (m *mockPruner) Forget(ctx context.Context, objectID string, ids []string) (*int64, error) {
	args := m.Called(ctx, objectID, ids)
	freed, _ := args.Get(0).(*int64)
	return freed, args.Error(1)
}

func (m *mockPruner) Close(ctx context.Context) {
	m.Called(ctx)
}

type mockTagRemover struct{ mock.Mock }

func (m *mockTagRemover) RemoveImageRef(ctx context.Context, ref model.ImageRef) error {
	return m.Called(ctx, ref).Error(0)
}
