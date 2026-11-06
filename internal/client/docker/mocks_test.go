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

package docker

import (
	"context"
	"io"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"
)

// mockAPI is a test double for the Docker API client (the dockerAPI seam), so
// Engine's volume I/O can be exercised without a daemon.
type mockAPI struct {
	mock.Mock
}

func (m *mockAPI) ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.ContainerListResult), args.Error(1)
}

func (m *mockAPI) ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(client.ContainerInspectResult), args.Error(1)
}

func (m *mockAPI) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.ContainerCreateResult), args.Error(1)
}

func (m *mockAPI) ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, m.Called(ctx, containerID, options).Error(0)
}

func (m *mockAPI) ContainerAttach(ctx context.Context, containerID string, options client.ContainerAttachOptions) (client.ContainerAttachResult, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(client.ContainerAttachResult), args.Error(1)
}

func (m *mockAPI) ContainerWait(ctx context.Context, containerID string, options client.ContainerWaitOptions) client.ContainerWaitResult {
	return m.Called(ctx, containerID, options).Get(0).(client.ContainerWaitResult)
}

func (m *mockAPI) ContainerLogs(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	args := m.Called(ctx, containerID, options)
	logs, _ := args.Get(0).(client.ContainerLogsResult)
	return logs, args.Error(1)
}

func (m *mockAPI) ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, m.Called(ctx, containerID, options).Error(0)
}

func (m *mockAPI) CopyFromContainer(ctx context.Context, containerID string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(client.CopyFromContainerResult), args.Error(1)
}

func (m *mockAPI) CopyToContainer(ctx context.Context, containerID string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	return client.CopyToContainerResult{}, m.Called(ctx, containerID, options).Error(0)
}

func (m *mockAPI) ContainerCommit(ctx context.Context, containerID string, options client.ContainerCommitOptions) (client.ContainerCommitResult, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(client.ContainerCommitResult), args.Error(1)
}

func (m *mockAPI) ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	args := m.Called(ctx, imageID)
	return args.Get(0).(client.ImageInspectResult), args.Error(1)
}

func (m *mockAPI) ImageList(ctx context.Context, options client.ImageListOptions) (client.ImageListResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.ImageListResult), args.Error(1)
}

func (m *mockAPI) ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	args := m.Called(ctx, ref, options)
	pulled, _ := args.Get(0).(client.ImagePullResponse)
	return pulled, args.Error(1)
}

func (m *mockAPI) ImageSave(ctx context.Context, imageIDs []string, opts ...client.ImageSaveOption) (client.ImageSaveResult, error) {
	args := m.Called(ctx, imageIDs)
	saved, _ := args.Get(0).(client.ImageSaveResult)
	return saved, args.Error(1)
}

func (m *mockAPI) ImageLoad(ctx context.Context, input io.Reader, opts ...client.ImageLoadOption) (client.ImageLoadResult, error) {
	args := m.Called(ctx, input)
	loaded, _ := args.Get(0).(client.ImageLoadResult)
	return loaded, args.Error(1)
}

func (m *mockAPI) ImageTag(ctx context.Context, options client.ImageTagOptions) (client.ImageTagResult, error) {
	return client.ImageTagResult{}, m.Called(ctx, options).Error(0)
}

func (m *mockAPI) ImageRemove(ctx context.Context, imageID string, options client.ImageRemoveOptions) (client.ImageRemoveResult, error) {
	args := m.Called(ctx, imageID, options)
	removed, _ := args.Get(0).(client.ImageRemoveResult)
	return removed, args.Error(1)
}

func (m *mockAPI) Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.SystemInfoResult), args.Error(1)
}

func (m *mockAPI) Ping(ctx context.Context, options client.PingOptions) (client.PingResult, error) {
	args := m.Called(ctx, options)
	return args.Get(0).(client.PingResult), args.Error(1)
}

func (m *mockAPI) VolumeInspect(ctx context.Context, volumeID string, options client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	args := m.Called(ctx, volumeID, options)
	return args.Get(0).(client.VolumeInspectResult), args.Error(1)
}

func (m *mockAPI) VolumeRemove(ctx context.Context, volumeID string, options client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	return client.VolumeRemoveResult{}, m.Called(ctx, volumeID, options).Error(0)
}

func (m *mockAPI) Close() error { return m.Called().Error(0) }
