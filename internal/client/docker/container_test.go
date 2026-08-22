package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestEngine_RecreateWithImage(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	inspect := api.On("ContainerInspect", mock.Anything, replacedID).Return(builtReplica(), nil).Once()
	remove := api.On("ContainerRemove", mock.Anything, replacedID,
		container.RemoveOptions{RemoveVolumes: true}).Return(nil).Once()
	var gotConfig *container.Config
	var gotHostConfig *container.HostConfig
	var gotNetworking *network.NetworkingConfig
	var gotPlatform *ocispec.Platform
	create := api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, "demo-app-2").
		Run(func(args mock.Arguments) {
			gotConfig = args.Get(1).(*container.Config)
			gotHostConfig = args.Get(2).(*container.HostConfig)
			gotNetworking = args.Get(3).(*network.NetworkingConfig)
			gotPlatform, _ = args.Get(4).(*ocispec.Platform)
		}).
		Return(container.CreateResponse{ID: "new"}, nil).Once()

	// The container must be gone before its name is claimed again, and inspected
	// before it is gone.
	mock.InOrder(inspect, remove, create)
	e := &Engine{api: api}

	// Act
	id, err := e.RecreateWithImage(context.Background(), replacedID, "arcave/demo-app-2:sp-1780335122")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "new", id)
	assert.Equal(t, "arcave/demo-app-2:sp-1780335122", gotConfig.Image)
	assert.Equal(t, "demo-app-2", gotConfig.Hostname)
	assert.Equal(t, []string{"MODE=original"}, gotConfig.Env, "the environment goes over whole")
	assert.Equal(t, map[string]string{"com.docker.compose.container-number": "2"}, gotConfig.Labels)
	assert.Equal(t, []string{"back"}, gotHostConfig.Binds)
	assert.Equal(t, []string{"NET_ADMIN"}, []string(gotHostConfig.CapAdd), "the host config goes over whole")
	assert.Equal(t, &ocispec.Platform{OS: "linux", Architecture: "arm64"}, gotPlatform,
		"an emulated stack rebuilds emulated")

	endpoint := gotNetworking.EndpointsConfig["demo_back"]
	require.NotNil(t, endpoint)
	assert.Equal(t, []string{"app-alias"}, endpoint.Aliases)
	assert.Equal(t, map[string]string{"opt": "value"}, endpoint.DriverOpts)
	assert.Empty(t, endpoint.NetworkID, "an endpoint the daemon assigned belongs to the container it replaces")
	assert.Empty(t, endpoint.EndpointID)
	assert.Empty(t, endpoint.IPAddress)
	assert.Empty(t, endpoint.Gateway)
	api.AssertExpectations(t)
}

// A hostname the daemon derived from the container being replaced would follow a
// container that no longer exists; one the service declared must not be lost.
func TestEngine_RecreateWithImage_Hostname(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		want     string
	}{
		{name: "the daemon's default is the old container's short id", hostname: replacedID[:12]},
		{name: "a declared hostname survives", hostname: "app-host", want: "app-host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			replica := builtReplica()
			replica.Config.Hostname = tt.hostname
			api := &mockAPI{}
			api.On("ContainerInspect", mock.Anything, replacedID).Return(replica, nil).Once()
			api.On("ContainerRemove", mock.Anything, replacedID, mock.Anything).Return(nil).Once()
			var got string
			api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything,
				mock.Anything, "demo-app-2").
				Run(func(args mock.Arguments) { got = args.Get(1).(*container.Config).Hostname }).
				Return(container.CreateResponse{ID: "new"}, nil).Once()
			e := &Engine{api: api}

			// Act
			_, err := e.RecreateWithImage(context.Background(), replacedID, "arcave/demo-app-2:sp-1780335122")

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			api.AssertExpectations(t)
		})
	}
}

func TestEngine_RecreateWithImage_AFailedCreateLeavesTheContainerRemoved(t *testing.T) {
	// Arrange
	wantErr := errors.New("no such image")
	api := &mockAPI{}
	api.On("ContainerInspect", mock.Anything, replacedID).Return(builtReplica(), nil).Once()
	api.On("ContainerRemove", mock.Anything, replacedID, container.RemoveOptions{RemoveVolumes: true}).
		Return(nil).Once()
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, "demo-app-2").Return(container.CreateResponse{}, wantErr).Once()
	e := &Engine{api: api}

	// Act
	id, err := e.RecreateWithImage(context.Background(), replacedID, "arcave/demo-app-2:sp-1780335122")

	// Assert
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, id)
	api.AssertExpectations(t)
}

func TestEngine_RecreateWithImage_RefusesAContainerItCannotRebuild(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ContainerInspect", mock.Anything, replacedID).
		Return(container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{Name: "/demo-app-2"}}, nil).Once()
	e := &Engine{api: api}

	// Act
	_, err := e.RecreateWithImage(context.Background(), replacedID, "arcave/demo-app-2:sp-1780335122")

	// Assert
	require.ErrorContains(t, err, "no configuration")
	api.AssertExpectations(t)
}

// replacedID is the container the fixup replaces; long enough for the daemon's
// short-id hostname default to be recognisable.
const replacedID = "cfbafac79192d0ea4de513a1b3a6a9d163d3274f32db5214d08c6e483597812e"

// builtReplica is one replica as the daemon reports it right after compose created
// it: declared configuration, plus the endpoint fields the daemon filled in.
func builtReplica() container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:         replacedID,
			Name:       "/demo-app-2",
			HostConfig: &container.HostConfig{Binds: []string{"back"}, CapAdd: []string{"NET_ADMIN"}},
		},
		ImageManifestDescriptor: &ocispec.Descriptor{
			Platform: &ocispec.Platform{OS: "linux", Architecture: "arm64"},
		},
		Config: &container.Config{
			Image:    "arcave/demo-app-1:sp-1780335122",
			Hostname: "demo-app-2",
			Env:      []string{"MODE=original"},
			Labels:   map[string]string{"com.docker.compose.container-number": "2"},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"demo_back": {
					Aliases:    []string{"app-alias"},
					DriverOpts: map[string]string{"opt": "value"},
					NetworkID:  "netid",
					EndpointID: "epid",
					IPAddress:  "172.18.0.3",
					Gateway:    "172.18.0.1",
				},
			},
		},
	}
}
