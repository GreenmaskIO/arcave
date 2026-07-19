package docker

import (
	"context"
	"io"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
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

func (m *mockAPI) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	args := m.Called(ctx, config, hostConfig, networkingConfig, platform, containerName)
	return args.Get(0).(container.CreateResponse), args.Error(1)
}

func (m *mockAPI) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	return m.Called(ctx, containerID, options).Error(0)
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

func (m *mockAPI) ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (image.InspectResponse, error) {
	args := m.Called(ctx, imageID)
	return args.Get(0).(image.InspectResponse), args.Error(1)
}

func (m *mockAPI) ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	args := m.Called(ctx, ref, options)
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *mockAPI) Close() error { return m.Called().Error(0) }

// mockCompose is a test double for the compose library (the composeService
// seam), so ComposeRunner's option assembly can be asserted without a daemon.
type mockCompose struct {
	mock.Mock
}

func (m *mockCompose) Stop(ctx context.Context, projectName string, options api.StopOptions) error {
	return m.Called(ctx, projectName, options).Error(0)
}

func (m *mockCompose) Up(ctx context.Context, project *types.Project, options api.UpOptions) error {
	return m.Called(ctx, project, options).Error(0)
}
