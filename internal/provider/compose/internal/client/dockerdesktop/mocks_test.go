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

package dockerdesktop

import (
	"context"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"
)

type mockAPI struct {
	client.APIClient
	mock.Mock
}

func (m *mockAPI) ContainerInspect(
	ctx context.Context, containerID string, options client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	args := m.Called(ctx, containerID, options)
	return client.ContainerInspectResult{Container: args.Get(0).(container.InspectResponse)}, args.Error(1)
}

func (m *mockAPI) ContainerList(
	ctx context.Context, options client.ContainerListOptions,
) (client.ContainerListResult, error) {
	args := m.Called(ctx, options)
	return client.ContainerListResult{Items: args.Get(0).([]container.Summary)}, args.Error(1)
}
