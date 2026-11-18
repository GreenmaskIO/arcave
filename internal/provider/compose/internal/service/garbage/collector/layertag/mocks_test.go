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

package layertag

import (
	"context"

	"github.com/stretchr/testify/mock"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// mockDaemon is the daemon as the collector reads it.
type mockDaemon struct {
	mock.Mock
}

func (m *mockDaemon) Ping(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockDaemon) ListLayerImages(ctx context.Context) ([]composemodel.TaggedImage, error) {
	args := m.Called(ctx)
	return args.Get(0).([]composemodel.TaggedImage), args.Error(1)
}

func (m *mockDaemon) ListContainerImages(ctx context.Context) ([]composemodel.ContainerImage, error) {
	args := m.Called(ctx)
	return args.Get(0).([]composemodel.ContainerImage), args.Error(1)
}
