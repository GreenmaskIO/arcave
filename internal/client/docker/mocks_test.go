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

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/mock"
)

// mockAPI is a test double for the Docker API client (the dockerAPI seam), so
// Engine's volume I/O can be exercised without a daemon.
type mockAPI struct {
	mock.Mock
}

func (m *mockAPI) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	args := m.Called(ctx, options)
	return args.Get(0).([]container.Summary), args.Error(1)
}

func (m *mockAPI) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	args := m.Called(ctx, containerID)
	return args.Get(0).(container.InspectResponse), args.Error(1)
}

func (m *mockAPI) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	args := m.Called(ctx, config, hostConfig, networkingConfig, platform, containerName)
	return args.Get(0).(container.CreateResponse), args.Error(1)
}

func (m *mockAPI) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	return m.Called(ctx, containerID, options).Error(0)
}

func (m *mockAPI) ContainerAttach(ctx context.Context, containerID string, options container.AttachOptions) (dockertypes.HijackedResponse, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(dockertypes.HijackedResponse), args.Error(1)
}

func (m *mockAPI) ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	args := m.Called(ctx, containerID, condition)
	return args.Get(0).(<-chan container.WaitResponse), args.Get(1).(<-chan error)
}

func (m *mockAPI) ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *mockAPI) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	return m.Called(ctx, containerID, options).Error(0)
}

func (m *mockAPI) CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, container.PathStat, error) {
	args := m.Called(ctx, containerID, srcPath)
	return args.Get(0).(io.ReadCloser), args.Get(1).(container.PathStat), args.Error(2)
}

func (m *mockAPI) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader, options container.CopyToContainerOptions) error {
	return m.Called(ctx, containerID, dstPath, content, options).Error(0)
}

func (m *mockAPI) ContainerCommit(ctx context.Context, containerID string, options container.CommitOptions) (container.CommitResponse, error) {
	args := m.Called(ctx, containerID, options)
	return args.Get(0).(container.CommitResponse), args.Error(1)
}

func (m *mockAPI) ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (image.InspectResponse, error) {
	args := m.Called(ctx, imageID)
	return args.Get(0).(image.InspectResponse), args.Error(1)
}

func (m *mockAPI) ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error) {
	args := m.Called(ctx, options)
	return args.Get(0).([]image.Summary), args.Error(1)
}

func (m *mockAPI) ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	args := m.Called(ctx, ref, options)
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *mockAPI) ImageSave(ctx context.Context, imageIDs []string, opts ...client.ImageSaveOption) (io.ReadCloser, error) {
	args := m.Called(ctx, imageIDs)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockAPI) ImageLoad(ctx context.Context, input io.Reader, opts ...client.ImageLoadOption) (image.LoadResponse, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(image.LoadResponse), args.Error(1)
}

func (m *mockAPI) ImageTag(ctx context.Context, source, target string) error {
	return m.Called(ctx, source, target).Error(0)
}

func (m *mockAPI) ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error) {
	args := m.Called(ctx, imageID, options)
	deleted, _ := args.Get(0).([]image.DeleteResponse)
	return deleted, args.Error(1)
}

func (m *mockAPI) Info(ctx context.Context) (system.Info, error) {
	args := m.Called(ctx)
	return args.Get(0).(system.Info), args.Error(1)
}

func (m *mockAPI) Ping(ctx context.Context) (dockertypes.Ping, error) {
	args := m.Called(ctx)
	return args.Get(0).(dockertypes.Ping), args.Error(1)
}

func (m *mockAPI) VolumeInspect(ctx context.Context, volumeID string) (volume.Volume, error) {
	args := m.Called(ctx, volumeID)
	return args.Get(0).(volume.Volume), args.Error(1)
}

func (m *mockAPI) VolumeRemove(ctx context.Context, volumeID string, force bool) error {
	return m.Called(ctx, volumeID, force).Error(0)
}

func (m *mockAPI) Close() error { return m.Called().Error(0) }
