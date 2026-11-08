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

package podman

import (
	"context"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/model"
)

// mockLibpod is a test double for libpod.Client (the libpodAPI seam).
type mockLibpod struct {
	mock.Mock
}

func (m *mockLibpod) InspectContainer(ctx context.Context, containerID string) (libpod.Container, error) {
	args := m.Called(ctx, containerID)
	return args.Get(0).(libpod.Container), args.Error(1)
}

func (m *mockLibpod) CommitContainer(ctx context.Context, options libpod.CommitOptions) (string, error) {
	args := m.Called(ctx, options)
	return args.String(0), args.Error(1)
}

func (m *mockLibpod) ExportImage(ctx context.Context, imageID, format string) (io.ReadCloser, error) {
	args := m.Called(ctx, imageID, format)
	archive, _ := args.Get(0).(io.ReadCloser)
	return archive, args.Error(1)
}

// mockCompatAPI is a test double for the Docker API of the daemon; a call it does not
// answer panics on the nil client it embeds.
type mockCompatAPI struct {
	client.APIClient
	mock.Mock
}

func (m *mockCompatAPI) ContainerInspect(
	ctx context.Context, containerID string, options client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	args := m.Called(ctx, containerID, options)
	return client.ContainerInspectResult{Container: args.Get(0).(container.InspectResponse)}, args.Error(1)
}

func (m *mockCompatAPI) ContainerRemove(
	ctx context.Context, containerID string, options client.ContainerRemoveOptions,
) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, m.Called(ctx, containerID, options).Error(0)
}

func (m *mockCompatAPI) ContainerCreate(
	ctx context.Context, options client.ContainerCreateOptions,
) (client.ContainerCreateResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.ContainerCreateResult), args.Error(1)
}

// mockOneShotRunner is a test double for the engine a volume export runs its container through.
type mockOneShotRunner struct {
	mock.Mock
}

func (m *mockOneShotRunner) RunOneShot(ctx context.Context, spec model.OneShotSpec) error {
	return m.Called(ctx, spec).Error(0)
}
